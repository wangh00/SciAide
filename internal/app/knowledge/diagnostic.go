package knowledge

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/document"
)

type ParseQuality string

const (
	ParseQualityGood        ParseQuality = "good"
	ParseQualityWarning     ParseQuality = "warning"
	ParseQualityPoor        ParseQuality = "poor"
	ParseQualityUnavailable ParseQuality = "unavailable"
)

type ParseDiagnostic struct {
	Format         document.Format `json:"format"`
	Quality        ParseQuality    `json:"quality"`
	Summary        string          `json:"summary"`
	Warnings       []string        `json:"warnings"`
	SizeBytes      int64           `json:"sizeBytes"`
	UnitCount      int             `json:"unitCount"`
	ExtractedRunes int             `json:"extractedRunes"`
	Truncated      bool            `json:"truncated"`
	Pages          int             `json:"pages,omitempty"`
	TextPages      int             `json:"textPages,omitempty"`
	EmptyPages     int             `json:"emptyPages,omitempty"`
	Sections       int             `json:"sections,omitempty"`
	Headings       int             `json:"headings,omitempty"`
	Tables         int             `json:"tables,omitempty"`
	Sheets         int             `json:"sheets,omitempty"`
	Parser         string          `json:"parser,omitempty"`
}

func buildParseDiagnostic(value attachment.Attachment) ParseDiagnostic {
	metadata := value.ParseMetadata
	result := ParseDiagnostic{
		Format: value.Format, Quality: ParseQualityGood, Warnings: []string{},
		SizeBytes: value.SizeBytes, UnitCount: value.UnitCount, ExtractedRunes: value.ExtractedRunes, Truncated: value.Truncated,
		Pages: metadataInt(metadata, "pages"), TextPages: metadataInt(metadata, "textPages"), EmptyPages: metadataInt(metadata, "emptyPages"),
		Sections: metadataInt(metadata, "sections"), Headings: metadataInt(metadata, "headings"), Tables: metadataInt(metadata, "tables"), Sheets: metadataInt(metadata, "sheets"),
		Parser: firstMetadata(metadata, "structureParser"),
	}
	if value.Status == attachment.StatusFailed {
		result.Quality = ParseQualityUnavailable
		result.Summary = "文档解析失败"
		result.Warnings = append(result.Warnings, boundedDiagnostic(value.ErrorMessage))
		return result
	}
	if value.UnitCount == 0 || value.ExtractedRunes == 0 {
		result.Quality = ParseQualityPoor
		result.Summary = "没有提取到可检索文本"
		result.Warnings = append(result.Warnings, "文件可能是扫描件、空文档或使用了当前解析器不支持的编码。")
		return result
	}
	if value.Truncated {
		result.Quality = ParseQualityWarning
		result.Warnings = append(result.Warnings, "文本超过本地解析上限，索引只包含前部内容。")
	}
	switch value.Format {
	case document.FormatPDF:
		result.Summary = fmt.Sprintf("%d 个结构单元 · %s 字符", value.UnitCount, formatDiagnosticNumber(value.ExtractedRunes))
		if result.Pages > 0 {
			result.Summary = fmt.Sprintf("%d/%d 页含文本 · %s 字符", result.TextPages, result.Pages, formatDiagnosticNumber(value.ExtractedRunes))
			if result.TextPages == 0 {
				result.Quality = ParseQualityPoor
				result.Warnings = append(result.Warnings, "所有页面都缺少可提取文本；当前版本不内置 OCR。")
			} else {
				coverage := float64(result.TextPages) / float64(result.Pages)
				average := value.ExtractedRunes / result.TextPages
				if coverage < 0.5 || average < 40 {
					result.Quality = ParseQualityPoor
					result.Warnings = append(result.Warnings, "可提取页面或每页文本过少，检索结果可能不完整。")
				} else if coverage < 0.9 || average < 120 {
					setDiagnosticWarning(&result)
					result.Warnings = append(result.Warnings, "部分页面文本较少，请核对原始 PDF。")
				}
			}
		}
	case document.FormatDOCX:
		result.Summary = fmt.Sprintf("%d 个段落单元 · %d 个标题 · %d 个表格", value.UnitCount, result.Headings, result.Tables)
	case document.FormatXLSX:
		result.Summary = fmt.Sprintf("%d 个工作表 · %d 个可读行", result.Sheets, value.UnitCount)
	default:
		result.Summary = fmt.Sprintf("%d 个可读单元 · %s 字符", value.UnitCount, formatDiagnosticNumber(value.ExtractedRunes))
	}
	return result
}

func jobProgress(documentStatus DocumentStatus, job *ImportJob) int {
	if job == nil {
		if documentStatus == DocumentReady {
			return 100
		}
		return 0
	}
	switch job.Status {
	case JobCompleted:
		return 100
	case JobFailed, JobCancelled:
		return 0
	}
	switch job.Stage {
	case "queued":
		return 5
	case "loading":
		return 20
	case "chunking":
		return 55
	case "indexing":
		return 85
	case "completed":
		return 100
	default:
		return 0
	}
}

func metadataInt(metadata map[string]string, key string) int {
	value, _ := strconv.Atoi(strings.TrimSpace(metadata[key]))
	if value < 0 {
		return 0
	}
	return value
}

func firstMetadata(metadata map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(metadata[key]); value != "" {
			return value
		}
	}
	return ""
}

func setDiagnosticWarning(value *ParseDiagnostic) {
	if value.Quality == ParseQualityGood {
		value.Quality = ParseQualityWarning
	}
}

func boundedDiagnostic(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "解析器没有返回可用的错误详情。"
	}
	runes := []rune(value)
	if len(runes) > 300 {
		value = string(runes[:300]) + "..."
	}
	return value
}

func formatDiagnosticNumber(value int) string {
	if value >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	}
	if value >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(value)/1_000)
	}
	return strconv.Itoa(value)
}

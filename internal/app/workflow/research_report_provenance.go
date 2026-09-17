package workflow

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
)

const researchReportVersion = "dynamic-report-v5"
const retrievalAppendixHeading = "## 附录：检索方法记录"

func usesReportProvenance(node CompiledNode) bool {
	return node.ID == "report_drafting" && (node.PromptVersion == researchReportVersion || strings.HasPrefix(node.PromptVersion, researchReportVersion+"-"))
}

func reportRetrievalAppendix(input json.RawMessage) (string, error) {
	var stage struct {
		Contract struct {
			Query string `json:"query"`
		} `json:"researchContract"`
		Source struct {
			Retrieval *struct {
				Queries        []string                `json:"queries"`
				Sources        []research.SourceSearch `json:"sources"`
				CandidateCount int                     `json:"candidateCount"`
				CompletedAt    *string                 `json:"completedAt"`
				FirstPageOnly  bool                    `json:"firstPageOnly"`
			} `json:"retrieval"`
		} `json:"researchSourceContext"`
	}
	if err := json.Unmarshal(input, &stage); err != nil {
		return "", fmt.Errorf("读取报告检索记录失败：%w", err)
	}
	r := stage.Source.Retrieval
	if r == nil {
		return "", nil
	}
	var b strings.Builder
	b.WriteString(retrievalAppendixHeading + "\n\n检索记录用于复核获取范围，不代表已穷尽所有相关研究。来源请求失败不等同于没有相关文献。\n")
	if r.CompletedAt != nil {
		b.WriteString("\n检索完成时间：" + *r.CompletedAt + "\n")
	}
	if r.FirstPageOnly {
		b.WriteString("\n范围：每个检索式、每个启用来源仅获取首页，未自动翻页。\n")
	}
	fmt.Fprintf(&b, "\n合并去重候选数：%d；候选数不等于纳入文献数或独立研究数。\n", r.CandidateCount)
	if stage.Contract.Query != "" {
		b.WriteString("\n### 原定检索式\n\n" + retrievalCodeBlock(stage.Contract.Query))
	}
	b.WriteString("\n### 实际执行检索式\n")
	for index, query := range r.Queries {
		fmt.Fprintf(&b, "\n检索式 %d：\n\n%s", index+1, retrievalCodeBlock(query))
	}
	if len(r.Queries) == 0 {
		b.WriteString("\n当前记录未提供完整检索式。\n")
	}
	if stage.Contract.Query != "" && (len(r.Queries) != 1 || r.Queries[0] != stage.Contract.Query) {
		b.WriteString("\n实际检索式与原定文本不完全相同，以上并列保留原文供核对；不据此断言两者语义等价。\n")
	}
	b.WriteString("\n### 来源执行记录\n\n各次来源请求的检索语法、年份限制及返回状态如下；返回数量不是最终纳入数量。\n")
	for index, source := range r.Sources {
		// Encode the recorded fields, without guessing a query the provider never
		// reported. A fenced value cannot inject report Markdown structure.
		entry := map[string]any{"来源": source.SourceID, "状态": source.Status, "返回条数": source.Count, "实际查询": source.ProviderQuery, "有效检索式": source.EffectiveQuery, "查询模式": source.QueryMode, "年份限制": source.PublicationYears, "已应用年份限制": source.YearFilterApplied, "达到返回上限": source.LimitReached}
		if source.ErrorCode != "" {
			entry["错误代码"] = source.ErrorCode
		}
		encoded, err := json.MarshalIndent(entry, "", "  ")
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "\n来源请求 %d：\n\n%s", index+1, retrievalCodeBlock(string(encoded)))
	}
	return strings.TrimSpace(b.String()), nil
}

func retrievalCodeBlock(value string) string {
	fence := "```"
	for strings.Contains(value, fence) {
		fence += "`"
	}
	return fence + "text\n" + value + "\n" + fence + "\n"
}

func enrichReportProvenance(value, input json.RawMessage) (json.RawMessage, []AIStageNormalization, error) {
	appendix, err := reportRetrievalAppendix(input)
	if err != nil || appendix == "" {
		return value, nil, err
	}
	fields := decodeObject(value)
	markdown, ok := fields["markdown"].(string)
	if !ok {
		return nil, nil, fmt.Errorf("报告缺少 Markdown 正文")
	}
	if strings.HasSuffix(strings.TrimSpace(markdown), appendix) {
		return value, nil, nil
	}
	// An author can omit the appendix entirely. Never silently replace an
	// altered one, which could hide a discrepancy in the research methods.
	if strings.Contains(markdown, retrievalAppendixHeading) {
		return nil, nil, fmt.Errorf("检索方法记录由真实日志自动附加，请省略该附录，不要改写旧附录；正文方法和科学局限仍需保留")
	}
	updated := strings.TrimSpace(markdown) + "\n\n" + appendix
	fields["markdown"] = updated
	return mustJSON(fields), []AIStageNormalization{{Path: "$.markdown", Rule: "append_frozen_retrieval_log", BeforeSHA256: hashBytes([]byte(markdown)), AfterSHA256: hashBytes([]byte(updated))}}, nil
}

func validateReportProvenance(node CompiledNode, value, input json.RawMessage) error {
	if !usesReportProvenance(node) {
		return nil
	}
	appendix, err := reportRetrievalAppendix(input)
	if err != nil {
		return err
	}
	var report struct {
		Markdown     string   `json:"markdown"`
		ClaimSummary []string `json:"claimSummary"`
		Method       string   `json:"methodSummary"`
		Limitations  []string `json:"limitations"`
	}
	if json.Unmarshal(value, &report) != nil || (appendix != "" && !strings.HasSuffix(strings.TrimSpace(report.Markdown), appendix)) {
		return fmt.Errorf("报告缺少与真实检索日志一致的附录")
	}
	// Narrow, explicit process-language checks only. Do not strip text or ban
	// citation markers, words such as 'review', or genuine research limitations.
	fields := map[string]string{"markdown": report.Markdown, "methodSummary": report.Method, "claimSummary": strings.Join(report.ClaimSummary, "\n"), "limitations": strings.Join(report.Limitations, "\n")}
	for name, text := range fields {
		for _, phrase := range []string{"显示时可呈现为常规编号引用", "这些标记不是额外文献", "独立审查曾要求", "获准撰写初稿", "获准在覆盖有限前提下撰写初稿"} {
			if strings.Contains(text, phrase) {
				return fmt.Errorf("报告 %s 残留内部流程说明 %q；请删除该说明，保留合法引用、科学内容和真实局限", name, phrase)
			}
		}
	}
	return (tool.JSONSchemaValidator{}).Validate(node.OutputSchema, value)
}

const reportProvenanceInstruction = `
The host appends a retrieval-methods appendix from the actual frozen search logs before review and delivery. Do not write, copy or edit the section titled "` + retrievalAppendixHeading + `"; omit it when revising an earlier draft. Keep a scientifically accurate methods summary and limitations in the authored report. The appendix preserves planned/executed queries, source adaptations, status, time and limits; it does not establish exhaustive coverage or semantic equivalence of changed queries. Keep review findings and response-to-review commentary out of all report fields. Preserve valid citations and scientific limitations.`

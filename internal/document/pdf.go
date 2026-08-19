package document

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"

	pdfreader "github.com/ledongthuc/pdf"
)

const (
	maxPDFTrailingDataScanBytes int64 = 1 << 20
	maxPDFAnalysisRunes               = MaxExtractedRunes + 256_000
)

const pdfEdgeLineWindow = 4

type pdfPage struct {
	number int
	lines  []string
}

func parsePDF(ctx context.Context, path string) (Parsed, error) {
	file, reader, err := openPDF(path)
	if err != nil {
		return Parsed{}, fmt.Errorf("open PDF: %w", err)
	}
	defer file.Close()
	pages := reader.NumPage()
	if pages < 0 || pages > 100_000 {
		return Parsed{}, fmt.Errorf("PDF page count is invalid")
	}
	extracted := make([]pdfPage, 0, min(pages, 1_000))
	analyzedRunes := 0
	analysisTruncated := false
	for pageNumber := 1; pageNumber <= pages; pageNumber++ {
		if err := ctx.Err(); err != nil {
			return Parsed{}, err
		}
		text, err := reader.Page(pageNumber).GetPlainText(nil)
		if err != nil {
			return Parsed{}, fmt.Errorf("extract PDF page %d: %w", pageNumber, err)
		}
		remaining := maxPDFAnalysisRunes - analyzedRunes
		textRunes := []rune(text)
		if len(textRunes) > remaining {
			textRunes = textRunes[:max(0, remaining)]
			analysisTruncated = true
		}
		analyzedRunes += len(textRunes)
		extracted = append(extracted, pdfPage{number: pageNumber, lines: normalizedSourceLines(string(textRunes))})
		if analysisTruncated || analyzedRunes >= maxPDFAnalysisRunes {
			analysisTruncated = pageNumber < pages || analysisTruncated
			break
		}
	}
	repeatedEdges := repeatedPDFEdgeLines(extracted)
	collect := collector{}
	textPages, emptyPages, sectionCount, removedEdges := 0, 0, 0, 0
	for _, page := range extracted {
		lines, removed := filterPDFEdgeLines(page.lines, repeatedEdges)
		removedEdges += removed
		if len(lines) == 0 {
			emptyPages++
			continue
		}
		textPages++
		blocks := splitPDFSections(lines)
		sectionCount += len(blocks)
		for _, block := range blocks {
			kind := "page"
			if block.title != "" {
				kind = "section"
			}
			if !collect.add(kind, fmt.Sprintf("page:%d", page.number), block.title, block.content) {
				break
			}
		}
		if collect.truncated {
			break
		}
	}
	metadata := map[string]string{
		"pages":            strconv.Itoa(pages),
		"processedPages":   strconv.Itoa(len(extracted)),
		"textPages":        strconv.Itoa(textPages),
		"emptyPages":       strconv.Itoa(emptyPages),
		"sections":         strconv.Itoa(sectionCount),
		"removedEdgeLines": strconv.Itoa(removedEdges),
		"structureParser":  "pdf-v2",
	}
	return Parsed{Units: collect.units, Metadata: metadata, Truncated: collect.truncated || analysisTruncated, ExtractedRunes: collect.runes}, nil
}

type pdfSection struct {
	title   string
	content string
}

func splitPDFSections(lines []string) []pdfSection {
	result := make([]pdfSection, 0, 4)
	current := make([]string, 0, len(lines))
	title := ""
	flush := func() {
		content := joinWrappedLines(current)
		if content != "" {
			result = append(result, pdfSection{title: title, content: content})
		}
		current = current[:0]
	}
	for _, line := range lines {
		if looksLikeHeading(line) {
			flush()
			title = line
			current = append(current, line)
			continue
		}
		current = append(current, line)
	}
	flush()
	return result
}

func repeatedPDFEdgeLines(pages []pdfPage) map[string]struct{} {
	counts := map[string]int{}
	pagesWithText := 0
	for _, page := range pages {
		if len(page.lines) == 0 {
			continue
		}
		pagesWithText++
		seen := map[string]struct{}{}
		for index, line := range page.lines {
			if !pdfEdgeIndex(index, len(page.lines)) {
				continue
			}
			key := canonicalEdgeLine(line)
			if key == "" || len([]rune(key)) > 160 {
				continue
			}
			seen[key] = struct{}{}
		}
		for key := range seen {
			counts[key]++
		}
	}
	result := map[string]struct{}{}
	if pagesWithText < 3 {
		return result
	}
	for key, count := range counts {
		if count >= 3 && count*2 >= pagesWithText {
			result[key] = struct{}{}
		}
	}
	return result
}

func filterPDFEdgeLines(lines []string, repeated map[string]struct{}) ([]string, int) {
	result := make([]string, 0, len(lines))
	removed := 0
	for index, line := range lines {
		if pdfEdgeIndex(index, len(lines)) {
			_, repeats := repeated[canonicalEdgeLine(line)]
			if repeats || isPageNumberLine(line) || decorativeLine(line) {
				removed++
				continue
			}
		}
		result = append(result, line)
	}
	return result, removed
}

func pdfEdgeIndex(index, count int) bool {
	return index < min(pdfEdgeLineWindow, count) || index >= max(0, count-pdfEdgeLineWindow)
}

func canonicalEdgeLine(value string) string {
	return strings.ToLower(normalizeLine(value))
}

func isPageNumberLine(value string) bool {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "-–—·•[]【】()（） ")
	return pageNumberPattern.MatchString(value)
}

func decorativeLine(value string) bool {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) == 0 || len(runes) > 12 {
		return false
	}
	for _, r := range runes {
		if !unicode.IsPunct(r) && !unicode.IsSymbol(r) {
			return false
		}
	}
	return true
}

// Some publishers append private metadata after the final %%EOF marker. Try the
// physical file first, then expose only a valid PDF prefix without rewriting it.
func openPDF(path string) (*os.File, *pdfreader.Reader, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	reader, originalErr := pdfreader.NewReader(file, info.Size())
	if originalErr == nil {
		return file, reader, nil
	}

	tailSize := min(info.Size(), maxPDFTrailingDataScanBytes)
	if tailSize <= 0 {
		file.Close()
		return nil, nil, originalErr
	}
	tail := make([]byte, tailSize)
	n, readErr := file.ReadAt(tail, info.Size()-tailSize)
	if readErr != nil && readErr != io.EOF {
		file.Close()
		return nil, nil, originalErr
	}
	tail = tail[:n]
	const marker = "%%EOF"
	for search := tail; len(search) > 0; {
		index := bytes.LastIndex(search, []byte(marker))
		if index < 0 {
			break
		}
		logicalSize := info.Size() - tailSize + int64(index+len(marker))
		if logicalSize < info.Size() {
			if reader, candidateErr := pdfreader.NewReader(file, logicalSize); candidateErr == nil {
				return file, reader, nil
			}
		}
		search = search[:index]
	}
	file.Close()
	return nil, nil, originalErr
}

package exporter

import (
	"bytes"
	_ "embed"
	"fmt"
	"math"
	"strings"
	"unicode"

	"github.com/go-pdf/fpdf"
)

//go:embed assets/DroidSansFallback.ttf
var cjkFont []byte

func RenderPDF(document Document) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetCatalogSort(true)
	pdf.SetCompression(true)
	pdf.SetCreationDate(document.CreatedAt.UTC())
	pdf.SetModificationDate(document.CreatedAt.UTC())
	pdf.SetCreator("SciAide "+document.Generator, true)
	pdf.SetProducer("SciAide deterministic research exporter", true)
	pdf.SetTitle(document.Title, true)
	pdf.SetMargins(22, 20, 22)
	pdf.SetAutoPageBreak(true, 18)
	pdf.AddUTF8FontFromBytes("SciAideCJK", "", cjkFont)
	if !pdf.Ok() {
		return nil, pdf.Error()
	}
	pdf.SetFooterFunc(func() {
		pdf.SetY(-12)
		pdf.SetFont("SciAideCJK", "", 8)
		pdf.SetTextColor(120, 124, 132)
		pdf.CellFormat(0, 5, fmt.Sprintf("SciAide · %s · 第 %d 页", shortSHA(document.SourceSHA256), pdf.PageNo()), "", 0, "C", false, 0, "")
	})
	pdf.AddPage()
	pdf.SetTextColor(32, 33, 36)
	pdf.SetFont("SciAideCJK", "", 21)
	pdf.MultiCell(0, 10, document.Title, "", "L", false)
	pdf.Ln(2)
	for _, block := range document.Blocks {
		renderPDFBlock(pdf, block)
		if !pdf.Ok() {
			return nil, pdf.Error()
		}
	}
	if len(document.References) > 0 {
		pdfHeading(pdf, "参考文献", 1)
		for _, reference := range document.References {
			pdf.SetFont("SciAideCJK", "", 9.5)
			pdf.SetTextColor(55, 58, 64)
			pdf.MultiCell(0, 5.2, reference.Entry, "", "L", false)
			pdf.Ln(1)
		}
	}
	pdf.Ln(4)
	pdf.SetDrawColor(210, 213, 219)
	x, y := pdf.GetXY()
	pdf.Line(x, y, 188, y)
	pdf.Ln(3)
	pdf.SetFont("SciAideCJK", "", 7.5)
	pdf.SetTextColor(112, 116, 124)
	pdf.MultiCell(0, 4, fmt.Sprintf("导出溯源：ArtifactVersion %s · SHA256 %s · %s", document.SourceVersion, document.SourceSHA256, document.Generator), "", "L", false)
	var output bytes.Buffer
	if err := pdf.Output(&output); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func renderPDFBlock(pdf *fpdf.Fpdf, block Block) {
	switch block.Kind {
	case BlockHeading:
		pdfHeading(pdf, block.Text, block.Level)
	case BlockParagraph:
		pdf.SetFont("SciAideCJK", "", 10.5)
		pdf.SetTextColor(48, 51, 57)
		pdf.MultiCell(0, 6.2, block.Text, "", "L", false)
		pdf.Ln(1.2)
	case BlockQuote:
		pdf.SetFont("SciAideCJK", "", 10)
		pdf.SetTextColor(73, 78, 87)
		pdf.SetFillColor(243, 244, 246)
		pdf.MultiCell(0, 6, block.Text, "", "L", true)
		pdf.Ln(1.5)
	case BlockCode:
		pdf.SetFont("SciAideCJK", "", 8.5)
		pdf.SetTextColor(42, 46, 53)
		pdf.SetFillColor(244, 245, 247)
		pdf.MultiCell(0, 5, block.Text, "", "L", true)
		pdf.Ln(1.5)
	case BlockListItem:
		marker := "•"
		if block.Ordered {
			marker = fmt.Sprintf("%d.", block.Number)
		}
		indent := float64(max(1, block.Level)-1) * 5
		left := 22 + indent
		pdf.SetX(left)
		pdf.SetFont("SciAideCJK", "", 10.5)
		pdf.SetTextColor(48, 51, 57)
		pdf.CellFormat(7, 6.2, marker, "", 0, "L", false, 0, "")
		pdf.MultiCell(166-indent, 6.2, block.Text, "", "L", false)
		pdf.Ln(.6)
	case BlockTable:
		renderPDFTable(pdf, block.Rows)
	case BlockRule:
		pdf.SetDrawColor(190, 194, 201)
		x, y := pdf.GetXY()
		pdf.Line(x, y+1, 188, y+1)
		pdf.Ln(4)
	}
}

func pdfHeading(pdf *fpdf.Fpdf, text string, level int) {
	level = min(3, max(1, level))
	sizes := map[int]float64{1: 15.5, 2: 13, 3: 11.5}
	space := map[int]float64{1: 5, 2: 4, 3: 3}
	pdf.Ln(space[level])
	pdf.SetFont("SciAideCJK", "", sizes[level])
	pdf.SetTextColor(45, 53, 65)
	pdf.MultiCell(0, sizes[level]*0.48, text, "", "L", false)
	pdf.Ln(1.5)
}

func renderPDFTable(pdf *fpdf.Fpdf, rows [][]string) {
	for index, segment := range tableColumnSegments(rows, maxRenderedTableColumns) {
		if index > 0 {
			pdf.Ln(2)
		}
		renderPDFTableSegment(pdf, segment)
	}
}

func renderPDFTableSegment(pdf *fpdf.Fpdf, rows [][]string) {
	columns := 1
	for _, row := range rows {
		columns = max(columns, len(row))
	}
	width := 166.0 / float64(columns)
	contentWidth := maxFloat(1, width-4)
	textWidth := maxFloat(1, contentWidth-2*pdf.GetCellMargin())
	const (
		lineHeight = 4.7
		rowPadding = 2.4
	)
	pdf.SetFont("SciAideCJK", "", 8.5)
	for rowIndex, row := range rows {
		wrapped := make([][]string, columns)
		maximumLines := 1
		for column := range columns {
			text := ""
			if column < len(row) {
				text = row[column]
			}
			wrapped[column] = splitPDFCellText(pdf, text, textWidth)
			if len(wrapped[column]) == 0 {
				wrapped[column] = []string{""}
			}
			maximumLines = max(maximumLines, len(wrapped[column]))
		}
		for lineOffset := 0; lineOffset < maximumLines; {
			_, pageHeight := pdf.GetPageSize()
			available := pageHeight - 18 - pdf.GetY()
			linesOnPage := int(math.Floor((available - rowPadding) / lineHeight))
			if linesOnPage < 1 {
				pdf.AddPage()
				continue
			}
			fragmentLines := min(maximumLines-lineOffset, linesOnPage)
			rowHeight := float64(fragmentLines)*lineHeight + rowPadding
			left, _, _, _ := pdf.GetMargins()
			pdf.SetX(left)
			x, y := pdf.GetXY()
			for column := range columns {
				end := min(len(wrapped[column]), lineOffset+fragmentLines)
				cellX := x + float64(column)*width
				if rowIndex == 0 {
					pdf.SetFillColor(236, 238, 242)
				} else {
					pdf.SetFillColor(252, 252, 253)
				}
				pdf.SetDrawColor(199, 202, 209)
				pdf.Rect(cellX, y, width, rowHeight, "DF")
				pdf.SetTextColor(48, 51, 57)
				for lineIndex := lineOffset; lineIndex < end; lineIndex++ {
					pdf.SetXY(cellX+2, y+1.2+float64(lineIndex-lineOffset)*lineHeight)
					pdf.CellFormat(contentWidth, lineHeight, wrapped[column][lineIndex], "", 0, "L", false, 0, "")
				}
			}
			pdf.SetXY(left, y+rowHeight)
			lineOffset += fragmentLines
			if lineOffset < maximumLines {
				pdf.AddPage()
			}
		}
	}
	pdf.Ln(2)
}

func splitPDFCellText(pdf *fpdf.Fpdf, text string, width float64) []string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	if text == "" {
		return []string{""}
	}
	lines := make([]string, 0, 1)
	current := make([]rune, 0)
	lastBreak := -1
	currentWidth := 0.0
	flush := func(value []rune) {
		lines = append(lines, strings.TrimRightFunc(string(value), unicode.IsSpace))
	}
	for _, character := range []rune(text) {
		if character == '\n' {
			flush(current)
			current = current[:0]
			lastBreak = -1
			currentWidth = 0
			continue
		}
		current = append(current, character)
		currentWidth += pdf.GetStringWidth(string(character))
		if unicode.IsSpace(character) {
			lastBreak = len(current) - 1
		}
		if currentWidth <= width {
			continue
		}
		cut := len(current) - 1
		if lastBreak >= 0 {
			cut = lastBreak
		}
		if cut == 0 {
			cut = 1
		}
		flush(current[:cut])
		current = append([]rune(nil), current[cut:]...)
		for len(current) > 0 && unicode.IsSpace(current[0]) {
			current = current[1:]
		}
		lastBreak = -1
		for index, value := range current {
			if unicode.IsSpace(value) {
				lastBreak = index
			}
		}
		currentWidth = pdf.GetStringWidth(string(current))
	}
	if len(current) > 0 {
		flush(current)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func shortSHA(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

func maxFloat(left, right float64) float64 {
	if left > right {
		return left
	}
	return right
}

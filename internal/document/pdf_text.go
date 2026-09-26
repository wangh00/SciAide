package document

import (
	"context"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	pdfreader "github.com/ledongthuc/pdf"
)

// Content preserves the PDF's drawing order, which commonly follows columns.
// Sorting all glyphs by Y would interleave independent columns. Recover spacing
// from adjacent glyph positions instead, without inventing missing text.
func positionedPDFText(ctx context.Context, page pdfreader.Page) (text string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			text, err = "", fmt.Errorf("extract positioned PDF text: %v", recovered)
		}
	}()
	if page.V.IsNull() || page.V.Key("Contents").Kind() == pdfreader.Null {
		return "", nil
	}
	glyphs := page.Content().Text
	// The upstream Content helper appends showText("\n") after each TJ array.
	// That byte is decoded through the active font and can become a spurious
	// Omega/circled symbol. Identify only these synthetic glyphs by stream index;
	// never strip actual scientific symbols by their Unicode value.
	synthetic := map[int]bool{}
	position := 0
	var encoding pdfreader.TextEncoding
	decodedLength := func(raw string) int {
		if encoding == nil {
			return utf8.RuneCountInString(raw)
		}
		return utf8.RuneCountInString(encoding.Decode(raw))
	}
	pdfreader.Interpret(page.V.Key("Contents"), func(stack *pdfreader.Stack, op string) {
		args := make([]pdfreader.Value, stack.Len())
		for n := len(args) - 1; n >= 0; n-- {
			args[n] = stack.Pop()
		}
		switch op {
		case "Tf":
			if len(args) == 2 {
				encoding = page.Font(args[0].Name()).Encoder()
			}
		case "Tj", "'", "\"":
			if len(args) > 0 {
				position += decodedLength(args[len(args)-1].RawString())
			}
		case "TJ":
			if len(args) != 1 {
				return
			}
			for n := 0; n < args[0].Len(); n++ {
				v := args[0].Index(n)
				if v.Kind() == pdfreader.String {
					position += decodedLength(v.RawString())
				}
			}
			for n := decodedLength("\n"); n > 0; n-- {
				synthetic[position] = true
				position++
			}
		}
	})
	if position != len(glyphs) {
		return "", fmt.Errorf("PDF glyph stream alignment mismatch")
	}
	kept := glyphs[:0]
	for n, g := range glyphs {
		if !synthetic[n] {
			kept = append(kept, g)
		}
	}
	return joinPDFGlyphs(ctx, kept)
}

var pdfLigatures = strings.NewReplacer("ﬀ", "ff", "ﬁ", "fi", "ﬂ", "fl", "ﬃ", "ffi", "ﬄ", "ffl", "ﬅ", "st", "ﬆ", "st")

func joinPDFGlyphs(ctx context.Context, glyphs []pdfreader.Text) (string, error) {
	var out strings.Builder
	var previous pdfreader.Text
	havePrevious := false
	var last rune
	for n, glyph := range glyphs {
		if n%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}
		if glyph.S == "" {
			continue
		}
		if math.IsNaN(glyph.X) || math.IsNaN(glyph.Y) || math.IsInf(glyph.X, 0) || math.IsInf(glyph.Y, 0) {
			return "", fmt.Errorf("invalid PDF glyph coordinates")
		}
		value := pdfLigatures.Replace(glyph.S)
		first, _ := utf8.DecodeRuneInString(value)
		if havePrevious {
			size := math.Max(1, math.Min(math.Abs(previous.FontSize), math.Abs(glyph.FontSize)))
			gap := glyph.X - (previous.X + previous.W)
			// Small baseline changes often represent superscripts/subscripts.
			newLine := math.Abs(glyph.Y-previous.Y) > size*.65 || glyph.X < previous.X-size*.5
			if newLine {
				out.WriteByte('\n')
			} else if gap > math.Max(.5, size*.16) && !unicode.IsSpace(last) && !unicode.IsSpace(first) {
				out.WriteByte(' ')
			}
		}
		out.WriteString(value)
		last, _ = utf8.DecodeLastRuneInString(value)
		previous, havePrevious = glyph, true
		// Bound accumulation before higher-level page/document limits are applied.
		if out.Len() > maxPDFAnalysisRunes*utf8.UTFMax {
			return "", fmt.Errorf("PDF page text exceeds extraction limit")
		}
	}
	return out.String(), nil
}

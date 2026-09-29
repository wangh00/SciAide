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
	var unicodeMap map[byte]string
	var mappingErr error
	mappings := map[string]map[byte]string{}
	decodedLength := func(raw string) int {
		if encoding == nil {
			return utf8.RuneCountInString(raw)
		}
		return utf8.RuneCountInString(encoding.Decode(raw))
	}
	advance := func(raw string) {
		length := decodedLength(raw)
		if unicodeMap != nil {
			if length != len(raw) || position+length > len(glyphs) {
				mappingErr = fmt.Errorf("simple-font PDF glyph alignment mismatch")
				return
			}
			for i := 0; i < len(raw); i++ {
				if decoded, ok := unicodeMap[raw[i]]; ok {
					glyphs[position+i].S = decoded
				}
			}
		}
		position += length
	}
	pdfreader.Interpret(page.V.Key("Contents"), func(stack *pdfreader.Stack, op string) {
		args := make([]pdfreader.Value, stack.Len())
		for n := len(args) - 1; n >= 0; n-- {
			args[n] = stack.Pop()
		}
		switch op {
		case "Tf":
			if len(args) == 2 {
				name := args[0].Name()
				font := page.Font(name)
				encoding = font.Encoder()
				var ok bool
				unicodeMap, ok = mappings[name]
				if !ok && mappingErr == nil {
					unicodeMap, mappingErr = simplePDFUnicode(font)
					mappings[name] = unicodeMap
				}
			}
		case "Tj", "'", "\"":
			if len(args) > 0 {
				advance(args[len(args)-1].RawString())
			}
		case "TJ":
			if len(args) != 1 {
				return
			}
			for n := 0; n < args[0].Len(); n++ {
				v := args[0].Index(n)
				if v.Kind() == pdfreader.String {
					advance(v.RawString())
				}
			}
			for n := decodedLength("\n"); n > 0; n-- {
				synthetic[position] = true
				position++
			}
		}
	})
	if mappingErr != nil {
		return "", mappingErr
	}
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
		var visible strings.Builder
		for _, r := range value {
			if r == '\ufffd' || (unicode.IsControl(r) && !unicode.IsSpace(r)) || unicode.Is(unicode.Co, r) {
				fmt.Fprintf(&visible, "[无法解码字形:%U]", r)
			} else {
				visible.WriteRune(r)
			}
		}
		value = visible.String()
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

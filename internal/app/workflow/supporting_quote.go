package workflow

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

// quoteTypography retains original byte spans, including expanded PDF ligatures.
// Only line-wrapped word hyphens are optional; ordinary word hyphens, numbers,
// minus signs, ranges, case and sentence punctuation remain significant.
func quoteTypography(s string) (string, []int, []int, []bool) {
	runes := []rune(s)
	offsets := make([]int, 0, len(runes)+1)
	for offset := range s {
		offsets = append(offsets, offset)
	}
	offsets = append(offsets, len(s))
	starts, ends := []int{}, []int{}
	optional := []bool{}
	var normalized strings.Builder
	space := false
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		start, end := offsets[i], offsets[i+1]
		wrap := quoteWordWrapEnd(runes, i)
		encoded := ""
		if unicode.IsSpace(r) {
			if space {
				ends[len(ends)-1] = end
				continue
			}
			r = ' '
			space = true
		} else {
			space = false
			if wrap > i {
				r = '-'
				end = offsets[wrap]
				i = wrap - 1 // The newline is typography, not a sentence/paragraph gap.
			} else if (r == '\u2010' || r == '\u2011' || r == '\u2019') && i > 0 && i+1 < len(runes) && unicode.IsLetter(runes[i-1]) && unicode.IsLetter(runes[i+1]) {
				if r == '\u2019' {
					r = '\'' // Word-internal typographic apostrophe only.
				} else {
					r = '-'
				}
			}
			switch r {
			case '\ufb00':
				encoded = "ff"
			case '\ufb01':
				encoded = "fi"
			case '\ufb02':
				encoded = "fl"
			case '\ufb03':
				encoded = "ffi"
			case '\ufb04':
				encoded = "ffl"
			case '\ufb05', '\ufb06':
				encoded = "st"
			}
		}
		if encoded == "" {
			encoded = string(r)
		}
		normalized.WriteString(encoded)
		for range []byte(encoded) {
			starts = append(starts, start)
			ends = append(ends, end)
			optional = append(optional, wrap > 0)
		}
	}
	return normalized.String(), starts, ends, optional
}

// A single physical line break between letters can split either a word
// (experi-\nments) or an existing compound (end-\nto-end). Preserve both forms
// during matching, without deleting ordinary hyphens or crossing paragraphs.
func quoteWordWrapEnd(runes []rune, i int) int {
	if (runes[i] != '-' && runes[i] != '\u2010') || i == 0 || !unicode.IsLetter(runes[i-1]) {
		return 0
	}
	horizontal := func(r rune) bool { return r == ' ' || r == '\t' }
	j := i + 1
	for j < len(runes) && horizontal(runes[j]) {
		j++
	}
	if j == len(runes) || (runes[j] != '\r' && runes[j] != '\n') {
		return 0
	}
	if runes[j] == '\r' && j+1 < len(runes) && runes[j+1] == '\n' {
		j++
	}
	j++
	for j < len(runes) && horizontal(runes[j]) {
		j++
	}
	if j < len(runes) && unicode.IsLetter(runes[j]) {
		return j
	}
	return 0
}

func typographyQuoteSpan(source, wanted string, starts, ends []int, optional []bool) (int, int, bool) {
	first, last := -1, -1
	for search := 0; search < len(source); {
		at := strings.IndexByte(source[search:], wanted[0])
		if at < 0 {
			break
		}
		at += search
		search = at + 1
		if at > 0 && starts[at] == starts[at-1] {
			continue // Never start inside an expanded ligature or UTF-8 rune.
		}
		end, w := at, 0
		for end < len(source) && w < len(wanted) {
			if source[end] == wanted[w] {
				end++
				w++
			} else if optional[end] {
				end++
			} else {
				break
			}
		}
		if w != len(wanted) || (end < len(source) && ends[end-1] == ends[end]) {
			continue // Nor end inside a ligature/rune.
		}
		if first >= 0 {
			return 0, 0, false // A non-exact repair needs a unique source location.
		}
		first, last = starts[at], ends[end-1]
	}
	return first, last, first >= 0
}

func exactSupportingQuote(source, quote string) (string, bool) {
	quote = strings.TrimSpace(quote)
	if quote == "" {
		return "", false
	}
	if strings.Contains(source, quote) {
		return quote, true
	}
	normalized, starts, ends, optional := quoteTypography(source)
	wanted, _, _, _ := quoteTypography(quote)
	start, end, ok := typographyQuoteSpan(normalized, wanted, starts, ends, optional)
	if !ok {
		return "", false
	}
	return source[start:end], true
}

// Canonicalize only this submission's supportingQuote fields against their
// exact bound candidate; immutable source snapshots, hashes and offsets stay put.
func canonicalSupportingQuotes(output, input json.RawMessage) json.RawMessage {
	var in struct {
		Candidates []struct {
			Reference string `json:"reference"`
			Quote     string `json:"quote"`
		} `json:"candidates"`
	}
	var out map[string]json.RawMessage
	if json.Unmarshal(input, &in) != nil || json.Unmarshal(output, &out) != nil {
		return output
	}
	var assessments []map[string]json.RawMessage
	if json.Unmarshal(out["citationAssessments"], &assessments) != nil {
		return output
	}
	sources := map[string]string{}
	for _, c := range in.Candidates {
		if _, exists := sources[c.Reference]; exists || c.Reference == "" {
			return output // An ambiguous binding must not be repaired.
		}
		sources[c.Reference] = c.Quote
	}
	changed := false
	for _, a := range assessments {
		var ref, quote, decision string
		json.Unmarshal(a["reference"], &ref)
		json.Unmarshal(a["supportingQuote"], &quote)
		json.Unmarshal(a["decision"], &decision)
		if decision == "exclude" {
			continue
		}
		if exact, ok := exactSupportingQuote(sources[ref], quote); ok && exact != quote && utf8.RuneCountInString(exact) <= 700 {
			a["supportingQuote"], _ = json.Marshal(exact)
			changed = true
		}
	}
	if !changed {
		return output
	}
	out["citationAssessments"], _ = json.Marshal(assessments)
	result, err := json.Marshal(out)
	if err != nil {
		return output
	}
	return result
}

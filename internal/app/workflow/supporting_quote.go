package workflow

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

// quoteTypography retains source byte offsets. Only whitespace and hyphens
// inside words are equivalent: numeric minus/ranges and punctuation are not.
func quoteTypography(s string) (string, []int, []int) {
	runes := []rune(s)
	starts, ends := []int{}, []int{}
	var normalized strings.Builder
	offset := 0
	space := false
	for i, r := range runes {
		start := offset
		offset += len(string(r))
		if unicode.IsSpace(r) {
			if space {
				ends[len(ends)-1] = offset
				continue
			}
			r = ' '
			space = true
		} else {
			space = false
			if (r == '\u2010' || r == '\u2011') && i > 0 && i+1 < len(runes) && unicode.IsLetter(runes[i-1]) && unicode.IsLetter(runes[i+1]) {
				r = '-'
			}
		}
		encoded := string(r)
		normalized.WriteString(encoded)
		for range []byte(encoded) {
			starts = append(starts, start)
			ends = append(ends, offset)
		}
	}
	return normalized.String(), starts, ends
}

func exactSupportingQuote(source, quote string) (string, bool) {
	quote = strings.TrimSpace(quote)
	if quote == "" {
		return "", false
	}
	if strings.Contains(source, quote) {
		return quote, true
	}
	normalized, starts, ends := quoteTypography(source)
	wanted, _, _ := quoteTypography(quote)
	at := strings.Index(normalized, wanted)
	if at < 0 || strings.Index(normalized[at+1:], wanted) >= 0 {
		return "", false
	}
	return source[starts[at]:ends[at+len(wanted)-1]], true
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

package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Only word-internal hyphen glyphs are equivalent. Mathematical minus signs,
// ranges, punctuation, whitespace, letter case and scientific text stay exact.
func literatureQuoteHyphens(text string) ([]rune, []int) {
	runes := []rune(text)
	offsets := make([]int, 0, len(runes)+1)
	for offset := range text {
		offsets = append(offsets, offset)
	}
	offsets = append(offsets, len(text))
	for i, r := range runes {
		if (r == '\u2010' || r == '\u2011') && i > 0 && i+1 < len(runes) && unicode.IsLetter(runes[i-1]) && unicode.IsLetter(runes[i+1]) {
			runes[i] = '-'
		}
	}
	return runes, offsets
}

func literatureQuoteSourceSpan(source, quote string) (string, bool) {
	if quote == "" || source == "" {
		return "", false
	}
	if strings.Contains(source, quote) {
		return quote, true
	}
	sourceRunes, offsets := literatureQuoteHyphens(source)
	quoteRunes, _ := literatureQuoteHyphens(quote)
	canonicalSource, canonicalQuote := string(sourceRunes), string(quoteRunes)
	start := strings.Index(canonicalSource, canonicalQuote)
	if start < 0 {
		return "", false
	}
	// Without an exact match, require a unique source location rather than
	// selecting between typography variants or repeated passages arbitrarily.
	runeStart := utf8.RuneCountInString(canonicalSource[:start])
	next := start + utf8.RuneLen(sourceRunes[runeStart])
	if strings.Contains(canonicalSource[next:], canonicalQuote) {
		return "", false
	}
	return source[offsets[runeStart]:offsets[runeStart+len(quoteRunes)]], true
}

func literatureQuotePath(note, quote int) string {
	return fmt.Sprintf("$.evidenceNotes[%d].quotes[%d].quote", note, quote)
}

func normalizeLiteratureQuotes(output, input json.RawMessage) (json.RawMessage, []AIStageNormalization, error) {
	var in struct {
		Literature literatureInput       `json:"_literature"`
		Candidates []literatureCandidate `json:"candidates"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return output, nil, err
	}
	if in.Literature.Phase != "batch" {
		return output, nil, nil
	}
	byID := make(map[string]literatureCandidate, len(in.Candidates))
	for _, candidate := range in.Candidates {
		if _, duplicate := byID[candidate.ID]; duplicate {
			return output, nil, nil
		}
		byID[candidate.ID] = candidate
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(output, &object); err != nil {
		return output, nil, err
	}
	var phase string
	_ = json.Unmarshal(object["phase"], &phase)
	if phase != "batch" {
		return output, nil, nil
	}
	var notes []map[string]json.RawMessage
	if err := json.Unmarshal(object["evidenceNotes"], &notes); err != nil {
		return output, nil, err
	}
	changes := []AIStageNormalization{}
	for i, note := range notes {
		var candidateID string
		if json.Unmarshal(note["candidateId"], &candidateID) != nil {
			continue
		}
		candidate, ok := byID[candidateID]
		if !ok {
			continue
		}
		var quotes []map[string]json.RawMessage
		if err := json.Unmarshal(note["quotes"], &quotes); err != nil {
			return output, nil, err
		}
		changed := false
		for j, q := range quotes {
			var field, quote, segmentID string
			if json.Unmarshal(q["field"], &field) != nil {
				continue
			}
			if len(q["quote"]) > 0 && json.Unmarshal(q["quote"], &quote) != nil {
				continue
			}
			if len(q["segmentId"]) > 0 {
				if json.Unmarshal(q["segmentId"], &segmentID) != nil {
					return output, nil, fmt.Errorf("invalid source segment ID")
				}
				original, found := literatureSegmentQuote(candidate, literatureQuote{Field: field, SegmentID: segmentID})
				if !found {
					return output, nil, fmt.Errorf("candidate %s has no %s source segment %s", candidateID, field, segmentID)
				}
				if quote != "" && quote != original {
					return output, nil, fmt.Errorf("candidate %s source segment %s conflicts with submitted quotation", candidateID, segmentID)
				}
				if quote == "" {
					replacement, _ := json.Marshal(original)
					changes = append(changes, AIStageNormalization{Path: literatureQuotePath(i, j), Rule: "literature_quote_source_segment", BeforeSHA256: hashJSON(q["segmentId"]), AfterSHA256: hashJSON(replacement)})
					q["quote"] = replacement
					changed = true
				}
				continue
			}
			source := ""
			switch field {
			case "title":
				source = candidate.Title
			case "abstract":
				source = candidate.Abstract
			}
			exact, matched := literatureQuoteSourceSpan(source, quote)
			if !matched || exact == quote {
				continue
			}
			replacement, err := json.Marshal(exact)
			if err != nil {
				return output, nil, err
			}
			before, _ := json.Marshal(quote)
			changes = append(changes, AIStageNormalization{Path: literatureQuotePath(i, j), Rule: "literature_quote_source_hyphen", BeforeSHA256: hashJSON(before), AfterSHA256: hashJSON(replacement)})
			q["quote"] = replacement
			changed = true
		}
		if changed {
			encoded, err := json.Marshal(quotes)
			if err != nil {
				return output, nil, err
			}
			note["quotes"] = encoded
		}
	}
	if len(changes) == 0 {
		return output, nil, nil
	}
	encoded, err := json.Marshal(notes)
	if err != nil {
		return output, nil, err
	}
	object["evidenceNotes"] = encoded
	normalized, err := json.Marshal(object)
	return normalized, changes, err
}

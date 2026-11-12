package workflow

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"unicode"
)

// Preserve all history and current values, but do not lead the model into the
// old report before it sees the current one. No persisted input/hash changes.
func reviewCurrentInputFirst(input json.RawMessage) json.RawMessage {
	var root map[string]json.RawMessage
	if json.Unmarshal(input, &root) != nil || len(root) == 0 {
		return input
	}
	historyKeys := []string{"_reviewCorrections", "reviewHistory"}
	names := []string{}
	priority := []string{"context", "reportRecordAppendices", "evidenceContext", "evidenceScreening", "researchContract", "acceptanceCriteria", "revisionTargets", "revisionTargetCapabilities"}
	first := map[string]bool{}
	for _, key := range priority {
		first[key] = true
	}
	for key := range root {
		if key != historyKeys[0] && key != historyKeys[1] && !first[key] {
			names = append(names, key)
		}
	}
	sort.Strings(names)
	ordered := []string{}
	for _, key := range priority {
		if _, ok := root[key]; ok {
			ordered = append(ordered, key)
		}
	}
	names = append(ordered, names...)
	for _, key := range historyKeys {
		if _, ok := root[key]; ok {
			names = append(names, key)
		}
	}
	var out bytes.Buffer
	out.WriteByte('{')
	for i, key := range names {
		if i > 0 {
			out.WriteByte(',')
		}
		encoded, _ := json.Marshal(key)
		out.Write(encoded)
		out.WriteByte(':')
		out.Write(root[key])
	}
	out.WriteByte('}')
	return out.Bytes()
}

// These are correction-only views, not additional frozen scientific inputs.
// IDs bind the input version, scalar path and entire source. Offsets are rune
// offsets in that source; skipped menu entries must NEVER be concatenated.
type ReviewBasisSource struct {
	Cell        string               `json:"cell"`
	Pointer     string               `json:"pointer"`
	InputSHA256 string               `json:"inputSHA256"`
	Segments    []ReviewBasisSegment `json:"segments"`
}

type ReviewBasisSegment struct {
	ID    string `json:"id"`
	Start int    `json:"start"`
	End   int    `json:"end"`
	Text  string `json:"text"`
}

func reviewBasisSource(input json.RawMessage, cell, pointer, text, rejected string, budget *int) *ReviewBasisSource {
	if text == "" || *budget < 400 {
		return nil
	}
	source := &ReviewBasisSource{Cell: cell, Pointer: pointer, InputSHA256: hashJSON(input)}
	segments := CitationQuoteSourceSegments(source.InputSHA256+"\x00"+pointer, text)
	// Similarity is used ONLY to navigate within the already designated field.
	// It never accepts/replaces a quote, searches history or selects evidence.
	query := reviewNavigationGrams(rejected)
	type ranked struct{ index, score int }
	ranks := []ranked{}
	for i, segment := range segments {
		score := 0
		for gram := range reviewNavigationGrams(segment.Text) {
			if query[gram] {
				score++
			}
		}
		if score > 0 {
			ranks = append(ranks, ranked{i, score})
		}
	}
	sort.SliceStable(ranks, func(i, j int) bool { return ranks[i].score > ranks[j].score })
	selected := map[int]bool{}
	if len([]rune(text)) <= 2400 {
		for i := range segments {
			selected[i] = true
		}
	} else {
		for _, rank := range ranks[:min(2, len(ranks))] {
			for i := max(0, rank.index-1); i <= min(len(segments)-1, rank.index+1); i++ {
				selected[i] = true
			}
		}
	}
	position := 0
	for i, segment := range segments {
		size := len([]rune(segment.Text))
		if selected[i] && size <= *budget {
			source.Segments = append(source.Segments, ReviewBasisSegment{segment.ID, position, position + size, segment.Text})
			*budget -= size
		}
		position += size
	}
	if len(source.Segments) == 0 {
		return nil
	}
	return source
}

func reviewNavigationGrams(text string) map[string]bool {
	characters := []rune(strings.ToLower(text))
	grams := map[string]bool{}
	for i := 1; i < len(characters); i++ {
		if unicode.IsLetter(characters[i-1]) && unicode.IsLetter(characters[i]) || unicode.IsDigit(characters[i-1]) && unicode.IsDigit(characters[i]) {
			grams[string(characters[i-1:i+1])] = true
		}
	}
	return grams
}

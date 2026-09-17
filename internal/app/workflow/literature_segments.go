package workflow

import (
	"encoding/json"
	"fmt"
	"unicode"
)

type literatureSegment struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Segments cover every source character in order. Prefer sentence boundaries,
// otherwise whitespace; never summarize, drop a tail, or change punctuation.
func literatureSourceSegments(text, field string) []literatureSegment {
	prefix := "a"
	if field == "title" {
		prefix = "t"
	}
	characters := []rune(text)
	segments := []literatureSegment{}
	for start := 0; start < len(characters); {
		end := min(start+400, len(characters))
		if end < len(characters) {
			boundary := 0
			for i := start + 1; i < end; i++ {
				if unicode.IsSpace(characters[i]) && (characters[i-1] == '.' || characters[i-1] == ';' || characters[i-1] == '?' || characters[i-1] == '!') {
					boundary = i + 1
				}
			}
			if boundary == 0 {
				for i := end - 1; i > start; i-- {
					if unicode.IsSpace(characters[i]) {
						boundary = i + 1
						break
					}
				}
			}
			if boundary > start {
				end = boundary
			}
		}
		segments = append(segments, literatureSegment{ID: fmt.Sprintf("%s%04d", prefix, len(segments)+1), Text: string(characters[start:end])})
		start = end
	}
	return segments
}

func literatureSegmentQuote(candidate literatureCandidate, quote literatureQuote) (string, bool) {
	source := ""
	switch quote.Field {
	case "title":
		source = candidate.Title
	case "abstract":
		source = candidate.Abstract
	default:
		return "", false
	}
	for _, segment := range literatureSourceSegments(source, quote.Field) {
		if segment.ID == quote.SegmentID {
			return segment.Text, true
		}
	}
	return "", false
}

func projectLiteratureCandidateSegments(rawCandidates json.RawMessage) json.RawMessage {
	var candidates []map[string]json.RawMessage
	if json.Unmarshal(rawCandidates, &candidates) != nil {
		return rawCandidates
	}
	for _, candidate := range candidates {
		for _, field := range []string{"title", "abstract"} {
			var source string
			if json.Unmarshal(candidate[field], &source) != nil {
				continue
			}
			candidate[field+"Segments"] = mustJSON(literatureSourceSegments(source, field))
			// The title remains available for scanning; long abstracts are
			// represented once, fully and exactly, in the segment inventory.
			if field == "abstract" {
				delete(candidate, field)
			}
		}
	}
	return mustJSON(candidates)
}

// Package reportview restores the exact report represented by a separated
// review input. Workflow dependency checks and publication share this codec;
// it neither edits scientific content nor decides whether a review passed.
package reportview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const PartsVersion = "report-review-parts-v1"

type ManagedSection struct {
	Heading string `json:"heading"`
	Start   int    `json:"startRune"`
	End     int    `json:"endRune"`
	SHA256  string `json:"sha256"`
}

type Composition struct {
	ViewVersion  string           `json:"viewVersion,omitempty"`
	NodeID       string           `json:"nodeId"`
	Attempt      int              `json:"attempt"`
	OutputSHA256 string           `json:"outputSha256"`
	Pointer      string           `json:"pointer"`
	Sections     []ManagedSection `json:"hostManagedSections"`
}

// AssembleInput preserves every character, including separators and trailing
// whitespace. Never infer appendices from headings or drop unrecognized parts.
// Non-separated inputs pass through unchanged. Callers must still verify the
// frozen review identity and compare the reconstructed report to its producer.
func AssembleInput(input json.RawMessage) (json.RawMessage, error) {
	var stage struct {
		Composition Composition `json:"reportComposition"`
		Records     []string    `json:"reportRecordAppendices"`
	}
	if err := json.Unmarshal(input, &stage); err != nil {
		return nil, fmt.Errorf("invalid report review input: %w", err)
	}
	if stage.Composition.ViewVersion == "" {
		return input, nil
	}
	if stage.Composition.ViewVersion != PartsVersion || stage.Composition.Pointer != "/context/markdown" || len(stage.Records) == 0 || len(stage.Records) != len(stage.Composition.Sections) {
		return nil, fmt.Errorf("invalid report review parts version, pointer or section count")
	}
	var fields, context map[string]json.RawMessage
	_ = json.Unmarshal(input, &fields)
	if err := json.Unmarshal(fields["context"], &context); err != nil {
		return nil, fmt.Errorf("invalid report review context: %w", err)
	}
	var body *string
	if err := json.Unmarshal(context["markdown"], &body); err != nil || body == nil {
		return nil, fmt.Errorf("report review body is not a string")
	}
	var full strings.Builder
	full.WriteString(*body)
	position := len([]rune(*body))
	for i, record := range stage.Records {
		section := stage.Composition.Sections[i]
		runes := []rune(record)
		// Compare before subtracting so malformed offsets cannot overflow.
		if section.Start != position || section.End <= section.Start || section.End-section.Start > len(runes) {
			return nil, fmt.Errorf("report review appendix %d has invalid offsets", i)
		}
		n := section.End - section.Start
		digest := sha256.Sum256([]byte(string(runes[:n])))
		if hex.EncodeToString(digest[:]) != section.SHA256 {
			return nil, fmt.Errorf("report review appendix %d hash mismatch", i)
		}
		full.WriteString(record)
		position += len(runes)
	}
	context["markdown"], _ = json.Marshal(full.String())
	fields["context"], _ = json.Marshal(context)
	return json.Marshal(fields)
}

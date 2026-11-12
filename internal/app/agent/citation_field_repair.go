package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

// This is an explicit model edit to its own rejected wire candidate, not an
// automatic repair or an accepted-result checkpoint. Every merged candidate
// goes through source resolution and the existing complete host validator.
type citationFieldRepair struct {
	base       map[string]json.RawMessage
	raw        json.RawMessage
	hash       string
	schema     json.RawMessage
	diagnostic string // belongs only to this exact raw/hash candidate
}

type citationFieldRepairFormatError struct{ cause error }

func (e *citationFieldRepairFormatError) Error() string {
	return "invalid result field correction: use only {baseSHA256,resultUpdates} from the CURRENT schema and base. Replace explicitly changed top-level fields; do not mix full-result fields into the correction envelope. " + e.cause.Error()
}
func (e *citationFieldRepairFormatError) Unwrap() error { return e.cause }

func (s *citationSourceSubmission) hasFieldRepair() bool {
	return s != nil && s.fieldRepair != nil
}

func (s *citationSourceSubmission) rememberCandidate(text string) {
	if !strings.HasSuffix(s.contract.PromptVersion, "|"+workflow.CitationSourceProtocolVersion) {
		return // Frozen v1 executions do not acquire a new correction contract.
	}
	s.fieldRepair = nil
	// Reuse the original bounded parser, but not the full schema: a parseable
	// summary overrun must itself be repairable without retyping other fields.
	value, err := workflow.ParseCitationFieldRepairObject(text)
	if err != nil {
		return
	}
	var base map[string]json.RawMessage
	var wire struct{ Properties map[string]json.RawMessage }
	if json.Unmarshal(value, &base) != nil || base == nil || json.Unmarshal(s.schema, &wire) != nil {
		return
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(value))
	patch, err := json.Marshal(map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"baseSHA256", "resultUpdates"},
		"properties": map[string]any{
			"baseSHA256":    map[string]any{"type": "string", "const": hash},
			"resultUpdates": map[string]any{"type": "object", "additionalProperties": false, "minProperties": 1, "properties": wire.Properties},
		},
	})
	if err != nil {
		return
	}
	s.fieldRepair = &citationFieldRepair{base: base, raw: append(json.RawMessage(nil), value...), hash: hash, schema: patch}
}

func (r *citationFieldRepair) instruction() string {
	return fmt.Sprintf("Current rejected evidence-synthesis baseSHA256=%s. Prefer submit_stage_repair:{baseSHA256,resultUpdates:{...}} for explicitly changed top-level result fields; unmentioned fields retain their submitted VALUES, not an approval. Replace any changed array in full, and update all dependent references, judgments and claims together. citationAssessments uses the current quoteSelection wire schema, never supportingQuote. Do not rewrite summary or unrelated fields merely because a citation decision changed. The host does not decide, trim or correct any scientific value. Each merge is rechecked against ALL original schema/source/scientific constraints and uses the SAME remaining correction allowance. A complete replacement remains available through submit_stage_result.", r.hash)
}

func (r *citationFieldRepair) merge(text string) (string, error) {
	if _, err := workflow.ParseCitationFieldRepairObject(text); err != nil {
		return "", &citationFieldRepairFormatError{cause: err}
	}
	if err := (tool.JSONSchemaValidator{}).ValidateWithDiagnostics(r.schema, []byte(text)); err != nil {
		return "", &citationFieldRepairFormatError{cause: err}
	}
	var patch struct {
		BaseSHA256    string                     `json:"baseSHA256"`
		ResultUpdates map[string]json.RawMessage `json:"resultUpdates"`
	}
	if json.Unmarshal([]byte(text), &patch) != nil || patch.BaseSHA256 != r.hash || len(patch.ResultUpdates) == 0 {
		return "", &citationFieldRepairFormatError{cause: fmt.Errorf("field correction must bind the current rejected candidate")}
	}
	merged := make(map[string]json.RawMessage, len(r.base)+len(patch.ResultUpdates))
	for key, value := range r.base {
		merged[key] = value
	}
	for key, value := range patch.ResultUpdates {
		merged[key] = value
	}
	value, err := json.Marshal(merged)
	return string(value), err
}

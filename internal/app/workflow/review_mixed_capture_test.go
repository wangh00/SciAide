package workflow

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCapturedMixedCorrectionDiagnosesOriginalReview(t *testing.T) {
	dir := os.Getenv("SCIAIDE_MIXED_REVIEW_REPLAY")
	if dir == "" {
		t.Skip("no read-only incident capture")
	}
	read := func(name string) json.RawMessage {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	node := CompiledNode{ID: "independent_review", OutputSchema: read("schema.json")}
	input := read("input.json")
	value, _, err := normalizeWorkflowAIStageSubmissionForInput(string(read("turn-4.json")), node, input)
	if err != nil {
		t.Fatal(err)
	}
	err = validateWorkflowAIStageOutput(node, value, input)
	if err == nil {
		t.Fatal("original invalid basis unexpectedly accepted")
	}
	t.Logf("first candidate: %v", err)
	var diagnostic *ReviewBasisErrors
	if !errors.As(err, &diagnostic) {
		t.Fatal("unexpected first failure", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "basis-diagnostic.json"), mustJSON(diagnostic), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"turn-5.json", "turn-6.json"} {
		if _, _, err := normalizeWorkflowAIStageSubmissionForInput(string(read(name)), node, input); err == nil {
			t.Fatal("mixed repair accepted as full review")
		}
	}
	// Emit the exact normalized rejected base for the agent replay; no invented
	// correction or scientific decision and no access to the formal database.
	if err := os.WriteFile(filepath.Join(dir, "normalized-base.json"), value, 0600); err != nil {
		t.Fatal(err)
	}
}

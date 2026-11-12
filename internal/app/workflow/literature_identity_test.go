package workflow

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiteratureIdentityDiagnosticsEnumerateBothCollections(t *testing.T) {
	in := raw(`{"_literature":{"phase":"batch"},"candidates":[{"id":"a","title":"First source"},{"id":"b","title":"Second source"}]}`)
	out := raw(`{"candidateAssessments":[{"candidateId":"a"},{"candidateId":"typo"}],"evidenceNotes":[{"candidateId":"a"},{"candidateId":"typo"}]}`)
	var d *LiteratureIdentityErrors
	if err := validateLiteratureIdentities(out, in); !errors.As(err, &d) || !d.Local || len(d.Cells) != 2 || len(d.Problems) != 4 {
		t.Fatal("incomplete diagnostics", err)
	}
	if d.Cells[0].Path != "/candidateAssessments/1/candidateId" || d.Cells[1].Path != "/evidenceNotes/1/candidateId" || strings.Join(d.Cells[0].AllowedIDs, ",") != "b" {
		t.Fatal(d)
	}
	for _, bad := range []string{strings.ReplaceAll(string(out), "typo", "a"), `{"candidateAssessments":[{"candidateId":"a"}],"evidenceNotes":[]}`} {
		var d *LiteratureIdentityErrors
		if e := validateLiteratureIdentities(raw(bad), in); !errors.As(e, &d) || d.Local {
			t.Fatal("ambiguous identity offered a local patch", e)
		}
	}
	if e := validateLiteratureIdentities(raw(strings.ReplaceAll(string(out), "typo", "b")), in); e != nil {
		t.Fatal(e)
	}
}

func TestCapturedScreeningIdentityDiagnosticsAndExactSourceRepair(t *testing.T) {
	dir := os.Getenv("SCIAIDE_FLOW_CAPTURE")
	if dir == "" {
		t.Skip("optional read-only capture")
	}
	read := func(name string) []byte {
		b, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	var n CompiledNode
	if e := json.Unmarshal(read("node.json"), &n); e != nil {
		t.Fatal(e)
	}
	input := read("input.json")
	n = literaturePhaseNode(n, input)
	for _, turn := range []string{"1", "2"} {
		text := string(read("candidate-" + turn + ".txt"))
		_, _, e := normalizeWorkflowAIStageSubmissionForInput(text, n, input)
		var d *LiteratureIdentityErrors
		if !errors.As(e, &d) || len(d.Cells) != 2 || !d.Local {
			t.Fatal("missing typed diagnostics", e)
		}
		for _, cell := range d.Cells {
			if len(cell.AllowedIDs) != 1 || cell.AllowedIDs[0] != "75956eb1-ff8e-455d-809e-d30729fdd552" {
				t.Fatal("wrong navigation", cell)
			}
		}
		// Explicit fixture choice, NOT production automatic fuzzy matching.
		text = strings.ReplaceAll(text, d.Cells[0].Value, d.Cells[0].AllowedIDs[0])
		v, _, e := normalizeWorkflowAIStageSubmissionForInput(text, n, input)
		if e != nil {
			t.Fatal(e)
		}
		if e = validateWorkflowAIStageOutput(n, v, input); e != nil {
			t.Fatal("further defect after identity correction", e)
		}
		t.Log("original candidate " + turn + ": exactly two ID cells; explicit correction passes unchanged full validator")
	}
}

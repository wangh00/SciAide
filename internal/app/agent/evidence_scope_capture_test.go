package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

// Opt-in captured data remain outside source control; no database/provider.
func TestCapturedTestingEffectMixedScopeSourceSelection(t *testing.T) {
	dir := os.Getenv("SCIAIDE_SCOPE_REPAIR_REPLAY_DIR")
	if dir == "" {
		t.Skip("no task-scoped failure capture")
	}
	read := func(name string) []byte {
		b, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	base, schema := read("submission-5.json"), json.RawMessage(read("schema.json"))
	var issues workflow.CitationQuoteErrors
	if err := json.Unmarshal(read("quote-issues.json"), &issues); err != nil {
		t.Fatal(err)
	}
	mixed := &workflow.EvidenceSubmissionErrors{Scope: errors.New(string(read("scope-error.txt"))), Citations: &issues}
	r := newCitationRepairForError(string(base), schema, mixed)
	if r == nil || len(r.sources) != 2 || !r.scopeUpdates {
		t.Fatal("real second submission did not expose combined source menu")
	}
	selections := []any{}
	for _, source := range r.sources {
		ids := []string{}
		selecting := false
		for _, segment := range source.Segments {
			if strings.Contains(segment.Text, "Contrary to prior work") || strings.Contains(segment.Text, "Results support the hypothesis") {
				selecting = true
			}
			if selecting {
				ids = append(ids, segment.ID)
			}
			if selecting && (strings.Contains(segment.Text, "but this benefit did not carry over a delay") || strings.Contains(segment.Text, "independent of the degree of prior learning")) {
				break
			}
		}
		if len(ids) == 0 {
			t.Fatal("capture lacks chosen continuous results passage")
		}
		selections = append(selections, map[string]any{"reference": source.Reference, "sourceSegmentIds": ids})
	}
	var scope map[string]json.RawMessage
	if json.Unmarshal(read("scope-updates.json"), &scope) != nil {
		t.Fatal("invalid scope fixture")
	}
	patch, _ := json.Marshal(map[string]any{"baseSHA256": r.hash, "citationQuoteSelections": selections, "evidenceScopeUpdates": scope})
	merged, err := r.merge(string(patch))
	if err != nil {
		t.Fatal(err)
	}
	if err := (tool.JSONSchemaValidator{}).Validate(schema, json.RawMessage(merged)); err != nil {
		t.Fatal(err)
	}
	var before, after map[string]json.RawMessage
	_ = json.Unmarshal(r.base, &before)
	_ = json.Unmarshal([]byte(merged), &after)
	for key, value := range before {
		if key != "evidenceGaps" && key != "citationAssessments" && !bytes.Equal(value, after[key]) {
			t.Fatal("unmentioned real field changed", key)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "merged-selected.json"), []byte(merged), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "selected-patch.json"), patch, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("Real second submission: both source quotations plus explicit gap corrections merged in one hash-bound patch; original full schema passes, all unmentioned fields preserved. No live task changed.")
}

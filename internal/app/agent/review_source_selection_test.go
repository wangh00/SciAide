package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

func reviewSelectionFixture() *reviewRepair {
	diagnostic := reviewRepairDiagnostic()
	r := newReviewRepair(reviewRepairTestBase, reviewRepairTestSchema, diagnostic)
	source := workflow.ReviewBasisSource{Cell: "/reviewFindings/0/basis/0/quote", Pointer: "/evidenceScreening/evidenceGaps/0", InputSHA256: strings.Repeat("a", 64), Segments: []workflow.ReviewBasisSegment{
		{ID: "seg-1", Start: 0, End: 9, Text: "Current. "}, {ID: "seg-2", Start: 9, End: 18, Text: "Not old. "}, {ID: "seg-4", Start: 27, End: 32, Text: "Tail."},
	}}
	r = newReviewRepair(reviewRepairTestBase, reviewRepairTestSchema, &workflow.ReviewBasisErrors{Problems: []string{"mismatch"}, RepairPaths: r.paths, Sources: []workflow.ReviewBasisSource{source}})
	return r
}

func TestReviewSourceSelectionCopiesExactTextWithoutChangingDecision(t *testing.T) {
	r := reviewSelectionFixture()
	if err := (tool.JSONSchemaValidator{}).ValidateSchema(r.schema); err != nil {
		t.Fatal(err)
	}
	patch, _ := json.Marshal(map[string]any{"baseSHA256": r.hash, "reviewBasisSelections": []any{map[string]any{"path": r.sources[0].Cell, "sourceSegmentIds": []string{"seg-1", "seg-2"}}}})
	merged, err := r.merge(string(patch))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(merged, `"quote":"Current. Not old. "`) || !strings.Contains(merged, `"approved":false`) || !strings.Contains(merged, "9007199254740993") {
		t.Fatal("changed source bytes or verdict", merged)
	}
	if !strings.Contains(r.instruction(), "<review_basis_sources>") || !strings.Contains(r.instruction(), "previous opinions") {
		t.Fatal("no source isolation guidance")
	}
}

func TestReviewSourceSelectionRejectsMixedStaleCrossCellAndGappedSpans(t *testing.T) {
	r := reviewSelectionFixture()
	cell := r.sources[0].Cell
	for _, items := range [][]any{
		{map[string]any{"path": cell, "sourceSegmentIds": []string{"seg-1"}, "value": "invented"}},
		{map[string]any{"path": cell}},
		{map[string]any{"path": cell, "sourceSegmentIds": []string{"old-version"}}},
		{map[string]any{"path": cell, "sourceSegmentIds": []string{"seg-1", "seg-1"}}},
		{map[string]any{"path": cell, "sourceSegmentIds": []string{"seg-2", "seg-1"}}},
		{map[string]any{"path": cell, "sourceSegmentIds": []string{"seg-2", "seg-4"}}},
		{map[string]any{"path": "/reviewFindings/0/changeReason", "sourceSegmentIds": []string{"seg-1"}}},
		{map[string]any{"path": cell, "sourceSegmentIds": []string{"seg-1"}}, map[string]any{"path": strings.TrimSuffix(cell, "quote") + "pointer", "value": "/context/markdown"}},
		{map[string]any{"path": strings.TrimSuffix(cell, "quote") + "pointer", "value": "/context/markdown"}, map[string]any{"path": cell, "sourceSegmentIds": []string{"seg-1"}}},
	} {
		patch, _ := json.Marshal(map[string]any{"baseSHA256": r.hash, "reviewBasisSelections": items})
		if _, err := r.merge(string(patch)); err == nil {
			t.Fatal("bad selection accepted", string(patch))
		}
	}
	for _, items := range [][]any{
		{map[string]any{"path": cell, "value": "duplicate quote"}},
		{map[string]any{"path": strings.TrimSuffix(cell, "quote") + "pointer", "value": "/context/markdown"}},
	} {
		patch, _ := json.Marshal(map[string]any{"baseSHA256": r.hash, "reviewFindingsPatch": items, "reviewBasisSelections": []any{map[string]any{"path": cell, "sourceSegmentIds": []string{"seg-1"}}}})
		if _, err := r.merge(string(patch)); err == nil {
			t.Fatal("duplicate cell or cross-source paired pointer accepted", string(patch))
		}
	}
	for _, fields := range []map[string]any{{"baseSHA256": r.hash}, {"baseSHA256": r.hash, "reviewFindingsPatch": []any{}, "reviewBasisSelections": []any{}}} {
		patch, _ := json.Marshal(fields)
		if _, err := r.merge(string(patch)); err == nil {
			t.Fatal("empty repair accepted")
		}
	}
}

func TestCapturedReviewSourceSelectionMerge(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REVIEW_LOCATION_OUTPUT")
	if dir == "" {
		t.Skip("no derived incident diagnostics supplied")
	}
	read := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var diagnostic workflow.ReviewBasisErrors
	if err := json.Unmarshal(read("basis-diagnostic.json"), &diagnostic); err != nil {
		t.Fatal(err)
	}
	r := newReviewRepair(string(read("base.json")), read("schema.json"), &diagnostic)
	if r == nil {
		t.Fatal("no repair for captured failure")
	}
	items := []any{}
	for _, source := range r.sources {
		ids := []string{}
		for _, segment := range source.Segments {
			if strings.Contains(segment.Text, "两项关键直接试验的实际能量摄入") {
				ids = append(ids, segment.ID)
				break
			}
		}
		if len(ids) == 0 {
			t.Fatal("current exact passage missing from menu")
		}
		items = append(items, map[string]any{"path": source.Cell, "sourceSegmentIds": ids})
	}
	patch, _ := json.Marshal(map[string]any{"baseSHA256": r.hash, "reviewBasisSelections": items})
	merged, err := r.merge(string(patch))
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]json.RawMessage
	_ = json.Unmarshal(r.base, &before)
	_ = json.Unmarshal([]byte(merged), &after)
	for key, value := range before {
		if key != "reviewFindings" && string(value) != string(after[key]) {
			t.Fatal("changed scientific field", key)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "selected-review.json"), []byte(merged), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("captured failure repaired with explicit test selection; original conclusion retained, not a real model test")
}

func TestReviewSourceSelectionRejectsOtherCellsAndOversizedSpan(t *testing.T) {
	r := reviewSelectionFixture()
	cell := r.sources[0].Cell
	other := r.sources[0]
	other.Cell = "/reviewFindings/0/basis/1/quote"
	other.Pointer = "/context/markdown"
	other.Segments = []workflow.ReviewBasisSegment{{ID: "other-source", Start: 0, End: 5, Text: "Other"}}
	r.sources = append(r.sources, other)
	if _, _, err := r.segmentQuote(cell, []string{"other-source"}); err == nil {
		t.Fatal("another offered source was accepted for this cell")
	}
	r.sources[0].Segments = []workflow.ReviewBasisSegment{{ID: "long", Start: 0, End: 2001, Text: strings.Repeat("长", 2001)}}
	if _, _, err := r.segmentQuote(cell, []string{"long"}); err == nil {
		t.Fatal("selection escaped the original quote size bound")
	}
}

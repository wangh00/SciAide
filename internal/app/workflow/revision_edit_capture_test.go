package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// Opt-in, read-only replay of an explicitly supplied private capture. The
// fixture never opens its DB, starts an app, or contacts a model.
func TestRevisionEditCapturedDesignAndReport(t *testing.T) {
	path := os.Getenv("SCIAIDE_REVISION_EDIT_CAPTURE")
	if path == "" {
		t.Skip("set an explicit private capture path for offline replay")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var capture struct{ Workflow RunDetail }
	if err := json.Unmarshal(content, &capture); err != nil {
		t.Fatal(err)
	}
	design := findStepByNode(capture.Workflow.Steps, "research_design")
	report := findStepByNode(capture.Workflow.Steps, "report_drafting")
	if design == nil || report == nil || design.Status != StepCompleted || report.Status != StepCompleted {
		t.Fatal("capture is not the completed rejected generation")
	}
	base, err := outputPort(design.Output, "analysis")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	var population []string
	_ = json.Unmarshal(base, &fields)
	_ = json.Unmarshal(fields["populationAndSampling"], &population)
	index := -1
	for i, text := range population {
		if strings.Contains(text, "腾写") {
			if index >= 0 {
				t.Fatal("captured target is ambiguous")
			}
			index = i
		}
	}
	if index < 0 || strings.Count(population[index], "腾写") != 1 {
		t.Fatal("expected actual observed typo missing")
	}
	patch := revisionEditPayload(base, map[string]any{"path": fmt.Sprintf("/populationAndSampling/%d", index), "before": "腾写", "after": "誊写"})
	merged, err := ApplyRevisionTextEdits(base, patch)
	population[index] = strings.Replace(population[index], "腾写", "誊写", 1)
	fields["populationAndSampling"] = mustJSON(population)
	expected := mustJSON(fields)
	if err != nil || !equalRevisionEditBase(merged, expected) {
		t.Fatal("exact observed repair changed other design fields", err)
	}
	reportBase, err := outputPort(report.Output, "analysis")
	if err != nil {
		t.Fatal(err)
	}
	authored, err := revisionEditReportBase(reportBase, report.Input)
	if err != nil {
		t.Fatal(err)
	}
	reused, err := ApplyRevisionTextEdits(authored, revisionEditPayload(authored))
	if err != nil {
		t.Fatal(err)
	}
	reassembled, _, err := enrichReportProvenance(reused, report.Input)
	if err != nil || !equalRevisionEditBase(reassembled, reportBase) {
		t.Fatal("explicit reuse changed the current original author body/metadata or records", err)
	}
	var input, revision map[string]json.RawMessage
	_ = json.Unmarshal(design.Input, &input)
	beforeValue, _ := decodeIsolationValue(design.Input)
	beforeInput := len(mustJSON(beforeValue))
	if json.Unmarshal(input["_userRevision"], &revision) == nil {
		if len(revision["priorOutput"]) > 0 {
			revision["priorOutput"] = reportRevisionPriorOutput(revision["priorOutput"])
		}
		if len(revision["comparisonBaseline"]) > 0 {
			revision["comparisonBaseline"] = revisionComparisonProjection(revision["comparisonBaseline"])
		}
		input["_userRevision"] = mustJSON(revision)
	}
	afterValue, _ := decodeIsolationValue(mustJSON(input))
	baseValue, _ := decodeIsolationValue(base)
	t.Logf("offline observed-design replay (same decoded UTF-8 JSON encoding, not actual provider requests): complete candidate=%d bytes; explicit one-character edit=%d bytes; design input projection=%d -> %d bytes; current report author metadata exactly retained", len(mustJSON(baseValue)), len(patch), beforeInput, len(mustJSON(afterValue)))
}

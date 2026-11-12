package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

// Scoped offline capture only; no formal DB, tools or model requests.
func TestCapturedGateRecoveryPreservesReviewAndRestoresPlan(t *testing.T) {
	dir := os.Getenv("SCIAIDE_GATE_CAPTURE")
	if dir == "" {
		t.Skip("optional task-scoped capture")
	}
	b, e := os.ReadFile(filepath.Join(dir, "detail.json"))
	if e != nil {
		t.Fatal(e)
	}
	var d RunDetail
	if e = json.Unmarshal(b, &d); e != nil {
		t.Fatal(e)
	}
	// Restore exact persisted JSON bytes; generic column-name conversion is not
	// valid for hash-bound snapshots because JSON reserialization changes hashes.
	readRows := func(name string) []map[string]json.RawMessage {
		b, e := os.ReadFile(filepath.Join(dir, name+".json"))
		if e != nil {
			t.Fatal(e)
		}
		var rows []map[string]json.RawMessage
		if json.Unmarshal(b, &rows) != nil {
			t.Fatal("bad rows")
		}
		return rows
	}
	str := func(v json.RawMessage) string { var s string; _ = json.Unmarshal(v, &s); return s }
	rr := readRows("workflow-run")[0]
	d.Run.Inputs = raw(str(rr["inputs_json"]))
	d.Run.Outputs = raw(str(rr["outputs_json"]))
	for j, row := range readRows("steps") {
		d.Steps[j].Input = raw(str(row["input_json"]))
		d.Steps[j].Output = raw(str(row["output_json"]))
	}
	for j, row := range readRows("executions") {
		d.AIExecutions[j].Output = raw(str(row["output_json"]))
	}
	for j, row := range readRows("events") {
		d.Events[j].Payload = raw(str(row["payload_json"]))
	}
	r, g := findStepByNode(d.Steps, "independent_review"), findStepByNode(d.Steps, "delivery_gate")
	if r == nil || g == nil {
		t.Fatal("missing stages")
	}
	n := compilationNodeMap(d.Run.Compilation)[r.NodeID]
	o, e := outputPort(r.Output, "analysis")
	if e != nil {
		t.Fatal(e)
	}
	if e = validateWorkflowAIStageOutput(n, o, r.Input); e != nil {
		t.Fatal("saved review invalid", e)
	}
	t.Log("saved full scientific/format review validation: PASS (approved remains false)")
	if e = validateReviewDependencies(d, *r); e != nil {
		t.Fatal("saved dependencies invalid", e)
	}
	t.Log("current dependencies: PASS")

	if researchRevisionRecommendation(d) != nil || len(researchRevisionTargets(d)) != 0 {
		t.Fatal("technical failure misrepresented as rejected review")
	}
	original := mustJSON(d)
	v, e := bindNodeInput(d, compilationNodeMap(d.Run.Compilation)[g.NodeID])
	if e != nil {
		t.Fatal("gate still fails", e)
	}
	if len(v) <= 256*1024 || tool.ValidateWorkflowArguments(v) != nil {
		t.Fatal("snapshot did not reach shared tool bound")
	}
	var envelope map[string]json.RawMessage
	_ = json.Unmarshal(v, &envelope)
	if string(envelope["subject"]) != string(r.Input) || string(original) != string(mustJSON(d)) {
		t.Fatal("snapshot mutated")
	}
	// Simulate ONLY the deterministic gate's business refusal in this local
	// captured value; live gate invocation is separately tested with SQLite.
	g.Input = v
	g.InputSHA256 = hashJSON(v)
	g.ErrorCode = "RESEARCH_REVIEW_NOT_APPROVED"
	rec := researchRevisionRecommendation(d)
	if rec == nil || rec.Status != "recommended" || rec.NodeID != "evidence_screening" || len(rec.RequiredInputs) != 0 || rec.ReviewOutputSHA256 == "" {
		t.Fatalf("current plan not restored: %+v", rec)
	}
	t.Logf("exact gate input bytes=%d; recommendation=%s/%s; user-input gaps=%d; review retained", len(v), rec.Status, rec.NodeID, len(rec.RequiredInputs))
}

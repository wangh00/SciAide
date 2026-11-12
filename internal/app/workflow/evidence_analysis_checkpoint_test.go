package workflow

import (
	"encoding/json"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func analysisCheckpointFixture() (RunDetail, Step, json.RawMessage, []json.RawMessage) {
	refs := []tool.CitationRef{{DocumentID: "a", Reference: "[K-000000000001]", Quote: "Original evidence A"}, {DocumentID: "b", Reference: "[K-000000000002]", Quote: "Original evidence B"}}
	input := mustJSON(map[string]any{"documentIds": []string{"a", "b", "c"}, "candidates": refs, "researchContext": "Compare the reported arms; abstract-level evidence is acceptable"})
	step := Step{ID: "screen", WorkflowRunID: "run", NodeID: "evidence_screening", Status: StepCompleted, Input: input}
	d := RunDetail{Run: Run{ID: "run"}, Steps: []Step{step, {ID: "search", NodeID: "evidence_search", Status: StepCompleted, Output: mustJSON(map[string]any{"citations": refs})}}}
	d.Steps = append(d.Steps, Step{ID: "sync", NodeID: "evidence_sync", Status: StepCompleted, Output: raw(`{"structured":{"documents":[{"id":"a"},{"id":"b"},{"id":"c"}]}}`)})
	rows := []json.RawMessage{raw(`{"documentId":"a","finding":"Arm A, not arm B","limitations":"Abstract only","references":["[K-000000000001]"],"workProduct":"Already completed comparison A"}`), raw(`{"documentId":"b","finding":"Reported B","limitations":"No long-term data","references":["[K-000000000002]"]}`), raw(`{"documentId":"c","finding":"Not used","limitations":"Metadata only","references":[]}`)}
	return d, step, input, rows
}

func appendAnalysisCheckpoint(d *RunDetail, step Step, input json.RawMessage, rows []json.RawMessage, attempt int) {
	output := mustJSON(map[string]any{"documentAnalyses": rows})
	id := string(rune('A' + attempt))
	d.AIExecutions = append(d.AIExecutions, AIExecution{ID: id, WorkflowRunID: d.Run.ID, WorkflowStepID: step.ID, Attempt: attempt, Status: "completed", Output: output, OutputSHA256: hashJSON(output)})
	step.Input = input
	d.Events = append(d.Events, RuntimeEvent{WorkflowRunID: d.Run.ID, Type: "workflow.ai_completed", Payload: mustJSON(map[string]any{"executionId": id, "sourceKey": selectedEvidenceSourceKey(*d, input), "analysisCheckpoint": evidenceCheckpointPayload(*d, step, nil)})})
}

func TestEvidenceAnalysisCheckpointReusesOnlyUnchangedScientificInputs(t *testing.T) {
	d, step, input, rows := analysisCheckpointFixture()
	appendAnalysisCheckpoint(&d, step, input, rows, 1)
	changed := decodeObject(input)
	changed["_reviewRevision"], changed["_reviewCorrections"] = "Fix synthesis attribution only", []string{"Do not conflate the comparison arms"}
	prepared, err := prepareSelectedEvidence(d, mustJSON(changed))
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Info selectedEvidenceInput `json:"_selectedEvidence"`
	}
	_ = json.Unmarshal(prepared, &in)
	if in.Info.Phase != "coverage" || in.Info.Direct || len(in.Info.Analyses) != 3 {
		t.Fatal("synthesis-only repair repeated document work")
	}
	for _, expected := range rows {
		found := false
		for _, row := range in.Info.Analyses {
			found = found || string(row) == string(expected)
		}
		if !found {
			t.Fatal("accepted record was rewritten")
		}
	}
	for _, field := range []string{"researchContext", "_userRevision", "researchContract"} {
		t.Run(field, func(t *testing.T) {
			args := decodeObject(input)
			args[field] = "New scientific requirement"
			if got := acceptedEvidenceAnalyses(d, step, mustJSON(args), "different"); len(got) != 0 {
				t.Fatal("changed user/scientific scope reused prior work")
			}
		})
	}
	// A new excerpt invalidates only its document, not the entire collection.
	ref := tool.CitationRef{DocumentID: "b", Reference: "[K-000000000003]", Quote: "New information changes the comparison"}
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.indexed_evidence_read", Payload: mustJSON(map[string]any{"materialKey": localEvidenceMaterialKey(d), "citations": []tool.CitationRef{ref}})})
	got := acceptedEvidenceAnalyses(d, step, input, "different")
	if len(got) != 2 {
		t.Fatalf("new evidence invalidated wrong number of documents: %d", len(got))
	}
	for _, row := range got {
		if decodeObject(row)["documentId"] == "b" {
			t.Fatal("changed document reused")
		}
	}
}

func TestEvidenceAnalysisCheckpointIdentityAndIntegrity(t *testing.T) {
	d, step, input, rows := analysisCheckpointFixture()
	before := evidenceAnalysisKeys(d, input)
	var refs []tool.CitationRef
	_ = json.Unmarshal(mustJSON(decodeObject(d.Steps[1].Output)["citations"]), &refs)
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.indexed_evidence_read", Payload: mustJSON(map[string]any{"materialKey": localEvidenceMaterialKey(d), "citations": []tool.CitationRef{refs[1], refs[0], refs[1]}})})
	if !rawJSONEqual(mustJSON(before), mustJSON(evidenceAnalysisKeys(d, input))) {
		t.Fatal("duplicate read/order invalidated work")
	}
	for _, id := range []string{"a", "b", "c"} {
		args := projectSelectedEvidenceBatch(d, decodeObject(input), []string{id}, refs)
		if evidenceAnalysisKeys(d, mustJSON(args))[id] != before[id] {
			t.Fatal("batch/full identity differs", id)
		}
	}
	appendAnalysisCheckpoint(&d, step, input, rows, 1)
	for _, test := range []struct {
		name   string
		change func(*RunDetail)
	}{
		{"bad hash", func(d *RunDetail) { d.AIExecutions[0].OutputSHA256 = "corrupted" }},
		{"other run", func(d *RunDetail) { d.AIExecutions[0].WorkflowRunID = "other" }},
		{"other step", func(d *RunDetail) { d.AIExecutions[0].WorkflowStepID = "other" }},
		{"other event owner", func(d *RunDetail) { d.Events[len(d.Events)-1].WorkflowRunID = "other" }},
		{"unknown version", func(d *RunDetail) {
			v := decodeObject(d.Events[len(d.Events)-1].Payload)
			v["analysisCheckpoint"] = map[string]any{"version": "document-analysis-v999"}
			d.Events[len(d.Events)-1].Payload = mustJSON(v)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var copy RunDetail
			_ = json.Unmarshal(mustJSON(d), &copy)
			test.change(&copy)
			if len(acceptedEvidenceAnalyses(copy, step, input, selectedEvidenceSourceKey(d, input))) != 0 {
				t.Fatal("invalid checkpoint reused")
			}
		})
	}
}

func TestEvidenceCoverageCanReplaceOneAnalysisWithoutRewritingOthers(t *testing.T) {
	d, step, input, rows := analysisCheckpointFixture()
	appendAnalysisCheckpoint(&d, step, input, rows, 1)
	prepared, err := prepareSelectedEvidence(d, input)
	if err != nil {
		t.Fatal(err)
	}
	update := raw(`{"documentId":"a","finding":"Corrected arm A attribution","limitations":"Abstract only","references":["[K-000000000001]"],"workProduct":"Corrected comparison"}`)
	output := map[string]any{"documentAnalyses": []json.RawMessage{update}, "recommendedReferences": []string{}, "citationAssessments": []any{}, "recommendation": "proceed_limited"}
	if err := validateSelectedEvidence(mustJSON(output), prepared); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []json.RawMessage{raw(`{"documentId":"outside","references":[]}`), raw(`{"documentId":"a","references":["[K-000000000002]"]}`)} {
		output["documentAnalyses"] = []json.RawMessage{invalid}
		if validateSelectedEvidence(mustJSON(output), prepared) == nil {
			t.Fatal("invalid replacement accepted")
		}
	}
	output["documentAnalyses"] = []json.RawMessage{update, update}
	if validateSelectedEvidence(mustJSON(output), prepared) == nil {
		t.Fatal("duplicate replacement accepted")
	}
	output["documentAnalyses"] = []json.RawMessage{update}
	d.Steps[0].Input = prepared
	d.Steps[0].Output = mustJSON(map[string]any{"analysis": output})
	work := decodeObject(mustJSON(currentEvidenceWork(d)))
	var merged []json.RawMessage
	_ = json.Unmarshal(mustJSON(work["documentAnalyses"]), &merged)
	if len(merged) != 3 {
		t.Fatal("downstream lost records")
	}
	for _, row := range merged {
		id := decodeObject(row)["documentId"]
		want := rows[0]
		if id == "a" {
			want = update
		} else if id == "b" {
			want = rows[1]
		} else {
			want = rows[2]
		}
		if string(row) != string(want) {
			t.Fatal("downstream changed unrelated work", id)
		}
	}
	appendAnalysisCheckpoint(&d, step, input, []json.RawMessage{update}, 2)
	var restarted RunDetail
	_ = json.Unmarshal(mustJSON(d), &restarted)
	next := acceptedEvidenceAnalyses(restarted, step, input, "new revision")
	if len(next) != 3 || string(next[0]) != string(update) {
		t.Fatal("restart lost newest local correction")
	}
}

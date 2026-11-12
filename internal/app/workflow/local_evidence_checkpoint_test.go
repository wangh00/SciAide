package workflow

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/tool"
)

type localAnalysisCheckpointRepository struct {
	indexedAcceptedFixture
}

func TestLocalFollowupCheckpointsRequireBoundExecution(t *testing.T) {
	for _, change := range []string{"run", "step", "attempt", "status", "input", "output", "hash", "submitted_output"} {
		t.Run(change, func(t *testing.T) {
			d, step, input, _ := analysisCheckpointFixture()
			step.Status, step.Ordinal, step.Attempt, step.InputSHA256 = StepRunning, 3, 1, hashJSON(input)
			d.Steps[1].Ordinal, d.Steps[1].Input = 2, raw(`{"limit":20}`)
			output := raw(`{"coverage":{"sufficientForClaimedScope":true},"supplementalQueries":["bounded local query"]}`)
			e := AIExecution{ID: "execution", WorkflowRunID: d.Run.ID, WorkflowStepID: step.ID, Attempt: step.Attempt, Status: "completed", InputSHA256: step.InputSHA256, Output: output, OutputSHA256: hashJSON(output)}
			switch change {
			case "run":
				e.WorkflowRunID = "other"
			case "step":
				e.WorkflowStepID = "other"
			case "attempt":
				e.Attempt++
			case "status":
				e.Status = "failed"
			case "input":
				e.InputSHA256 = "other"
			case "output":
				e.Output = raw(`{}`)
			case "hash":
				e.OutputSHA256 = "other"
			case "submitted_output":
				output = raw(`{"coverage":{"sufficientForClaimedScope":true},"supplementalQueries":["different query"]}`)
			}
			repo := &localAnalysisCheckpointRepository{}
			s := &RuntimeService{repository: repo, now: time.Now, newID: func() (string, error) { return "event", nil }}
			if handled, err := s.supplementLocalEvidenceWithEvidence(context.Background(), d, step, output, nil, &e); !handled || err == nil || repo.queued {
				t.Fatal("unbound checkpoint reached the search transaction", handled, err)
			}
		})
	}
}

func TestLocalFollowupReusesOnlyCurrentDocumentInputs(t *testing.T) {
	d, step, input, rows := analysisCheckpointFixture()
	appendAnalysisCheckpoint(&d, step, input, rows, 1)
	d.Events[len(d.Events)-1].Type = "workflow.local_evidence_requested"
	if got := acceptedEvidenceAnalyses(d, step, input, "changed whole-search key"); !reflect.DeepEqual(got, rows) {
		t.Fatal("follow-up did not preserve complete unchanged records")
	}
	// Only document B receives genuinely new evidence; A and C stay exact.
	newRef := tool.CitationRef{DocumentID: "b", Reference: "[K-000000000003]", Quote: "Additional bounded result for B"}
	d.Events = append(d.Events, RuntimeEvent{WorkflowRunID: d.Run.ID, Type: "workflow.indexed_evidence_read", Payload: mustJSON(map[string]any{"materialKey": localEvidenceMaterialKey(d), "citations": []tool.CitationRef{newRef}})})
	got := acceptedEvidenceAnalyses(d, step, input, "changed whole-search key")
	if len(got) != 2 || string(got[0]) != string(rows[0]) || string(got[1]) != string(rows[2]) {
		t.Fatal("follow-up repeated unchanged documents or reused changed B")
	}
	// Old follow-up events cannot be retroactively upgraded from a sourceKey.
	payload := decodeObject(d.Events[0].Payload)
	delete(payload, "analysisCheckpoint")
	d.Events[0].Payload = mustJSON(payload)
	if got := acceptedEvidenceAnalyses(d, step, input, selectedEvidenceSourceKey(d, input)); len(got) != 0 {
		t.Fatal("historical follow-up was treated as an accepted checkpoint")
	}
}

func TestLocalFollowupIgnoresOnlyCoverageQueryAudit(t *testing.T) {
	d, step, input, rows := analysisCheckpointFixture()
	args := decodeObject(input)
	coverage := []map[string]any{}
	for _, id := range []string{"a", "b", "c"} {
		coverage = append(coverage, map[string]any{"documentId": id, "queries": []string{"epsilon"}, "excerptCount": 1, "searchCompleted": true, "selectionLimited": false, "fullTextRead": false, "readingUnit": "verified_index_chunk"})
	}
	args["documentCoverage"] = coverage
	input = mustJSON(args)
	appendAnalysisCheckpoint(&d, step, input, rows, 1)
	d.Events[len(d.Events)-1].Type = "workflow.local_evidence_requested"
	for _, row := range coverage {
		row["queries"] = []string{"epsilon", "bounded additional observation"}
	}
	updated := mustJSON(args)
	if got := acceptedEvidenceAnalyses(d, step, updated, "new whole-search key"); !reflect.DeepEqual(got, rows) {
		t.Fatal("query audit alone invalidated unchanged document evidence")
	}
	for _, field := range []string{"excerptCount", "searchCompleted", "selectionLimited", "fullTextRead", "readingUnit"} {
		t.Run(field, func(t *testing.T) {
			var changed []map[string]any
			_ = json.Unmarshal(mustJSON(coverage), &changed)
			switch field {
			case "excerptCount":
				changed[1][field] = 2
			case "readingUnit":
				changed[1][field] = "changed scientific scope"
			default:
				changed[1][field] = changed[1][field] != true
			}
			args := decodeObject(updated)
			args["documentCoverage"] = changed
			got := acceptedEvidenceAnalyses(d, step, mustJSON(args), "new whole-search key")
			if len(got) != 2 || string(got[0]) != string(rows[0]) || string(got[1]) != string(rows[2]) {
				t.Fatal("changed coverage reused B or invalidated unrelated A/C")
			}
		})
	}
}

func TestEvidenceAnalysisV1KeepsItsOriginalIdentityRules(t *testing.T) {
	d, step, input, rows := analysisCheckpointFixture()
	args := decodeObject(input)
	args["documentCoverage"] = []map[string]any{{"documentId": "a", "queries": []string{"original query"}, "excerptCount": 1}}
	input = mustJSON(args)
	appendAnalysisCheckpoint(&d, step, input, rows, 1)
	payload := decodeObject(d.Events[0].Payload)
	payload["analysisCheckpoint"] = map[string]any{"version": evidenceAnalysisCheckpointV1, "keys": evidenceAnalysisKeysForVersion(d, input, evidenceAnalysisCheckpointV1)}
	d.Events[0].Payload = mustJSON(payload)
	if got := acceptedEvidenceAnalyses(d, step, input, "different whole-search key"); !reflect.DeepEqual(got, rows) {
		t.Fatal("unchanged historical v1 work was invalidated")
	}
	args["documentCoverage"] = []map[string]any{{"documentId": "a", "queries": []string{"original query", "new audit query"}, "excerptCount": 1}}
	got := acceptedEvidenceAnalyses(d, step, mustJSON(args), "different whole-search key")
	if len(got) != 2 || string(got[0]) != string(rows[1]) || string(got[1]) != string(rows[2]) {
		t.Fatal("historical v1 work was silently upgraded to v2 identity")
	}
}

func TestEvidenceAnalysisDoesNotIgnoreUnknownQueryAuditShapes(t *testing.T) {
	for _, audit := range []any{nil, "query", 1, true, map[string]any{"query": "other"}, []any{nil}, []any{"other", nil}, []any{"other", 1}, []any{"other", map[string]any{"query": "different"}}} {
		d, step, input, rows := analysisCheckpointFixture()
		args := decodeObject(input)
		args["documentCoverage"] = []map[string]any{{"documentId": "b", "queries": []string{"original"}}}
		input = mustJSON(args)
		appendAnalysisCheckpoint(&d, step, input, rows, 1)
		args["documentCoverage"] = []map[string]any{{"documentId": "b", "queries": audit}}
		got := acceptedEvidenceAnalyses(d, step, mustJSON(args), "different search key")
		if len(got) != 2 || string(got[0]) != string(rows[0]) || string(got[1]) != string(rows[2]) {
			t.Fatalf("unknown query shape was erased from identity: %s", mustJSON(audit))
		}
	}
}

func (r *localAnalysisCheckpointRepository) QueueLocalEvidenceSearch(_ context.Context, _, _, _ string, _ int, _ time.Time, event RuntimeEvent) error {
	r.event, r.queued = event, true
	return nil
}

// A fully validated synthesis requests a bounded local follow-up. Its existing
// document work must remain available even though the overall step is not done.
func TestLocalFollowupKeepsValidatedDocumentAnalysis(t *testing.T) {
	d, step, node, execution, calls := indexedFixture()
	step.Ordinal = 3
	step.WorkflowRunID = d.Run.ID
	step.Input = raw(`{"documentIds":["doc"],"candidates":[],"_selectedEvidence":{"direct":true,"phase":"coverage","documents":["doc"]}}`)
	step.InputSHA256 = hashJSON(step.Input)
	d.Run.Status, d.Run.CurrentStep = RunRunning, step.Ordinal
	d.Steps = append(d.Steps[:2], Step{ID: "search", NodeID: "evidence_search", Ordinal: 2, Status: StepCompleted, Input: raw(`{"limit":20}`), Output: raw(`{"citations":[]}`)}, step)
	execution.WorkflowRunID, execution.InputSHA256 = d.Run.ID, step.InputSHA256
	text := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(indexedSynthesis(calls.calls[0].Result.Citations[0]), "```json"), "```"))
	output := decodeObject(raw(text))
	output["supplementalQueries"] = []string{"bounded missing outcome within selected material"}
	var documents []map[string]any
	if err := json.Unmarshal(mustJSON(output["documentAnalyses"]), &documents); err != nil {
		t.Fatal(err)
	}
	documents[0]["applicability"] = "Synthetic bounded mechanism only"
	output["documentAnalyses"] = documents
	execution.OutputText = string(mustJSON(output))
	repo := &localAnalysisCheckpointRepository{}
	s := &RuntimeService{repository: repo, tools: tool.NewService(calls, tool.JSONSchemaValidator{}), now: time.Now, newID: func() (string, error) { return "event", nil }}
	if err := s.completeAIExecution(context.Background(), d, step, node, execution, true); err != nil {
		t.Fatal(err)
	}
	if !repo.queued || repo.event.Type != "workflow.local_evidence_requested" || repo.execution.Status != "completed" || repo.execution.OutputSHA256 == "" || len(repo.output) != 0 || len(repo.repairs) != 0 {
		t.Fatalf("fixture did not reach a validated local follow-up: queued=%v code=%s", repo.queued, repo.failure)
	}
	d.AIExecutions = append(d.AIExecutions, repo.execution)
	d.Events = append(d.Events, repo.event)
	next, err := prepareSelectedEvidence(d, bindIndexedEvidence(d, node, step.Input))
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		Info selectedEvidenceInput `json:"_selectedEvidence"`
	}
	if err := json.Unmarshal(next, &input); err != nil {
		t.Fatal(err)
	}
	if input.Info.Phase != "coverage" || input.Info.Direct || len(input.Info.Analyses) != 1 {
		t.Fatalf("local follow-up lost a validated document analysis: phase=%s direct=%v analyses=%d", input.Info.Phase, input.Info.Direct, len(input.Info.Analyses))
	}
	if hashJSON(step.Input) != step.InputSHA256 || hashJSON(repo.execution.Output) != repo.execution.OutputSHA256 {
		t.Fatal("follow-up changed frozen input or accepted output bytes")
	}
}

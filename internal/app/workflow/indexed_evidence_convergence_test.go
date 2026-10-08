package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/tool"
)

type indexedAcceptedFixture struct {
	indexedContinuationFixture
	completedEvent RuntimeEvent
	commitErr      error
}

func (r *indexedAcceptedFixture) CompleteStep(_ context.Context, _, _ string, out json.RawMessage, _ int, _ bool, _ json.RawMessage, _ time.Time, ev RuntimeEvent) error {
	r.output, r.completedEvent = out, ev
	return r.commitErr
}

func TestIndexedEvidenceAcceptedSynthesisRecoversAfterCommitFault(t *testing.T) {
	d, step, node, e, calls := indexedFixture()
	step.Input = raw(`{"documentIds":["doc"],"candidates":[],"_selectedEvidence":{"direct":true,"phase":"coverage","documents":["doc"]}}`)
	step.InputSHA256 = hashJSON(step.Input)
	e.InputSHA256, e.OutputText = step.InputSHA256, indexedSynthesis(calls.calls[0].Result.Citations[0])
	d.Steps[2] = step
	repo := &indexedAcceptedFixture{commitErr: fmt.Errorf("injected commit fault")}
	unused := calls.calls[0].Result.Citations[0]
	unused.ChunkID, unused.Locator = "checked-but-not-cited", "page:3"
	unused.Reference = citation.KnowledgeReference(e.ChatRunID, unused.IndexVersionID, unused.ChunkID, unused.QuoteSHA256)
	calls.calls[0].Result.Citations = append(calls.calls[0].Result.Citations, unused)
	s := &RuntimeService{repository: repo, tools: tool.NewService(calls, tool.JSONSchemaValidator{}), now: time.Now, newID: func() (string, error) { return "event", nil }}
	if err := s.completeAIExecution(context.Background(), d, step, node, e, true); err == nil {
		t.Fatal("missing commit fault")
	}
	frozen := repo.execution
	if frozen.Status != "completed" || rawJSONEqual(frozen.Output, raw(`{}`)) {
		t.Fatal("synthesis was not frozen")
	}
	repo.commitErr, repo.output = nil, nil
	d.AIExecutions = []AIExecution{frozen}
	if err := s.completeAIExecution(context.Background(), d, step, node, frozen, false); err != nil {
		t.Fatal(err)
	}
	if repo.failure != "" || repo.queued || len(repo.output) == 0 || repo.execution.OutputSHA256 != frozen.OutputSHA256 {
		t.Fatal("restart discarded or changed accepted synthesis")
	}
	d.Events = []RuntimeEvent{repo.completedEvent}
	d.Steps[2].Output = repo.output
	if _, err := s.verifiedIndexedEvidence(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if offered, ok := acceptedIndexedCandidates(d, d.Steps[2]); !ok || len(offered) != 1 {
		t.Fatal("recovered candidates not frozen")
	}
	if ranges := indexedReadRanges(d); len(ranges) != 2 {
		t.Fatal("accepted checked scope lost uncited actual range", ranges)
	}
}

func TestIndexedEvidenceManualAdoptionAndContinuationAuditBindings(t *testing.T) {
	d, step, node, e, calls := indexedFixture()
	step.Input = raw(`{"documentIds":["doc"],"candidates":[],"_selectedEvidence":{"direct":true,"phase":"coverage","documents":["doc"]}}`)
	step.InputSHA256 = hashJSON(step.Input)
	e.InputSHA256, e.OutputText = step.InputSHA256, indexedSynthesis(calls.calls[0].Result.Citations[0])
	d.Steps[2] = step
	repo := &indexedAcceptedFixture{}
	s := &RuntimeService{repository: repo, tools: tool.NewService(calls, tool.JSONSchemaValidator{}), now: time.Now, newID: func() (string, error) { return "event", nil }}
	if err := s.completeAIExecution(context.Background(), d, step, node, e, true); err != nil {
		t.Fatal(err)
	}
	d.AIExecutions = []AIExecution{repo.execution}
	var completed map[string]json.RawMessage
	_ = json.Unmarshal(repo.completedEvent.Payload, &completed)
	for _, kind := range []string{"workflow.selected_evidence_batch", "workflow.local_evidence_requested"} {
		d.Events = []RuntimeEvent{{Type: kind, Payload: mustJSON(map[string]any{"indexedEvidence": completed["indexedEvidence"]})}}
		if refs, err := s.verifiedIndexedEvidence(context.Background(), d); err != nil || len(refs) != 1 {
			t.Fatal("continuation lost accepted read", kind, err)
		}
	}
	d.Events = []RuntimeEvent{repo.completedEvent}
	out := decodeObject(repo.output)
	var window struct {
		Candidates []tool.CitationRef `json:"evidenceCandidates"`
	}
	_ = json.Unmarshal(repo.output, &window)
	out["evidenceCandidates"] = window.Candidates
	analysis := decodeObject(mustJSON(out["analysis"]))
	analysis["summary"] = "User-adopted bounded interpretation"
	out["analysis"], out["evidenceSourceOutputSha256"] = analysis, repo.execution.OutputSHA256
	step.Output = mustJSON(out)
	d.Steps[2] = step
	if _, ok := acceptedIndexedCandidates(d, step); ok {
		t.Fatal("adoption trusted without decision")
	}
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.human_decided", Payload: mustJSON(map[string]any{"stepId": step.ID, "approved": true, "outputSha256": hashJSON(mustJSON(analysis)), "evidenceSourceOutputSha256": repo.execution.OutputSHA256, "evidenceCandidatesSha256": hashJSON(mustJSON(out["evidenceCandidates"]))})})
	if refs, ok := acceptedIndexedCandidates(d, step); !ok || len(refs) != 1 {
		t.Fatal("adopted verified window lost")
	}
	analysis["summary"] = "tampered after decision"
	step.Output = mustJSON(out)
	if _, ok := acceptedIndexedCandidates(d, step); ok {
		t.Fatal("tampered adopted synthesis trusted")
	}
}

func indexedSynthesis(ref tool.CitationRef) string {
	return "```json\n" + string(mustJSON(map[string]any{
		"summary":               "Verified bounded finding " + ref.Reference,
		"recommendedReferences": []string{ref.Reference},
		"citationAssessments":   []any{map[string]any{"reference": ref.Reference, "decision": "support", "reason": "Original passage supports the bounded finding", "sourceLevel": "full_text", "supportingQuote": ref.Quote}},
		"coverage":              map[string]any{"strength": "limited", "sufficientForClaimedScope": true, "independentStudyEstimate": 1, "directPopulationEvidence": false, "fullTextEvidenceAvailable": true, "gaps": []string{}},
		"recommendedScope":      "The verified passage only", "recommendation": "proceed_limited", "limitations": []string{"A fixture, not real science"},
		"documentAnalyses": []any{map[string]any{"documentId": ref.DocumentID, "finding": "Verified bounded finding", "limitations": "Only checked passage", "references": []string{ref.Reference}}},
	})) + "\n```"
}

func TestIndexedEvidenceCompactSubmissionKeepsVerifiedOriginalSnapshot(t *testing.T) {
	d, step, node, e, calls := indexedFixture()
	repo := &indexedAcceptedFixture{}
	s := &RuntimeService{repository: repo, tools: tool.NewService(calls, tool.JSONSchemaValidator{}), now: time.Now, newID: func() (string, error) { return "event", nil }}
	if ok, err := s.checkpointIndexedEvidence(context.Background(), d, step, node, e); !ok || err != nil {
		t.Fatal(ok, err)
	}
	d.Events, d.AIExecutions = []RuntimeEvent{repo.event}, []AIExecution{repo.execution}
	legacy := indexedEvidenceSnapshots(d)[0]
	step.Attempt++
	step.Input = mustJSON(map[string]any{"documentIds": []string{"doc"}, "candidates": []map[string]any{{"reference": legacy.Reference, "documentId": legacy.DocumentID, "quote": legacy.Quote, "sourceName": legacy.SourceName, "title": legacy.Title}}, "_selectedEvidence": map[string]any{"direct": true, "phase": "coverage", "documents": []string{"doc"}}})
	step.InputSHA256 = hashJSON(step.Input)
	e.ID, e.ChatRunID, e.Attempt, e.InputSHA256 = "compact-execution", "compact-chat", step.Attempt, step.InputSHA256
	current := calls.calls[0].Result.Citations[0]
	current.ID, current.Reference = current.AttachmentID, citation.KnowledgeReference(e.ChatRunID, current.IndexVersionID, current.ChunkID, current.QuoteSHA256)
	calls.calls = append(calls.calls, tool.Call{ID: "compact-read", RunID: e.ChatRunID, ToolName: "builtin.resource.open", Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Structured: raw(`{"sourceTool":"builtin.knowledge.read"}`), Citations: []tool.CitationRef{current}}})
	d.Steps[2] = step
	e.OutputText = indexedSynthesis(current)
	repo.queued = false
	if err := s.completeAIExecution(context.Background(), d, step, node, e, true); err != nil {
		t.Fatal(err)
	}
	if repo.failure != "" || repo.queued || len(repo.output) == 0 {
		t.Fatal("compact synthesis failed", repo.failure)
	}
	d.Events = append(d.Events, repo.completedEvent)
	d.AIExecutions = append(d.AIExecutions, repo.execution)
	step.Output = repo.output
	d.Steps[2] = step
	refs, ok := acceptedIndexedCandidates(d, step)
	if !ok || len(refs) != 1 || validateCitationSubset(refs, []tool.CitationRef{legacy}) != nil {
		t.Fatal("compact frozen candidate replaced with reread", refs)
	}
	selection := bindIndexedEvidence(d, CompiledNode{ID: "evidence_extraction"}, raw(`{"candidates":[]}`))
	var offered struct {
		Candidates []tool.CitationRef `json:"candidates"`
	}
	_ = json.Unmarshal(selection, &offered)
	if validateCitationSubset(offered.Candidates, []tool.CitationRef{legacy}) != nil {
		t.Fatal("selection replaced compact original")
	}
}

func TestIndexedEvidenceEvictedRereadAndNewReadCommitSameChatSynthesis(t *testing.T) {
	for _, reread := range []bool{false, true} {
		t.Run(fmt.Sprintf("evicted_reread=%v", reread), func(t *testing.T) {
			d, step, node, e, calls := indexedFixture()
			step.Input = raw(`{"documentIds":["doc"],"candidates":[],"_selectedEvidence":{"direct":true,"phase":"coverage","documents":["doc"]}}`)
			step.InputSHA256 = hashJSON(step.Input)
			e.InputSHA256 = step.InputSHA256
			repo := &indexedAcceptedFixture{}
			s := &RuntimeService{repository: repo, tools: tool.NewService(calls, tool.JSONSchemaValidator{}), now: time.Now, newID: func() (string, error) { return "event", nil }}
			originalInput := string(step.Input)
			if reread {
				if ok, err := s.checkpointIndexedEvidence(context.Background(), d, step, node, e); err != nil || !ok {
					t.Fatal(ok, err)
				}
				d.Events, d.AIExecutions = []RuntimeEvent{repo.event}, []AIExecution{repo.execution}
				step.Attempt++
				e.ID, e.ChatRunID, e.Attempt = "reread-execution", "reread-chat", step.Attempt
				r := calls.calls[0].Result.Citations[0]
				r.ID = r.AttachmentID
				r.Reference = citation.KnowledgeReference(e.ChatRunID, r.IndexVersionID, r.ChunkID, r.QuoteSHA256)
				calls.calls = append(calls.calls, tool.Call{ID: "reread-call", RunID: e.ChatRunID, ToolName: "builtin.resource.open", Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Structured: raw(`{"sourceTool":"builtin.knowledge.read"}`), Citations: []tool.CitationRef{r}}})
			}
			d.Steps[2] = step
			ref := calls.calls[len(calls.calls)-1].Result.Citations[0]
			e.OutputText = indexedSynthesis(ref)
			repo.queued = false
			if err := s.completeAIExecution(context.Background(), d, step, node, e, true); err != nil {
				t.Fatal(err)
			}
			if repo.queued || len(repo.repairs) != 0 || repo.failure != "" || len(repo.output) == 0 || rawJSONEqual(repo.execution.Output, raw(`{}`)) {
				t.Fatalf("valid synthesis restarted or failed: queued=%v failure=%s error=%s", repo.queued, repo.failure, repo.execution.ErrorMessage)
			}
			if string(step.Input) != originalInput {
				t.Fatal("immutable request was changed")
			}
			d.Events = append(d.Events, repo.completedEvent)
			d.AIExecutions = append(d.AIExecutions, repo.execution)
			step.Status, step.Output = StepCompleted, repo.output
			d.Steps[2] = step
			// Persistence/restart must retain the exact current source (including
			// legacy/current ID variants), not reconstruct from the old input.
			if err := json.Unmarshal(mustJSON(d), &d); err != nil {
				t.Fatal(err)
			}
			verified, err := s.verifiedIndexedEvidence(context.Background(), d)
			want := 1
			if reread {
				want = 2
			}
			if err != nil || len(verified) != want {
				t.Fatal("accepted read audit", len(verified), err)
			}
			selection := bindIndexedEvidence(d, CompiledNode{ID: "evidence_extraction"}, raw(`{"candidates":[]}`))
			var offered struct {
				Candidates []tool.CitationRef `json:"candidates"`
			}
			_ = json.Unmarshal(selection, &offered)
			ref.Reference = citation.KnowledgeReference(d.Run.ID, ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
			if len(offered.Candidates) != 1 || validateCitationSubset(offered.Candidates, []tool.CitationRef{ref}) != nil {
				t.Fatal("selection lost observed evidence", string(selection))
			}
			repo.detail = d
			if sources, err := s.IndexedEvidenceSources(context.Background(), d.Run.ProjectID, d.Run.ID); err != nil || len(sources) != want {
				t.Fatal("publication lost accepted source", len(sources), err)
			}
			calls.calls[len(calls.calls)-1].Result.Citations[0].Quote = "tampered"
			if _, err := s.verifiedIndexedEvidence(context.Background(), d); err == nil {
				t.Fatal("tampered accepted source was trusted")
			}
		})
	}
}

func TestIndexedEvidenceLongWindowEvictionsNeverCreateProgress(t *testing.T) {
	d, step, node, e, calls := indexedFixture()
	refs := []tool.CitationRef{}
	base := calls.calls[0].Result.Citations[0]
	for i := 0; i < 100; i++ {
		r := base
		r.ChunkID = fmt.Sprintf("chunk-%d", i)
		r.Quote = strings.Repeat(fmt.Sprintf("quote %d ", i), 300)
		r.QuoteSHA256 = citation.QuoteSHA256(r.Quote)
		r.Reference = citation.KnowledgeReference(d.Run.ID, r.IndexVersionID, r.ChunkID, r.QuoteSHA256)
		refs = append(refs, r)
	}
	d.Events = []RuntimeEvent{{Type: "workflow.indexed_evidence_read", Payload: mustJSON(indexedEvidencePage{Key: localEvidenceMaterialKey(d), Citations: refs})}}
	step.Input = bindIndexedEvidence(d, node, step.Input)
	var in struct {
		Candidates []tool.CitationRef `json:"candidates"`
	}
	_ = json.Unmarshal(step.Input, &in)
	if len(in.Candidates) >= len(refs) {
		t.Fatal("fixture did not exercise eviction")
	}
	s := &RuntimeService{tools: tool.NewService(calls, tool.JSONSchemaValidator{})}
	for round := 0; round < 40; round++ {
		step.Attempt++
		step.InputSHA256 = hashJSON(step.Input)
		e.Attempt, e.InputSHA256 = step.Attempt, step.InputSHA256
		r := refs[len(refs)-1-round%20]
		r.Reference = citation.KnowledgeReference(e.ChatRunID, r.IndexVersionID, r.ChunkID, r.QuoteSHA256)
		calls.calls[0].Result.Citations = []tool.CitationRef{r}
		if added, err := s.newIndexedEvidence(context.Background(), d, step, node, e); err != nil || len(added) != 0 {
			t.Fatal("eviction counted as scientific progress", round, added, err)
		}
	}
	// A genuinely different indexed excerpt still checkpoints; no fixed round
	// ceiling prevents expanding to additional relevant evidence.
	r := base
	r.ChunkID = "actually-new"
	r.Reference = citation.KnowledgeReference(e.ChatRunID, r.IndexVersionID, r.ChunkID, r.QuoteSHA256)
	calls.calls[0].Result.Citations = []tool.CitationRef{r}
	if added, err := s.newIndexedEvidence(context.Background(), d, step, node, e); err != nil || len(added) != 1 {
		t.Fatal("genuine new evidence lost", added, err)
	}
}

func TestIndexedEvidenceSelectionDoesNotRebudgetAcceptedUsedSources(t *testing.T) {
	d, step, _, e, calls := indexedFixture()
	refs := []tool.CitationRef{}
	for i := 0; i < 100; i++ {
		ref := calls.calls[0].Result.Citations[0]
		ref.ChunkID = fmt.Sprintf("accepted-%d", i)
		ref.Quote = strings.Repeat("exact source ", 100)
		ref.QuoteSHA256 = citation.QuoteSHA256(ref.Quote)
		ref.Reference = citation.KnowledgeReference(d.Run.ID, ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
		refs = append(refs, ref)
	}
	if len(mustJSON(refs)) <= 80*1024 {
		t.Fatal("fixture must exceed model working-set budget")
	}
	step.InputSHA256 = hashJSON(step.Input)
	e.Status, e.Output, e.OutputSHA256 = "completed", raw(`{"summary":"Accepted full synthesis"}`), hashJSON(raw(`{"summary":"Accepted full synthesis"}`))
	e.InputSHA256 = step.InputSHA256
	step.Output = mustJSON(map[string]any{"analysis": e.Output, "evidenceCandidates": refs})
	d.Steps[2], d.AIExecutions = step, []AIExecution{e}
	d.Events = []RuntimeEvent{{Type: "workflow.ai_completed", Payload: mustJSON(map[string]any{"stepId": step.ID, "executionId": e.ID, "outputSha256": e.OutputSHA256, "evidenceCandidatesSha256": hashJSON(mustJSON(refs)), "indexedEvidence": indexedEvidencePage{Key: localEvidenceMaterialKey(d), Run: e.ChatRunID, StepID: step.ID, ExecutionID: e.ID, Attempt: e.Attempt, Citations: refs, AcceptedOutputSHA256: e.OutputSHA256}})}}
	selection := bindIndexedEvidence(d, CompiledNode{ID: "evidence_extraction"}, raw(`{"candidates":[]}`))
	var out struct {
		Candidates []tool.CitationRef `json:"candidates"`
	}
	_ = json.Unmarshal(selection, &out)
	if len(out.Candidates) != 100 || validateCitationSubset(out.Candidates, refs) != nil {
		t.Fatal("selection discarded accepted sources", len(out.Candidates))
	}
}

func TestIndexedEvidenceValidBoundedNoRecommendationIsNotCollectionDraft(t *testing.T) {
	d, step, node, e, calls := indexedFixture()
	step.Input = raw(`{"documentIds":["doc"],"candidates":[],"_selectedEvidence":{"direct":true,"phase":"coverage","documents":["doc"]}}`)
	step.InputSHA256 = hashJSON(step.Input)
	e.InputSHA256 = step.InputSHA256
	value, _, err := normalizeWorkflowAIStageSubmissionForInput(indexedSynthesis(calls.calls[0].Result.Citations[0]), selectedEvidencePhaseNode(node, step.Input), step.Input)
	if err != nil {
		t.Fatal(err)
	}
	out := decodeObject(value)
	out["summary"] = "Checked material is inapplicable; no positive evidence recommendation."
	out["recommendedReferences"], out["citationAssessments"] = []string{}, []any{}
	out["documentAnalyses"] = []any{map[string]any{"documentId": "doc", "finding": "Not used for the bounded conclusion", "limitations": "Checked source is inapplicable", "references": []string{}}}
	e.OutputText = "```json\n" + string(mustJSON(out)) + "\n```"
	d.Steps[2] = step
	repo := &indexedAcceptedFixture{}
	s := &RuntimeService{repository: repo, tools: tool.NewService(calls, tool.JSONSchemaValidator{}), now: time.Now, newID: func() (string, error) { return "event", nil }}
	if err := s.completeAIExecution(context.Background(), d, step, node, e, true); err != nil {
		t.Fatal(err)
	}
	if repo.queued || repo.failure != "" || len(repo.output) == 0 {
		t.Fatal("valid no-recommendation result restarted", repo.failure)
	}
}

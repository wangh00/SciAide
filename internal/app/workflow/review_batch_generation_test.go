package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func reviewEvidenceBatchFixture() (RunDetail, CompiledNode, reviewRevision) {
	d, base := selectedEvidenceProjectionFixture()
	args := decodeObject(base)
	// A realistic non-source envelope keeps this collection in batch mode even
	// after the bounded excerpt window is applied. No model or database is used.
	args["researchContract"] = strings.Repeat("Frozen research scope. ", 2500)
	node := CompiledNode{ID: "evidence_screening", Kind: NodeAgentStage, PromptVersion: selectedEvidenceVersion, ReviewPolicy: AIReviewAuto, Arguments: mustJSON(args), OutputSchema: selectedEvidenceSchema()}
	d.Run.Inputs = raw(`{}`)
	d.Run.Compilation.Nodes = []CompiledNode{node}
	for i := range d.Steps {
		d.Steps[i].Ordinal = i
	}
	s := findStepByNode(d.Steps, node.ID)
	s.Attempt, s.NodeKind = 2, node.Kind
	d.Steps = append(d.Steps, Step{ID: "review", NodeID: "independent_review", Ordinal: 3}, Step{ID: "gate", NodeID: "delivery_gate", Ordinal: 4})
	prior := raw(`{"summary":"Frozen synthesis before the confirmed repair","limitations":["Bounded source excerpts"]}`)
	review := raw(`{"requiredCorrections":["Recheck the comparison against the selected sources"],"revisionPlan":[{"correctionIndex":0,"nodeId":"evidence_screening"}]}`)
	r := reviewRevision{ProducerStepID: "screen", ProducerNodeID: node.ID, ProducerAttempt: 2, NextProducerAttempt: 3, ReviewStepID: "review", ReviewNodeID: "independent_review", GateStepID: "gate", PriorSubject: prior, PriorSubjectSHA256: hashJSON(prior), IndependentReview: review, ReviewOutputSHA256: hashJSON(review)}
	d.AIExecutions = []AIExecution{{ID: "prior-synthesis", WorkflowStepID: "screen", Attempt: 2, Status: "completed", Output: prior, OutputSHA256: hashJSON(prior)}}
	d.Events = []RuntimeEvent{
		{Type: "workflow.ai_completed", Payload: raw(`{"stepId":"screen","executionId":"prior-synthesis"}`)},
		{Type: "workflow.review_revision_queued", Payload: mustJSON(r)},
	}
	return d, node, r
}

func prepareReviewEvidenceBatch(t *testing.T, d RunDetail, node CompiledNode) json.RawMessage {
	t.Helper()
	input, err := bindNodeInput(d, node)
	if err != nil {
		t.Fatal(err)
	}
	input = addResearchSourceContext(d, node, input)
	input = bindIndexedEvidence(d, node, input)
	prepared, err := prepareSelectedEvidence(d, input)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func TestReviewEvidenceBatchesFreezeGenerationBaselineAndConverge(t *testing.T) {
	for _, interruption := range []string{"none", "cancelled", "interrupted", "failed"} {
		t.Run(interruption, func(t *testing.T) {
			d, node, revision := reviewEvidenceBatchFixture()
			original := mustJSON(revision)
			seen, batches, key := map[string]bool{}, 0, ""
			for cycle := 0; cycle < 10; cycle++ {
				input := prepareReviewEvidenceBatch(t, d, node)
				var in struct {
					Key         string                 `json:"sourceKey"`
					Info        selectedEvidenceInput  `json:"_selectedEvidence"`
					Corrections stageReviewCorrections `json:"_reviewCorrections"`
				}
				if err := json.Unmarshal(input, &in); err != nil {
					t.Fatal(err)
				}
				if key == "" {
					key = in.Key
				}
				if key != in.Key || !rawJSONEqual(in.Corrections.PriorOutput, revision.PriorSubject) {
					t.Fatal("accepted intermediate batch changed the confirmed repair baseline/sourceKey", cycle, len(seen), key, in.Key)
				}
				if in.Info.Phase == "coverage" && !in.Info.Direct {
					if len(seen) != 7 || len(in.Info.Analyses) != 7 || batches < 2 {
						t.Fatal("coverage did not retain every document disposition", len(seen), len(in.Info.Analyses), batches)
					}
					if !rawJSONEqual(original, d.Events[1].Payload) {
						t.Fatal("binding rewrote the frozen review event")
					}
					t.Logf("%d non-overlapping batches -> coverage with all 7 documents; interruption=%s", batches, interruption)
					return
				}
				if in.Info.Phase != "batch" || len(in.Info.Documents) == 0 {
					t.Fatal("fixture did not enter bounded batches")
				}
				step := findStepByNode(d.Steps, node.ID)
				step.Attempt++
				step.Status, step.Input, step.InputSHA256 = StepRunning, input, hashJSON(input)
				notes := []map[string]any{}
				for _, id := range in.Info.Documents {
					if seen[id] {
						t.Fatal("accepted document scheduled again", id)
					}
					seen[id] = true
					notes = append(notes, map[string]any{"documentId": id, "finding": "Fixture: no result claim", "limitations": "Scheduling fixture, not scientific validation", "references": []string{}})
				}
				output := mustJSON(map[string]any{"documentAnalyses": notes})
				if err := validateWorkflowAIStageOutput(selectedEvidencePhaseNode(node, input), output, input); err != nil {
					t.Fatal(err)
				}
				e := AIExecution{ID: fmt.Sprintf("batch-%d", cycle), WorkflowRunID: d.Run.ID, WorkflowStepID: step.ID, Attempt: step.Attempt, Status: "completed", InputSHA256: step.InputSHA256, Output: output, OutputSHA256: hashJSON(output)}
				repo := &literatureRepositoryFixture{}
				svc := &RuntimeService{repository: repo, now: time.Now, newID: func() (string, error) { return "batch-event", nil }}
				if err := svc.completeSelectedEvidenceBatch(context.Background(), d, *step, e); err != nil {
					t.Fatal(err)
				}
				d.AIExecutions = append(d.AIExecutions, e)
				d.Events = append(d.Events, repo.event)
				step.Status, step.Input, step.InputSHA256 = StepQueued, raw(`{}`), ""
				batches++
				if batches == 1 && interruption != "none" {
					step.Attempt++
					step.Status = StepStatus(interruption)
					d.AIExecutions = append(d.AIExecutions, AIExecution{ID: "unaccepted-draft", WorkflowStepID: step.ID, Attempt: step.Attempt, Status: interruption, Output: raw(`{"documentAnalyses":[{"documentId":"doc-0","finding":"Unaccepted draft"}]}`)})
					// Simulate reloading persisted state, then the user's explicit
					// retry. No automatic restart or paid model request is made.
					if err := json.Unmarshal(mustJSON(d), &d); err != nil {
						t.Fatal(err)
					}
					findStepByNode(d.Steps, node.ID).Status = StepQueued
				}
				if err := json.Unmarshal(mustJSON(d), &d); err != nil {
					t.Fatal(err)
				}
			}
			t.Fatal("confirmed evidence repair did not converge")
		})
	}
}

func TestReviewCorrectionBaselineIsFrozenBeforeGenerationNotLatestCollection(t *testing.T) {
	d := recommendationFixture(t)
	prior := raw(`{"markdown":"Complete report before repair"}`)
	review, _ := outputPort(d.Steps[4].Output, "analysis")
	r := reviewRevision{ProducerStepID: d.Steps[0].ID, ProducerNodeID: "method_selection", GateStepID: d.Steps[5].ID, PriorSubject: raw(`{"summary":"Frozen method"}`), IndependentReview: review, ReviewOutputSHA256: hashJSON(review)}
	r.PriorSubjectSHA256 = hashJSON(r.PriorSubject)
	at := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	d.Events = []RuntimeEvent{{Type: "workflow.review_revision_queued", Payload: mustJSON(r), CreatedAt: at}}
	s := &d.Steps[3]
	s.Status = StepQueued
	d.AIExecutions = append(d.AIExecutions, AIExecution{ID: "old-report", WorkflowStepID: s.ID, Attempt: 2, Status: "completed", Output: prior, CreatedAt: at.Add(-time.Minute)})
	for i, kind := range []string{"workflow.selected_evidence_batch", "workflow.indexed_evidence_read", "workflow.local_evidence_requested"} {
		e := AIExecution{ID: fmt.Sprint("collection-", i), WorkflowStepID: s.ID, Attempt: 3 + i, Status: "completed", Output: raw(`{"summary":"Partial collection, not a reviewed report"}`), CreatedAt: at.Add(time.Minute)}
		d.AIExecutions = append(d.AIExecutions, e)
		d.Events = append(d.Events, RuntimeEvent{Type: kind, Payload: mustJSON(map[string]any{"stepId": s.ID, "executionId": e.ID, "attempt": e.Attempt})})
		check := pendingStageReviewCorrections(d, s.NodeID)
		if check == nil || !rawJSONEqual(check.PriorOutput, prior) {
			t.Fatal("nonempty intermediate result replaced the frozen report baseline", kind)
		}
	}
	// A completed replacement consumes this generation even if a more recent
	// collection execution exists; checking only the latest execution is unsafe.
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.ai_completed", Payload: raw(`{"executionId":"old-report"}`)})
	if pendingStageReviewCorrections(d, s.NodeID) != nil {
		t.Fatal("later collection hid the accepted replacement event")
	}
}

func TestReviewEvidenceCheckpointInvalidatesOnlyChangedGenerationOrInputs(t *testing.T) {
	d, node, revision := reviewEvidenceBatchFixture()
	input := prepareReviewEvidenceBatch(t, d, node)
	var first struct {
		Key  string                `json:"sourceKey"`
		Info selectedEvidenceInput `json:"_selectedEvidence"`
	}
	_ = json.Unmarshal(input, &first)
	notes := []map[string]any{}
	for _, id := range first.Info.Documents {
		notes = append(notes, map[string]any{"documentId": id, "finding": "Fixture only", "limitations": "Not a scientific result", "references": []string{}})
	}
	d.AIExecutions = append(d.AIExecutions, AIExecution{ID: "accepted-batch", WorkflowStepID: "screen", Attempt: 3, Status: "completed", Output: mustJSON(map[string]any{"documentAnalyses": notes})})
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.selected_evidence_batch", Payload: mustJSON(map[string]any{"executionId": "accepted-batch", "sourceKey": first.Key})})
	findStepByNode(d.Steps, node.ID).Attempt = 3
	for _, change := range []string{"none", "same-review-new-generation", "corrections", "prior-subject", "research-goal", "search", "index", "user-revision"} {
		t.Run(change, func(t *testing.T) {
			var next RunDetail
			_ = json.Unmarshal(mustJSON(d), &next)
			n := node
			r := revision
			switch change {
			case "same-review-new-generation":
				r.ProducerAttempt, r.NextProducerAttempt = 3, 4
			case "corrections":
				r.IndependentReview = raw(`{"requiredCorrections":["Another independently confirmed correction"],"revisionPlan":[{"correctionIndex":0,"nodeId":"evidence_screening"}]}`)
				r.ReviewOutputSHA256 = hashJSON(r.IndependentReview)
			case "prior-subject":
				r.PriorSubject = raw(`{"summary":"A different frozen synthesis"}`)
				r.PriorSubjectSHA256 = hashJSON(r.PriorSubject)
			case "research-goal":
				v := decodeObject(n.Arguments)
				v["originalRequest"] = "A newly confirmed research question"
				n.Arguments = mustJSON(v)
			case "search", "index":
				id := "evidence_search"
				if change == "index" {
					id = "evidence_sync"
				}
				s := findStepByNode(next.Steps, id)
				v := decodeObject(s.Output)
				v["sourceVersion"] = "A new frozen source dependency"
				s.Output = mustJSON(v)
			case "user-revision":
				next.Events = append(next.Events, RuntimeEvent{Type: "workflow.user_revision_queued", Payload: raw(`{}`)})
			}
			if change == "same-review-new-generation" || change == "corrections" || change == "prior-subject" {
				next.Events = append(next.Events, RuntimeEvent{Type: "workflow.review_revision_queued", Payload: mustJSON(r)})
			}
			prepared := prepareReviewEvidenceBatch(t, next, n)
			var actual struct {
				Key  string                `json:"sourceKey"`
				Info selectedEvidenceInput `json:"_selectedEvidence"`
			}
			_ = json.Unmarshal(prepared, &actual)
			if change == "none" {
				if actual.Key != first.Key {
					t.Fatal("same generation lost its accepted checkpoint")
				}
				for _, id := range actual.Info.Documents {
					if stringSetOf(first.Info.Documents)[id] {
						t.Fatal("same-generation batch scheduled accepted document again", id)
					}
				}
			} else if actual.Key == first.Key {
				t.Fatal("a different generation/research/source reused old progress")
			}
		})
	}
}

func TestReviewCorrectionDownstreamBaselineDoesNotDriftAcrossCommitGap(t *testing.T) {
	d := recommendationFixture(t)
	review, _ := outputPort(d.Steps[4].Output, "analysis")
	r := reviewRevision{ProducerStepID: d.Steps[0].ID, ProducerNodeID: "method_selection", GateStepID: d.Steps[5].ID, PriorSubject: raw(`{}`), PriorSubjectSHA256: hashJSON(raw(`{}`)), IndependentReview: review, ReviewOutputSHA256: hashJSON(review)}
	at := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	d.Events = []RuntimeEvent{{Type: "workflow.review_revision_queued", Payload: mustJSON(r), CreatedAt: at}}
	s := &d.Steps[3]
	s.Status = StepQueued
	prior := raw(`{"markdown":"Complete pre-repair report"}`)
	before, after := at.Add(-time.Minute), at.Add(time.Minute)
	d.AIExecutions = append(d.AIExecutions,
		AIExecution{ID: "prior", WorkflowStepID: s.ID, Attempt: 2, Status: "completed", Output: prior, CreatedAt: before, CompletedAt: &before},
		// Execution output can be saved before the continuation event commits.
		// Its post-confirmation completion must never become the old report.
		AIExecution{ID: "uncommitted-collection", WorkflowStepID: s.ID, Attempt: 3, Status: "completed", Output: raw(`{"summary":"Partial new material"}`), CreatedAt: before, CompletedAt: &after},
	)
	check := pendingStageReviewCorrections(d, s.NodeID)
	if check == nil || !rawJSONEqual(check.PriorOutput, prior) {
		t.Fatal("uncommitted post-confirmation output changed the prior report")
	}
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.completed", Payload: raw(`{"executionId":"uncommitted-collection"}`)})
	if pendingStageReviewCorrections(d, s.NodeID) != nil {
		t.Fatal("final replacement did not consume the current correction checklist")
	}
}

// The capture is opt-in and remains outside source control. Only the captured
// task is loaded into memory; this test never opens its DB or calls a provider.
func TestCapturedReviewEvidenceGenerationConverges(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REVIEW_BATCH_REPLAY_DIR")
	if dir == "" {
		t.Skip("no cancelled evidence-repair capture supplied")
	}
	data, err := os.ReadFile(filepath.Join(dir, "workflow-detail.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d RunDetail
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatal(err)
	}
	// The exported JSON envelope is not a SQLite row codec. Normalize that
	// envelope once before simulating reloads, so JSON HTML escaping cannot
	// masquerade as a new byte-exact search/index snapshot in the fixture.
	if err := json.Unmarshal(mustJSON(d), &d); err != nil {
		t.Fatal(err)
	}
	step := findStepByNode(d.Steps, "evidence_screening")
	if step == nil || step.Status != StepCancelled || step.Attempt < 200 {
		t.Fatal("capture is not the cancelled repeated-batch incident")
	}
	node := compilationNodeMap(d.Run.Compilation)[step.NodeID]
	step.Status, step.Input, step.InputSHA256 = StepQueued, raw(`{}`), ""
	seen, batches, reused, key := map[string]bool{}, 0, 0, ""
	for cycle := 0; cycle < 30; cycle++ {
		input := prepareReviewEvidenceBatch(t, d, node)
		var in struct {
			Key  string                `json:"sourceKey"`
			Info selectedEvidenceInput `json:"_selectedEvidence"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			t.Fatal(err)
		}
		if key == "" {
			key = in.Key
			// A correctly identified checkpoint from the start of this same
			// confirmed generation may already exist. Reuse only exact matches;
			// do not rewrite the hundreds of mismatched historical checkpoints.
			accepted := map[string]bool{}
			for _, ev := range d.Events {
				var checkpoint struct {
					ExecutionID string `json:"executionId"`
					Key         string `json:"sourceKey"`
				}
				if ev.Type == "workflow.selected_evidence_batch" && json.Unmarshal(ev.Payload, &checkpoint) == nil && checkpoint.Key == key {
					accepted[checkpoint.ExecutionID] = true
				}
			}
			for _, e := range d.AIExecutions {
				if e.WorkflowStepID != step.ID || e.Status != "completed" || !accepted[e.ID] {
					continue
				}
				var out struct {
					Documents []struct {
						ID string `json:"documentId"`
					} `json:"documentAnalyses"`
				}
				_ = json.Unmarshal(e.Output, &out)
				for _, doc := range out.Documents {
					seen[doc.ID] = true
				}
			}
			reused = len(seen)
		}
		if in.Key != key {
			t.Fatal("captured repair still changes sourceKey after a successful batch")
		}
		if in.Info.Phase == "coverage" && !in.Info.Direct {
			if len(in.Info.Analyses) != 24 || len(seen) != 24 {
				t.Fatal("capture lost document dispositions", len(seen), len(in.Info.Analyses))
			}
			t.Logf("cancelled attempt 284, 293 historical executions: %d exact-checkpoint documents reused + %d fixture batches -> coverage with all 24 documents; no duplicate work/model call/DB write", reused, batches)
			return
		}
		if in.Info.Phase != "batch" || len(in.Info.Documents) == 0 {
			t.Fatal("capture did not continue from a bounded batch")
		}
		step = findStepByNode(d.Steps, node.ID)
		step.Attempt++
		step.Status, step.Input, step.InputSHA256 = StepRunning, input, hashJSON(input)
		notes := []map[string]any{}
		for _, id := range in.Info.Documents {
			if seen[id] {
				t.Fatal("capture repeated an accepted document", id)
			}
			seen[id] = true
			notes = append(notes, map[string]any{"documentId": id, "finding": "Scheduling fixture: no scientific claim", "limitations": "Not an evaluation of the original findings", "references": []string{}})
		}
		output := mustJSON(map[string]any{"documentAnalyses": notes})
		if err := validateWorkflowAIStageOutput(selectedEvidencePhaseNode(node, input), output, input); err != nil {
			t.Fatal(err)
		}
		e := AIExecution{ID: fmt.Sprintf("capture-fixture-%d", cycle), WorkflowRunID: d.Run.ID, WorkflowStepID: step.ID, Attempt: step.Attempt, Status: "completed", Output: output, OutputSHA256: hashJSON(output)}
		repo := &literatureRepositoryFixture{}
		svc := &RuntimeService{repository: repo, now: time.Now, newID: func() (string, error) { return "capture-event", nil }}
		if err := svc.completeSelectedEvidenceBatch(context.Background(), d, *step, e); err != nil {
			t.Fatal(err)
		}
		d.AIExecutions = append(d.AIExecutions, e)
		d.Events = append(d.Events, repo.event)
		step.Status, step.Input, step.InputSHA256 = StepQueued, raw(`{}`), ""
		batches++
		if err := json.Unmarshal(mustJSON(d), &d); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("captured repair still fails to converge")
}

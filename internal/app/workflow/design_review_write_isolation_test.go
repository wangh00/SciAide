package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func reviewedDesignIsolationFixture(t *testing.T, recommended bool, upstream ...bool) (RunDetail, reviewRevision) {
	t.Helper()
	d := materialRevisionFixture(t)
	c := bindMaterialOwnershipFixture(t, &d)
	value := decodeObject(d.AIExecutions[0].Output)
	value["reviewedInputSha256"] = d.Steps[4].InputSHA256
	plan := value["revisionPlan"].([]any)[0].(map[string]any)
	plan["nodeId"], plan["editTargets"] = "method_implementation", []string{writeObjectID(t, c, "frozen_design_material")}
	target := "method_implementation"
	if len(upstream) > 0 && upstream[0] {
		target = "method_selection"
		for _, o := range c.Objects {
			if o.Kind == "stage_result" && o.OwnerNodeID == target {
				plan["nodeId"], plan["editTargets"] = target, []string{o.ID}
			}
		}
		if len(upstream) > 1 && upstream[1] {
			plan["editTargets"] = append(plan["editTargets"].([]string), writeObjectID(t, c, "frozen_design_material"))
		}
	}
	freezeRecommendationReview(t, &d, value)
	_, _, r, err := reviewRevisionFromTarget(d, d.Steps[5], target)
	if err != nil {
		t.Fatal(err)
	}
	r.UsedRecommendation = recommended
	if err := bindDesignReviewIsolation(d, &r); err != nil {
		t.Fatal(err)
	}
	if r.DesignWriteIsolation == nil {
		t.Fatal("missing private review receipt")
	}
	return d, r
}

func TestDesignReviewWriteIsolationExplicitUpstreamSelectionDoesNotResurrectOldMask(t *testing.T) {
	d, r := reviewedDesignIsolationFixture(t, true, true)
	event := RuntimeEvent{ID: "review-revision", WorkflowRunID: d.Run.ID, Type: "workflow.review_revision_queued", Payload: mustJSON(r)}
	next := d
	next.Steps = append([]Step(nil), d.Steps...)
	next.Steps[1].Attempt++
	next.Steps[1].Status = StepRunning
	next.Events = []RuntimeEvent{event}
	next.RevisionProposals = []ResearchRevisionProposal{{Status: "confirmed", RunID: d.Run.ID, NodeID: d.Steps[1].NodeID, DesignWriteIsolationVersion: designWriteIsolationVersion}}
	s := &RuntimeService{repository: &designIsolationRepository{detail: next, snapshot: mustJSON(d), reviewSnapshot: true}}
	mask, err := s.designWriteMask(context.Background(), next, next.Steps[1])
	if err != nil || mask != nil {
		t.Fatal("unselected downstream owner inherited old user mask", err)
	}
}

func TestDesignReviewWriteIsolationLegacyUsesLatestGeneration(t *testing.T) {
	_, repo, step, _, _ := designIsolationFixture(t, "frozen_design_material")
	d := repo.detail
	if !legacyReviewTouchesNarrowDesign(d, step.NodeID) {
		t.Fatal("legacy review bypassed active narrow scope")
	}
	p := d.RevisionProposals[0]
	var c revisionWriteContract
	_ = json.Unmarshal(p.WriteContract, &c)
	for _, o := range c.Objects {
		if o.Kind == "stage_result" {
			p.EditTargets = append(p.EditTargets, o.ID)
		}
	}
	d.RevisionProposals = append(d.RevisionProposals, p)
	if legacyReviewTouchesNarrowDesign(d, step.NodeID) {
		t.Fatal("historical narrow proposal overrode newer whole-stage scope")
	}
	p.DesignWriteIsolationVersion = ""
	p.WriteContract = nil
	p.EditTargets = nil
	d.RevisionProposals = append(d.RevisionProposals, p)
	if legacyReviewTouchesNarrowDesign(d, step.NodeID) {
		t.Fatal("older narrow generation survived explicit input-contract successor")
	}
}

func TestDesignReviewWriteIsolationMissingReceiptKeepsPrivateAnchorFatal(t *testing.T) {
	d, r := reviewedDesignIsolationFixture(t, true)
	r.DesignWriteIsolation = nil
	next := d
	next.Steps = append([]Step(nil), d.Steps...)
	next.Steps[1].Status = StepRunning
	next.Steps[1].Attempt++
	next.Events = []RuntimeEvent{{ID: "review-revision", WorkflowRunID: d.Run.ID, Type: "workflow.review_revision_queued", Payload: mustJSON(r)}}
	repo := &designIsolationRepository{detail: next, snapshot: mustJSON(d), reviewSnapshot: true}
	s := &RuntimeService{repository: repo}
	if _, err := s.designWriteMask(context.Background(), next, next.Steps[1]); err == nil {
		t.Fatal("new review with private anchor silently became legacy when receipt disappeared")
	}
	// A genuinely old event with no private new anchor remains legacy.
	repo.reviewSnapshot = false
	if mask, err := s.designWriteMask(context.Background(), next, next.Steps[1]); err != nil || mask != nil {
		t.Fatal("legacy event was upgraded", err)
	}
}

func TestDesignReviewWriteIsolationProjectsAdoptedOwnerNotOldAIExecution(t *testing.T) {
	d, r := reviewedDesignIsolationFixture(t, true, true, true)
	baseline, _ := outputPort(d.Steps[1].Output, "analysis")
	stale := raw(strings.Replace(string(baseline), "Material A", "Before human adoption", 1))
	d.AIExecutions = append(d.AIExecutions, AIExecution{ID: "prior-owner", WorkflowStepID: d.Steps[1].ID, Attempt: d.Steps[1].Attempt, Status: "completed", Output: stale})
	next := d
	next.Steps = append([]Step(nil), d.Steps...)
	next.Steps[1].Status = StepQueued
	next.Events = []RuntimeEvent{{ID: "review-revision", WorkflowRunID: d.Run.ID, Type: "workflow.review_revision_queued", Payload: mustJSON(r)}}
	legacy := pendingStageReviewCorrections(next, next.Steps[1].NodeID)
	if legacy == nil || !strings.Contains(string(legacy.PriorOutput), "Before human adoption") {
		t.Fatal("test did not expose stale AI prior")
	}
	s := &RuntimeService{repository: &designIsolationRepository{detail: next, snapshot: mustJSON(d), reviewSnapshot: true}}
	node := compilationNodeMap(next.Run.Compilation)[next.Steps[1].NodeID]
	input := mustJSON(map[string]any{"_reviewCorrections": legacy})
	bound, err := s.bindDesignReviewPriorOutput(context.Background(), next, node, input)
	var got struct {
		Corrections struct {
			Prior json.RawMessage `json:"priorOutput"`
		} `json:"_reviewCorrections"`
	}
	if err != nil || json.Unmarshal(bound, &got) != nil || !rawJSONEqual(got.Corrections.Prior, baseline) {
		t.Fatal("model baseline diverged from validated adopted snapshot", err)
	}
	if strings.Contains(string(bound), "Before human adoption") || strings.Contains(string(bound), `"steps":`) {
		t.Fatal("old AI prior or complete private snapshot leaked")
	}
}

func TestDesignReviewWriteIsolationManualEarlierOwnerWithoutOriginalChecklist(t *testing.T) {
	for _, target := range []string{"method_implementation", "method_selection"} {
		t.Run(target, func(t *testing.T) {
			d := materialRevisionFixture(t)
			c := bindMaterialOwnershipFixture(t, &d)
			value := decodeObject(d.AIExecutions[0].Output)
			value["reviewedInputSha256"] = d.Steps[4].InputSHA256
			plan := value["revisionPlan"].([]any)[0].(map[string]any)
			plan["nodeId"], plan["editTargets"] = "report_drafting", []string{writeObjectID(t, c, "report_authored_body")}
			freezeRecommendationReview(t, &d, value)
			_, _, r, err := reviewRevisionFromTarget(d, d.Steps[5], target)
			if err != nil {
				t.Fatal(err)
			}
			if err := bindDesignReviewIsolation(d, &r); err != nil {
				t.Fatal(err)
			}
			next := d
			next.Steps = append([]Step(nil), d.Steps...)
			next.Steps[1].Status = StepQueued
			next.Events = []RuntimeEvent{{ID: "review-revision", WorkflowRunID: d.Run.ID, Type: "workflow.review_revision_queued", Payload: mustJSON(r)}}
			if pendingStageReviewCorrections(next, next.Steps[1].NodeID) != nil {
				t.Fatal("test requires a genuinely absent original checklist")
			}
			s := &RuntimeService{repository: &designIsolationRepository{detail: next, snapshot: mustJSON(d), reviewSnapshot: true}}
			node := compilationNodeMap(next.Run.Compilation)[next.Steps[1].NodeID]
			input, err := bindNodeInput(next, node)
			if err != nil {
				t.Fatal(err)
			}
			bound, err := s.bindDesignReviewPriorOutput(context.Background(), next, node, input)
			var got struct {
				Corrections struct {
					Prior   json.RawMessage               `json:"priorOutput"`
					Items   []any                         `json:"corrections"`
					Receipt *designReviewIsolationReceipt `json:"designWriteIsolation"`
				} `json:"_reviewCorrections"`
			}
			baseline, _ := outputPort(d.Steps[1].Output, "analysis")
			if err != nil || json.Unmarshal(bound, &got) != nil || got.Corrections.Receipt == nil || !rawJSONEqual(got.Corrections.Prior, baseline) || len(got.Corrections.Items) != 0 {
				t.Fatal("manual restart was blocked or invented review findings", err)
			}
			future := next.Steps[1]
			future.Attempt++
			mask, err := s.designWriteMask(context.Background(), next, future)
			if err != nil || mask == nil || !mask.wholeStage {
				t.Fatal("manual stage access was narrowed", err)
			}
		})
	}
}

func TestDesignReviewWriteIsolationRebindsNarrowAndManualScopes(t *testing.T) {
	for _, recommended := range []bool{true, false} {
		d, r := reviewedDesignIsolationFixture(t, recommended)
		event := RuntimeEvent{ID: "review-revision", WorkflowRunID: d.Run.ID, Type: "workflow.review_revision_queued", Payload: mustJSON(r)}
		if valid, err := ValidateDesignReviewIsolationSnapshot(d, event); err != nil || !valid {
			t.Fatal(valid, err)
		}
		baseline, _ := outputPort(d.Steps[1].Output, "analysis")
		next := d
		next.Steps = append([]Step(nil), d.Steps...)
		next.Steps[1].Attempt++
		next.Steps[1].Status = StepRunning
		next.Events = []RuntimeEvent{event}
		next.Steps[1].Status = StepQueued
		checklist := pendingStageReviewCorrections(next, next.Steps[1].NodeID)
		if checklist == nil || checklist.DesignWriteIsolation == nil || !strings.Contains(checklist.DesignWriteIsolationInstruction, "supersedes the older _userRevision") {
			t.Fatal("new authority was hidden behind old user scope in model input")
		}
		next.Steps[1].Status = StepRunning
		repo := &designIsolationRepository{detail: next, snapshot: mustJSON(d), reviewSnapshot: true}
		s := &RuntimeService{repository: repo}
		mask, err := s.designWriteMask(context.Background(), next, next.Steps[1])
		if err != nil || mask == nil {
			t.Fatal(mask, err)
		}
		good := raw(strings.Replace(string(baseline), "Original material, not yet drafted.", "Actual selected material.", 1))
		if err := mask.validate(good); err != nil {
			t.Fatal(err)
		}
		bad := raw(strings.Replace(string(good), `"title":"Material A"`, `"title":"Unselected title"`, 1))
		if (mask.validate(bad) == nil) == recommended {
			t.Fatal("review scope was dropped or manual full-stage access was lost", recommended)
		}
		for _, change := range []func(*reviewRevision){
			func(r *reviewRevision) {
				r.PriorSubject = raw(`{"injected":true}`)
				r.PriorSubjectSHA256 = hashJSON(r.PriorSubject)
			},
			func(r *reviewRevision) {
				r.IndependentReview = raw(`{"injected":true}`)
				r.ReviewOutputSHA256 = hashJSON(r.IndependentReview)
			},
			func(r *reviewRevision) { r.DesignWriteIsolation.SnapshotSHA256 = "stale" },
		} {
			copy := r
			receipt := *r.DesignWriteIsolation
			copy.DesignWriteIsolation = &receipt
			change(&copy)
			badEvent := event
			badEvent.Payload = mustJSON(copy)
			if valid, err := ValidateDesignReviewIsolationSnapshot(d, badEvent); err == nil || valid {
				t.Fatal("changed review confirmation accepted")
			}
		}
		changed := d
		changed.Steps = append([]Step(nil), d.Steps...)
		changed.Steps[1].Output = raw(`{"analysis":{"changed":true}}`)
		if valid, err := ValidateDesignReviewIsolationSnapshot(changed, event); err == nil || valid {
			t.Fatal("stale output baseline accepted")
		}
		var projected map[string]any
		_ = json.Unmarshal(mustJSON(r), &projected)
		if _, exists := projected["designWriteIsolation"].(map[string]any)["snapshot"]; exists {
			t.Fatal("full private snapshot leaked into event/model projection")
		}
	}
}

package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
)

// Baselines stay in a private transactional repository, not public events,
// timeline snapshots or every model input. This receipt is safe to project.
type designReviewIsolationReceipt struct {
	Version        string   `json:"version"`
	SnapshotSHA256 string   `json:"snapshotSha256"`
	EditTargets    []string `json:"editTargets"`
}

func bindDesignReviewIsolation(d RunDetail, r *reviewRevision) error {
	if err := validateRevisionWriteSnapshot(*r); err != nil {
		return err
	}
	if len(r.WriteContract) == 0 {
		if legacyReviewTouchesNarrowDesign(d, r.ProducerNodeID) {
			return fmt.Errorf("旧审查缺少新的实际写入范围，不能重跑受窄授权保护的设计；请在同一任务重新制定方案")
		}
		return nil
	} // An existing v8 narrow scope is NOT dropped by the replay resolver.
	var c revisionWriteContract
	if json.Unmarshal(r.WriteContract, &c) != nil {
		return fmt.Errorf("审查返修写入契约无法读取")
	}
	producer := findStepByNode(d.Steps, r.ProducerNodeID)
	review := findStepByNode(d.Steps, r.ReviewNodeID)
	if producer == nil || producer.ID != r.ProducerStepID || producer.Attempt != r.ProducerAttempt || producer.Status != StepCompleted || review == nil || review.ID != r.ReviewStepID || review.Attempt != r.ReviewAttempt || hashJSON(review.Input) != review.InputSHA256 {
		return fmt.Errorf("审查返修的产出或审查身份不一致")
	}
	gate := findStep(d.Steps, r.GateStepID)
	if gate == nil {
		return fmt.Errorf("审查返修缺少真实失败门禁")
	}
	defaultProducer, _, expected, err := reviewRevisionContext(d, *gate)
	if err != nil {
		return err
	}
	if producer.ID != defaultProducer.ID {
		// The public Retry path already checked source-selection eligibility.
		// Transactional replay rebinds the actual immutable stage object instead
		// of re-running discovery that requires unrelated AI/event history.
		allowed := false
		for _, o := range c.Objects {
			if o.Kind == "stage_result" && o.StepID == producer.ID && o.OwnerNodeID == producer.NodeID && o.Attempt == producer.Attempt && o.Ordinal == producer.Ordinal && o.OutputSHA256 == hashJSON(producer.Output) {
				allowed = true
			}
		}
		if !allowed || producer.Ordinal > defaultProducer.Ordinal || !workflowNodeReaches(d.Run.Compilation, producer.NodeID, gate.NodeID) {
			return fmt.Errorf("审查返修起点未绑定允许的实际产出对象")
		}
		port := "analysis"
		if compilationNodeMap(d.Run.Compilation)[producer.NodeID].Kind == NodeCitationSelection {
			port = "citations"
		}
		prior, err := outputPort(producer.Output, port)
		if err != nil {
			return err
		}
		expected.ProducerStepID, expected.ProducerNodeID, expected.ProducerAttempt = producer.ID, producer.NodeID, producer.Attempt
		expected.NextProducerAttempt = producer.Attempt + 1
		expected.PriorSubject, expected.PriorSubjectSHA256 = cloneRaw(prior), hashJSON(prior)
	}
	expected.UsedRecommendation = r.UsedRecommendation
	if r.UsedRecommendation || compilationNodeMap(d.Run.Compilation)[producer.NodeID].Kind == NodeCitationSelection {
		full, err := outputPort(review.Output, "analysis")
		if err != nil {
			return err
		}
		expected.IndependentReview, expected.ReviewOutputSHA256 = cloneRaw(full), hashJSON(full)
	}
	actual := *r
	actual.DesignWriteIsolation = nil
	x, e1 := canonicalJSON(expected)
	y, e2 := canonicalJSON(actual)
	if e1 != nil || e2 != nil || string(x) != string(y) {
		return fmt.Errorf("审查返修事件与原产出、完整审查或门禁绑定不一致")
	}
	var input map[string]json.RawMessage
	assembled, ok := reviewAssembledInput(review.Input)
	if !ok || json.Unmarshal(assembled, &input) != nil || !rawJSONEqual(input["revisionWriteContract"], r.WriteContract) {
		return fmt.Errorf("审查返修写入契约未绑定冻结审查输入")
	}
	ids := []string{}
	if r.UsedRecommendation {
		// The ordinary Retry path has already verified the current independent
		// execution and verdict. Rebind its exact completed output here so the
		// private snapshot need not duplicate all AI prompts/execution history.
		full, err := outputPort(review.Output, "analysis")
		if err != nil || hashJSON(full) != r.ReviewOutputSHA256 || hashJSON(r.IndependentReview) != r.ReviewOutputSHA256 {
			return fmt.Errorf("审查建议未绑定完整当前审查")
		}
		if err := validateResearchRevisionPlan(r.IndependentReview, review.Input); err != nil {
			return err
		}
		var plan struct {
			Items []researchRevisionPlanItem `json:"revisionPlan"`
		}
		if json.Unmarshal(r.IndependentReview, &plan) != nil {
			return fmt.Errorf("审查建议无法读取")
		}
		for _, item := range plan.Items {
			for _, id := range item.EditTargets {
				if !slices.Contains(ids, id) {
					ids = append(ids, id)
				}
			}
		}
		if len(ids) == 0 {
			return fmt.Errorf("采用审查建议缺少实际写入对象，不能推定整个设计均可修改")
		}
	} else {
		// A manual explicit stage restart authorizes full regeneration of its
		// suffix. Do not mislabel it as the review's narrower object selection.
		for _, o := range c.Objects {
			if o.Kind == "stage_result" && o.Ordinal >= producer.Ordinal {
				ids = append(ids, o.ID)
			}
		}
	}
	// Even a new report/upstream-only selection is an explicit successor
	// authority. Distinguish its unselected downstream owners from a missing
	// legacy receipt; do not resurrect the older user's narrow mask later.
	materialRoute := false
	for _, o := range c.Objects {
		if o.Kind == "frozen_design_material" || o.Kind == "design_material_collection" {
			materialRoute = true
		}
	}
	if !materialRoute {
		return nil
	}
	r.DesignWriteIsolation = &designReviewIsolationReceipt{Version: designWriteIsolationVersion, SnapshotSHA256: ResearchRevisionSnapshot(d), EditTargets: ids}
	return nil
}

// No-contract legacy reviews can exist on an old frozen graph after a new
// discussion proposal. Only the latest applicable confirmed generation counts;
// an older narrow card must not permanently override a later explicit broad one.
func legacyReviewTouchesNarrowDesign(d RunDetail, startNode string) bool {
	start := findStepByNode(d.Steps, startNode)
	if start == nil {
		return false
	}
	owners := map[string]int{}
	for _, p := range d.RevisionProposals {
		if p.Status != "confirmed" {
			continue
		}
		var c revisionWriteContract
		_ = json.Unmarshal(p.WriteContract, &c)
		for _, o := range c.Objects {
			if (o.Kind == "frozen_design_material" || o.Kind == "design_material_collection") && o.Ordinal >= start.Ordinal {
				owners[o.OwnerNodeID] = o.Ordinal
			}
		}
	}
	for node, ordinal := range owners {
		for i := len(d.RevisionProposals) - 1; i >= 0; i-- {
			p := d.RevisionProposals[i]
			pstart := findStepByNode(d.Steps, p.NodeID)
			if p.Status != "confirmed" || pstart == nil || pstart.Ordinal > ordinal {
				continue
			}
			if p.DesignWriteIsolationVersion == "" {
				break
			}
			var c revisionWriteContract
			if json.Unmarshal(p.WriteContract, &c) != nil {
				return true
			}
			narrow, wide := false, false
			for _, o := range c.Objects {
				if o.OwnerNodeID == node && slices.Contains(p.EditTargets, o.ID) {
					if o.Kind == "stage_result" {
						wide = true
					} else if o.Kind == "frozen_design_material" || o.Kind == "design_material_collection" {
						narrow = true
					}
				}
			}
			if narrow && !wide {
				return true
			}
			break
		}
	}
	return false
}

// Called inside the SAME reset transaction with freshly loaded run/steps.
// The journal receipt cannot bind a stale owner output or broader selection.
func ValidateDesignReviewIsolationSnapshot(d RunDetail, event RuntimeEvent) (bool, error) {
	if event.Type != "workflow.review_revision_queued" {
		return false, nil
	}
	var r reviewRevision
	if json.Unmarshal(event.Payload, &r) != nil {
		return false, fmt.Errorf("审查返修事件无法读取")
	}
	if r.DesignWriteIsolation == nil {
		return false, nil
	}
	actual := r.DesignWriteIsolation
	r.DesignWriteIsolation = nil
	if event.WorkflowRunID != d.Run.ID {
		return false, fmt.Errorf("审查写隔离回执跨任务")
	}
	if err := bindDesignReviewIsolation(d, &r); err != nil {
		return false, err
	}
	expected := r.DesignWriteIsolation
	if expected == nil || actual.Version != expected.Version || actual.SnapshotSHA256 != expected.SnapshotSHA256 || !slices.Equal(actual.EditTargets, expected.EditTargets) {
		return false, fmt.Errorf("审查写隔离回执与实际重置快照不一致")
	}
	return true, nil
}

func (s *RuntimeService) designReviewWriteAuthority(ctx context.Context, d RunDetail, step Step) (*ResearchRevisionProposal, json.RawMessage, bool, error) {
	for i := len(d.Events) - 1; i >= 0; i-- {
		event := d.Events[i]
		if event.Type == "workflow.user_revision_queued" {
			var user struct {
				Start *int `json:"startOrdinal"`
			}
			if json.Unmarshal(event.Payload, &user) != nil || user.Start == nil || *user.Start <= step.Ordinal {
				break
			}
		}
		if event.Type != "workflow.review_revision_queued" {
			continue
		}
		var r reviewRevision
		if json.Unmarshal(event.Payload, &r) != nil {
			return nil, nil, false, fmt.Errorf("审查返修事件无法读取")
		}
		if r.DesignWriteIsolation == nil {
			if anchor, ok := s.repository.(interface {
				HasDesignRevisionWriteSnapshot(context.Context, string, string) (bool, error)
			}); ok {
				exists, err := anchor.HasDesignRevisionWriteSnapshot(ctx, d.Run.ID, event.ID)
				if err != nil {
					return nil, nil, true, err
				}
				if exists {
					return nil, nil, true, fmt.Errorf("新的审查原稿锚仍存在但写隔离回执丢失，不能降级为旧授权")
				}
			}
			return nil, nil, false, nil // Legacy events are checked only if a new user scope requires them.
		}
		producer := findStepByNode(d.Steps, r.ProducerNodeID)
		if producer == nil || producer.ID != r.ProducerStepID {
			return nil, nil, false, fmt.Errorf("审查返修起点无法还原")
		}
		if producer.Ordinal > step.Ordinal {
			continue
		}
		reader, ok := s.repository.(interface {
			ReadDesignRevisionWriteSnapshot(context.Context, string, string) (json.RawMessage, error)
		})
		if !ok {
			return nil, nil, true, fmt.Errorf("无法读取审查返修私有原稿快照")
		}
		raw, err := reader.ReadDesignRevisionWriteSnapshot(ctx, d.Run.ID, event.ID)
		if err != nil {
			return nil, nil, true, err
		}
		var before RunDetail
		if json.Unmarshal(raw, &before) != nil {
			return nil, nil, true, fmt.Errorf("审查返修私有快照无法读取")
		}
		if valid, err := ValidateDesignReviewIsolationSnapshot(before, event); err != nil || !valid {
			return nil, nil, true, fmt.Errorf("审查返修私有快照或回执无效：%v", err)
		}
		p := &ResearchRevisionProposal{ID: event.ID, RunID: d.Run.ID, NodeID: r.ProducerNodeID, Status: "confirmed", SnapshotSHA256: r.DesignWriteIsolation.SnapshotSHA256, EditTargets: slices.Clone(r.DesignWriteIsolation.EditTargets), WriteContract: cloneRaw(r.WriteContract), WriteContractSHA256: r.WriteContractSHA256, WriteContractVersion: r.WriteContractVersion, DesignWriteIsolationVersion: r.DesignWriteIsolation.Version}
		return p, raw, true, nil
	}
	return nil, nil, false, nil
}

// Freeze exactly the baseline the validator will use into the current stage
// input. A prior AI execution can differ from a subsequently human-adopted
// Step.Output, so it is not authority for a new review's selected material.
func (s *RuntimeService) bindDesignReviewPriorOutput(ctx context.Context, d RunDetail, node CompiledNode, input json.RawMessage) (json.RawMessage, error) {
	step := findStepByNode(d.Steps, node.ID)
	if step == nil || step.Status != StepQueued {
		return input, nil
	}
	p, _, handled, err := s.designReviewWriteAuthority(ctx, d, *step)
	if err != nil {
		return nil, writeBaselineError(err)
	}
	if !handled || p == nil {
		return input, nil
	}
	future := *step
	future.Attempt++
	mask, err := s.designWriteMask(ctx, d, future)
	if err != nil {
		return nil, writeBaselineError(err)
	}
	if mask == nil {
		return input, nil
	}
	var value map[string]json.RawMessage
	var checklist map[string]json.RawMessage
	if json.Unmarshal(input, &value) != nil || value == nil {
		return nil, writeBaselineError(fmt.Errorf("当前审查返修阶段输入无效"))
	}
	if prior, exists := value["_reviewCorrections"]; exists {
		if json.Unmarshal(prior, &checklist) != nil || checklist == nil {
			return nil, writeBaselineError(fmt.Errorf("当前审查返修上下文损坏"))
		}
	} else {
		// A manual broader restart need not be named in the original review
		// plan. Supply authority/baseline, not invented findings or corrections.
		checklist = map[string]json.RawMessage{"corrections": raw(`[]`)}
	}
	checklist["designWriteIsolation"] = mustJSON(designReviewIsolationReceipt{Version: p.DesignWriteIsolationVersion, SnapshotSHA256: p.SnapshotSHA256, EditTargets: p.EditTargets})
	checklist["designWriteIsolationInstruction"] = mustJSON(designWriteIsolationInstruction)
	checklist["priorOutput"] = cloneRaw(mask.baseline)
	checklist["priorOutputSource"] = mustJSON(map[string]any{"kind": "confirmed_review_snapshot", "eventId": p.ID, "snapshotSha256": p.SnapshotSHA256, "analysisSha256": hashJSON(mask.baseline)})
	value["_reviewCorrections"] = mustJSON(checklist)
	return mustJSON(value), nil
}

package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
)

// Discussion and independent review use the same graph-derived write objects.
// Do not pass discussion-only import/preflight targets to the review builder:
// those stages have separate explicit file/version contracts and output ports.
func discussionRevisionWriteContract(d RunDetail) (*revisionWriteContract, error) {
	nodes := compilationNodeMap(d.Run.Compilation)
	var result *revisionWriteContract
	seen := map[string]bool{}
	for _, edge := range d.Run.Compilation.Edges {
		if edge.ToPort != "review" || !isReviewGateNode(nodes[edge.ToNode]) || seen[edge.FromNode] {
			continue
		}
		seen[edge.FromNode] = true
		review := findStepByNode(d.Steps, edge.FromNode)
		if review == nil || review.Status != StepCompleted || !isIndependentReviewSchema(nodes[review.NodeID].OutputSchema) {
			continue // A genuine prerequisite repair may have no report or review.
		}
		if review.InputSHA256 == "" || hashJSON(review.Input) != review.InputSHA256 {
			return nil, fmt.Errorf("对话返修的审查冻结输入不一致")
		}
		contract, err := currentRevisionWriteContract(d, review.NodeID, review.Input, revisionPlanTargetsForReview(d, review.NodeID))
		if err != nil {
			return nil, err
		}
		if frozen := decodeObject(review.Input)["revisionWriteContract"]; frozen != nil && !rawJSONEqual(mustJSON(frozen), mustJSON(contract)) {
			return nil, fmt.Errorf("对话返修写入对象与冻结审查不一致")
		}
		if contract != nil {
			if result != nil {
				return nil, fmt.Errorf("当前任务存在多个设计材料返修来源，不能自动选择")
			}
			result = contract
		}
	}
	return result, nil
}

func separateDiscussionInputContract(d RunDetail, c ProposeResearchRevisionCommand) bool {
	switch c.NodeID {
	case "evidence_import":
		return revisionMaterialChain(d)
	case "data_preflight":
		return revisionDataChain(d)
	case "method_implementation":
		return c.SourceInputs != nil && revisionSourceChain(d)
	}
	return false
}

func bindDiscussionRevisionWrites(d RunDetail, c ProposeResearchRevisionCommand, p *ResearchRevisionProposal) error {
	contract, err := discussionRevisionWriteContract(d)
	if err != nil {
		return err
	}
	// File/source reselection retains its original explicit adoption guards.
	// Mixed input adoption and edits of existing frozen objects must be split,
	// rather than inventing an owner or weakening the original input contract.
	if separateDiscussionInputContract(d, c) {
		if len(c.EditTargets) != 0 {
			return fmt.Errorf("输入版本更新与现有写入对象修改请分别提出方案；不得混用两种确认契约")
		}
		return nil
	}
	if contract == nil {
		if len(c.EditTargets) != 0 {
			return fmt.Errorf("当前任务没有设计材料写入对象契约，不能使用未提供的 editTargets")
		}
		return nil // Preserve non-material and pre-report repair routes.
	}
	if len(c.EditTargets) > 100 {
		return fmt.Errorf("一次返修最多声明 100 个写入对象")
	}
	input := mustJSON(map[string]any{"revisionWriteContract": contract, "revisionTargets": discussionRevisionTargets(d)})
	if err := validateRevisionWriteObjects([]researchRevisionPlanItem{{NodeID: c.NodeID, EditTargets: c.EditTargets}}, input); err != nil {
		return fmt.Errorf("对话返修写入范围无效：%w；请完整读取 task.read overview 的 revisionWriteContract 后重新提出方案", err)
	}
	p.EditTargets = slices.Clone(c.EditTargets)
	p.WriteContract = mustJSON(contract)
	p.WriteContractSHA256 = hashJSON(p.WriteContract)
	p.WriteContractVersion = revisionWriteVersion
	if designIsolationRequired(contract, c.EditTargets) {
		p.DesignWriteIsolationVersion = designWriteIsolationVersion
	}
	return nil
}

// Validate again at read/confirm boundaries. A missing receipt is not upgraded
// in place, and a historical proposal never silently acquires broader scope.
func ValidateResearchRevisionWriteBinding(d RunDetail, p ResearchRevisionProposal) error {
	if p.WriteContractVersion != "" && (len(p.WriteContract) == 0 || hashJSON(p.WriteContract) != p.WriteContractSHA256) {
		return fmt.Errorf("返修写入对象记录与其 SHA-256 摘要不一致，请在同一任务重新制定方案")
	}
	c := ProposeResearchRevisionCommand{NodeID: p.NodeID, EditTargets: p.EditTargets}
	if p.SourceVersion != nil {
		c.SourceInputs = &RevisionSourceSelection{}
	}
	var expected ResearchRevisionProposal
	if err := bindDiscussionRevisionWrites(d, c, &expected); err != nil {
		return err
	}
	if expected.WriteContractVersion != p.WriteContractVersion || expected.WriteContractSHA256 != p.WriteContractSHA256 || expected.DesignWriteIsolationVersion != p.DesignWriteIsolationVersion || !rawJSONEqual(emptyContractJSON(expected.WriteContract), emptyContractJSON(p.WriteContract)) {
		return fmt.Errorf("返修方案缺少有效或当前的写入对象绑定；请在同一任务重新制定方案，原成果、资料和历史保留")
	}
	return nil
}

func emptyContractJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage("null")
	}
	return value
}

func ResearchRevisionConfirmationPayload(d RunDetail, p ResearchRevisionProposal) (map[string]any, error) {
	start := findStepByNode(d.Steps, p.NodeID)
	if start == nil || start.Status != StepCompleted {
		return nil, fmt.Errorf("返修确认事件的起点已失效")
	}
	return map[string]any{"proposal": p, "startOrdinal": start.Ordinal, "priorOutput": start.Output, "comparisonBaseline": researchComparisonBaseline(d)}, nil
}

// The repository is an internal boundary, not a public event-injection API.
// Still bind its durable replay metadata to the same actual reset: an incorrect
// ordinal would otherwise lose _userRevision in later stage input projection.
func ValidateResearchRevisionConfirmationEvent(d RunDetail, p ResearchRevisionProposal, event RuntimeEvent) error {
	if event.WorkflowRunID != d.Run.ID || event.Type != "workflow.user_revision_queued" {
		return fmt.Errorf("返修确认事件缺少同一任务的原方案")
	}
	var payload map[string]json.RawMessage
	var proposed ResearchRevisionProposal
	if json.Unmarshal(event.Payload, &payload) != nil || json.Unmarshal(payload["proposal"], &proposed) != nil {
		return fmt.Errorf("返修确认事件无法还原原方案")
	}
	p.CanConfirm, proposed.CanConfirm = false, false
	p.ConfirmationBlockedReason, proposed.ConfirmationBlockedReason = "", ""
	payload["proposal"] = mustJSON(proposed)
	expected, err := ResearchRevisionConfirmationPayload(d, p)
	if err != nil {
		return err
	}
	actualJSON, actualErr := canonicalJSON(payload)
	expectedJSON, expectedErr := canonicalJSON(expected)
	if actualErr != nil || expectedErr != nil || !bytes.Equal(actualJSON, expectedJSON) {
		return fmt.Errorf("返修确认事件与已保存的写入范围、起点或历史基线不一致")
	}
	return nil
}

// Repository lists, full detail and timeline cards share this projection.
// It changes no persisted proposal, task state, material, or history.
func ProjectResearchRevisionProposals(d RunDetail, proposals []ResearchRevisionProposal) []ResearchRevisionProposal {
	result := slices.Clone(proposals)
	for i := range result {
		p := &result[i]
		p.ConfirmationBlockedReason = ""
		if p.Status != "pending" {
			continue
		}
		var err error
		if p.SnapshotSHA256 != ResearchRevisionSnapshot(d) {
			err = fmt.Errorf("任务快照已变化，请在同一任务重新制定返修方案")
		} else {
			err = ValidateResearchRevisionWriteBinding(d, *p)
		}
		if err != nil {
			p.CanConfirm = false
			p.ConfirmationBlockedReason = err.Error()
		}
	}
	return result
}

func discussionRevisionOverview(d RunDetail) json.RawMessage {
	targets := discussionRevisionTargets(d)
	value := map[string]any{"runId": d.Run.ID, "status": d.Run.Status, "stageCount": len(d.Steps), "stages": researchDiscussionStages(d), "revisionTargets": targets, "revisionTargetCapabilities": revisionTargetCapabilities(d, targets), "snapshotSha256": ResearchRevisionSnapshot(d), "evidenceReselection": researchEvidenceReselection(d), "registeredDeliverables": d.RegisteredDeliverables, "implementationDecision": currentImplementationDecision(d), "dataBinding": currentResearchDataBinding(d)}
	contract, err := discussionRevisionWriteContract(d)
	if err != nil {
		value["revisionWriteError"] = err.Error()
	} else if contract != nil {
		value["revisionWriteContract"] = contract
		value["revisionWriteContractSha256"] = hashJSON(mustJSON(contract))
	}
	return mustJSON(value)
}

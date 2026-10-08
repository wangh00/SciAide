package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/tool"
)

const ResearchTaskReadTool = "builtin.research.task.read"
const ResearchRevisionProposeTool = "builtin.research.revision.propose"

type ResearchDiscussionBinding struct {
	ProjectID     string
	WorkflowRunID string
	UserMessageID string
}

type ResearchRevisionProposal struct {
	SourceVersion      *ResearchSourceVersion `json:"sourceVersion,omitempty"`
	DataVersion        *ResearchDataVersion   `json:"dataVersion,omitempty"`
	Materials          []RevisionMaterial     `json:"materials,omitempty"`
	ID                 string                 `json:"id"`
	RunID              string                 `json:"runId"`
	ChatRunID          string                 `json:"chatRunId"`
	UserMessageID      string                 `json:"userMessageId"`
	SourceCallID       string                 `json:"-"`
	Status             string                 `json:"status"`
	SnapshotSHA256     string                 `json:"snapshotSha256"`
	NodeID             string                 `json:"nodeId"`
	Label              string                 `json:"label"`
	Summary            string                 `json:"summary"`
	Changes            []string               `json:"changes"`
	Reason             string                 `json:"reason"`
	AffectedStages     []string               `json:"affectedStages"`
	ReusedStages       []string               `json:"reusedStages,omitempty"`
	RepeatsSideEffects bool                   `json:"repeatsSideEffects"`
	CreatedAt          time.Time              `json:"createdAt"`
	CanConfirm         bool                   `json:"canConfirm"`
}

type ProposeResearchRevisionCommand struct {
	SourceInputs  *RevisionSourceSelection `json:"sourceInputs,omitempty"`
	DataInputs    *RevisionDataSelection   `json:"dataInputs,omitempty"`
	AttachmentIDs []string                 `json:"attachmentIds,omitempty"`
	NodeID        string                   `json:"nodeId"`
	Summary       string                   `json:"summary"`
	Changes       []string                 `json:"changes"`
	Reason        string                   `json:"reason"`
}

type ConfirmResearchRevisionCommand struct {
	ProjectID  string `json:"projectId"`
	RunID      string `json:"runId"`
	ProposalID string `json:"proposalId"`
	Confirmed  bool   `json:"confirmed"`
}

// This capability is separate from Retry: delivered/rejected tasks are never
// reopened by a model call, and the user confirmation transition is atomic.
type ResearchDiscussionRepository interface {
	ResolveResearchDiscussion(context.Context, string) (ResearchDiscussionBinding, error)
	ReadResearchDiscussionRecords(context.Context, string, string, string) (json.RawMessage, error)
	SaveResearchRevisionProposal(context.Context, ResearchRevisionProposal) (ResearchRevisionProposal, error)
	ListResearchRevisionProposals(context.Context, string) ([]ResearchRevisionProposal, error)
	ConfirmResearchRevision(context.Context, ResearchRevisionProposal, RunDetail, RuntimeEvent) error
}

func ResearchRevisionSnapshot(detail RunDetail) string {
	return hashJSON(rawObject(map[string]any{"runId": detail.Run.ID, "status": detail.Run.Status,
		"inputs": detail.Run.Inputs, "compilation": detail.Run.Compilation, "outputs": detail.Run.Outputs, "steps": detail.Steps}))
}

func completedRevisionTargets(detail RunDetail) []ResearchRevisionTarget {
	if detail.Run.Status != RunCompleted || detail.Run.WorkflowPurpose == PurposeResearchStarter || len(detail.RegisteredDeliverables) != 0 {
		return nil
	}
	nodes := compilationNodeMap(detail.Run.Compilation)
	var gate, producer *Step
	for _, step := range detail.Steps {
		if isReviewGateNode(nodes[step.NodeID]) {
			value := step
			gate = &value
		}
	}
	if gate == nil || gate.Status != StepCompleted {
		return nil
	}
	for _, edge := range detail.Run.Compilation.Edges {
		if edge.ToNode == gate.NodeID && edge.ToPort == "subject" {
			producer = findStepByNode(detail.Steps, edge.FromNode)
		}
	}
	if producer == nil {
		return nil
	}
	result := researchRevisionCandidates(detail, *producer, *gate)
	for _, id := range []string{"evidence_import", "data_preflight"} {
		step := findStepByNode(detail.Steps, id)
		if step == nil || step.Status != StepCompleted || !workflowNodeReaches(detail.Run.Compilation, id, gate.NodeID) {
			continue
		}
		n := nodes[id]
		if id == "evidence_import" && !revisionMaterialChain(detail) {
			continue
		}
		if id == "data_preflight" && !revisionDataChain(detail) {
			continue
		}
		result = append(result, ResearchRevisionTarget{NodeID: id, Label: n.Name, RepeatsSideEffects: revisionSuffixSideEffects(detail, step.Ordinal)})
	}
	return result
}

// Only a verified scientific rejection can enter discussion-based repair.
// Transport failures, corrupt submissions and arbitrary failed runs cannot.
func rejectedDiscussionReview(detail RunDetail) (*Step, json.RawMessage, error) {
	if detail.Run.Status != RunFailed || detail.Run.WorkflowPurpose == PurposeResearchStarter || len(detail.RegisteredDeliverables) != 0 {
		return nil, nil, fmt.Errorf("当前任务不是可讨论返修的审查拒绝状态")
	}
	nodes := compilationNodeMap(detail.Run.Compilation)
	for i := range detail.Steps {
		gate := &detail.Steps[i]
		if gate.Status == StepFailed && gate.ErrorCode == "RESEARCH_REVIEW_NOT_APPROVED" && isReviewGateNode(nodes[gate.NodeID]) {
			_, output, err := currentRejectedReview(detail, *gate)
			return gate, output, err
		}
	}
	return nil, nil, fmt.Errorf("当前任务没有一致的独立审查拒绝记录")
}

func discussionRevisionTargets(detail RunDetail) []ResearchRevisionTarget {
	if detail.Run.Status == RunCompleted {
		return completedRevisionTargets(detail)
	}
	if _, _, err := rejectedDiscussionReview(detail); err != nil {
		return nil
	}
	result := researchRevisionTargets(detail)
	gate, _, _ := rejectedDiscussionReview(detail)
	// New materials/data remain opt-in and must follow an already supported
	// frozen route, exactly as on a delivered task.
	for _, id := range []string{"evidence_import", "data_preflight"} {
		step := findStepByNode(detail.Steps, id)
		if step == nil || step.Status != StepCompleted || step.Ordinal >= gate.Ordinal || !workflowNodeReaches(detail.Run.Compilation, id, gate.NodeID) || (id == "evidence_import" && !revisionMaterialChain(detail)) || (id == "data_preflight" && !revisionDataChain(detail)) {
			continue
		}
		result = append(result, ResearchRevisionTarget{NodeID: id, Label: compilationNodeMap(detail.Run.Compilation)[id].Name, RepeatsSideEffects: revisionSuffixSideEffects(detail, step.Ordinal)})
	}
	return result
}

func newResearchRevisionProposal(detail RunDetail, command ProposeResearchRevisionCommand) (ResearchRevisionProposal, error) {
	var target *ResearchRevisionTarget
	for _, value := range discussionRevisionTargets(detail) {
		if value.NodeID == strings.TrimSpace(command.NodeID) {
			copy := value
			target = &copy
			break
		}
	}
	if target == nil {
		return ResearchRevisionProposal{}, fmt.Errorf("当前任务不能从该阶段返修；请选择已交付或独立审查拒绝任务的可用修改起点")
	}
	command.Summary, command.Reason = strings.TrimSpace(command.Summary), strings.TrimSpace(command.Reason)
	if command.Summary == "" || len([]rune(command.Summary)) > 1000 || command.Reason == "" || len([]rune(command.Reason)) > 2000 || len(command.Changes) == 0 || len(command.Changes) > 20 {
		return ResearchRevisionProposal{}, fmt.Errorf("返修方案需要完整的修改目标、修改要求和起点依据")
	}
	for i := range command.Changes {
		command.Changes[i] = strings.TrimSpace(command.Changes[i])
		if command.Changes[i] == "" || len([]rune(command.Changes[i])) > 2000 {
			return ResearchRevisionProposal{}, fmt.Errorf("每项修改要求须为 1 至 2000 字")
		}
	}
	proposal := ResearchRevisionProposal{RunID: detail.Run.ID, Status: "pending", SnapshotSHA256: ResearchRevisionSnapshot(detail),
		NodeID: target.NodeID, Label: target.Label, Summary: command.Summary, Changes: command.Changes, Reason: command.Reason}
	start := findStepByNode(detail.Steps, target.NodeID)
	stages := append([]Step(nil), detail.Steps...)
	sort.Slice(stages, func(i, j int) bool { return stages[i].Ordinal < stages[j].Ordinal })
	nodes := compilationNodeMap(detail.Run.Compilation)
	for _, step := range stages {
		if step.Ordinal < start.Ordinal && step.Status == StepCompleted {
			proposal.ReusedStages = append(proposal.ReusedStages, nonEmptyMessage(nodes[step.NodeID].Name, step.NodeID))
		}
		if step.Ordinal >= start.Ordinal {
			proposal.AffectedStages = append(proposal.AffectedStages, nonEmptyMessage(nodes[step.NodeID].Name, step.NodeID))
			proposal.RepeatsSideEffects = proposal.RepeatsSideEffects || nodes[step.NodeID].SideEffect
		}
	}
	return proposal, nil
}

func (s *RuntimeService) ProposeResearchRevision(ctx context.Context, chatRunID, callID string, command ProposeResearchRevisionCommand) (ResearchRevisionProposal, error) {
	repository, ok := s.repository.(ResearchDiscussionRepository)
	if !ok {
		return ResearchRevisionProposal{}, fmt.Errorf("research discussion storage is not configured")
	}
	binding, err := repository.ResolveResearchDiscussion(ctx, chatRunID)
	if err != nil {
		return ResearchRevisionProposal{}, err
	}
	detail, err := s.repository.GetRun(ctx, binding.ProjectID, binding.WorkflowRunID)
	if err != nil {
		return ResearchRevisionProposal{}, err
	}
	if err := validateDiscussionDelivery(detail); err != nil {
		return ResearchRevisionProposal{}, err
	}
	proposal, err := newResearchRevisionProposal(detail, command)
	if err != nil {
		return proposal, err
	}
	if err := validateRevisionMaterialSelection(command); err != nil {
		return proposal, err
	}
	proposal.Materials, err = s.prepareRevisionMaterials(ctx, detail, command)
	if err != nil {
		return proposal, err
	}
	proposal.DataVersion, err = s.prepareRevisionData(ctx, detail, command)
	if err != nil {
		return proposal, err
	}
	proposal.SourceVersion, err = s.prepareRevisionSource(ctx, detail, command)
	if err != nil {
		return proposal, err
	}
	proposal.ID, err = s.newID()
	if err != nil {
		return proposal, err
	}
	proposal.ChatRunID, proposal.UserMessageID, proposal.SourceCallID, proposal.CreatedAt = chatRunID, binding.UserMessageID, callID, s.now()
	return repository.SaveResearchRevisionProposal(ctx, proposal)
}

func (s *RuntimeService) ListResearchRevisionProposals(ctx context.Context, projectID, runID string) ([]ResearchRevisionProposal, error) {
	owner, err := s.repository.ProjectIDForWorkflowRun(ctx, runID)
	if err != nil || owner != strings.TrimSpace(projectID) {
		return nil, fmt.Errorf("当前项目不存在该科研任务")
	}
	repository, ok := s.repository.(ResearchDiscussionRepository)
	if !ok {
		return []ResearchRevisionProposal{}, nil
	}
	return repository.ListResearchRevisionProposals(ctx, runID)
}

func (s *RuntimeService) ConfirmResearchRevision(ctx context.Context, command ConfirmResearchRevisionCommand) (RunDetail, error) {
	if !command.Confirmed {
		return RunDetail{}, fmt.Errorf("请明确确认最终返修方案后再执行")
	}
	repository, ok := s.repository.(ResearchDiscussionRepository)
	if !ok {
		return RunDetail{}, fmt.Errorf("research discussion storage is not configured")
	}
	detail, err := s.Get(ctx, command.ProjectID, command.RunID)
	if err != nil {
		return RunDetail{}, err
	}
	if _, err := s.validateRunSnapshots(ctx, detail); err != nil {
		return RunDetail{}, err
	}
	if err := validateDiscussionDelivery(detail); err != nil {
		return RunDetail{}, err
	}
	var proposal *ResearchRevisionProposal
	for _, item := range detail.RevisionProposals {
		if item.ID == command.ProposalID && item.Status == "pending" && item.CanConfirm {
			value := item
			proposal = &value
			break
		}
	}
	if proposal == nil || proposal.SnapshotSHA256 != ResearchRevisionSnapshot(detail) {
		return RunDetail{}, fmt.Errorf("返修方案已失效或对话尚未完成，请重新确认最新方案")
	}
	verified, err := newResearchRevisionProposal(detail, ProposeResearchRevisionCommand{NodeID: proposal.NodeID, Summary: proposal.Summary, Changes: proposal.Changes, Reason: proposal.Reason})
	if err != nil {
		return RunDetail{}, err
	}
	if !slices.Equal(verified.AffectedStages, proposal.AffectedStages) || verified.RepeatsSideEffects != proposal.RepeatsSideEffects {
		return RunDetail{}, fmt.Errorf("返修影响范围已变化，请重新生成方案")
	}
	if err := s.validateRevisionMaterials(ctx, detail, *proposal); err != nil {
		return RunDetail{}, err
	}
	if err := s.validateRevisionData(ctx, detail, *proposal); err != nil {
		return RunDetail{}, err
	}
	if err := s.validateRevisionSource(ctx, detail, *proposal); err != nil {
		return RunDetail{}, err
	}
	start := findStepByNode(detail.Steps, proposal.NodeID)
	event, err := s.event(detail.Run.ID, "workflow.user_revision_queued", map[string]any{"proposal": proposal, "startOrdinal": start.Ordinal, "priorOutput": start.Output, "comparisonBaseline": researchComparisonBaseline(detail)}, s.now())
	if err != nil {
		return RunDetail{}, err
	}
	if err := repository.ConfirmResearchRevision(ctx, *proposal, detail, event); err != nil {
		return RunDetail{}, err
	}
	s.launch(detail.Run.ID)
	return s.Get(ctx, command.ProjectID, command.RunID)
}

type ResearchTaskReadCommand struct {
	RevisionID   string `json:"revisionId,omitempty"`
	AttachmentID string `json:"attachmentId,omitempty"`
	Section      string `json:"section"`
	NodeID       string `json:"nodeId,omitempty"`
	Offset       int    `json:"offset,omitempty"`
}

func (s *RuntimeService) ReadResearchTask(ctx context.Context, chatRunID string, command ResearchTaskReadCommand) (json.RawMessage, error) {
	repository, ok := s.repository.(ResearchDiscussionRepository)
	if !ok {
		return nil, fmt.Errorf("research discussion storage is not configured")
	}
	binding, err := repository.ResolveResearchDiscussion(ctx, chatRunID)
	if err != nil {
		return s.readResearchStageTask(ctx, chatRunID, command)
	}
	detail, err := s.repository.GetRun(ctx, binding.ProjectID, binding.WorkflowRunID)
	if err != nil {
		return nil, err
	}
	return s.readResearchTaskDetail(ctx, detail, command)
}

// Both discussion and stage execution read the same exact task records. Only
// the durable binding differs; revision proposals still use discussion alone.
func (s *RuntimeService) readResearchTaskDetail(ctx context.Context, detail RunDetail, command ResearchTaskReadCommand) (json.RawMessage, error) {
	repository, ok := s.repository.(ResearchDiscussionRepository)
	if !ok {
		return nil, fmt.Errorf("research task record storage is not configured")
	}
	var value json.RawMessage
	var err error
	switch command.Section {
	case "overview":
		value = rawObject(map[string]any{"runId": detail.Run.ID, "status": detail.Run.Status, "stageCount": len(detail.Steps), "stages": researchDiscussionStages(detail), "revisionTargets": discussionRevisionTargets(detail), "evidenceReselection": researchEvidenceReselection(detail), "registeredDeliverables": detail.RegisteredDeliverables})
	case "inputs":
		value = detail.Run.Inputs
	case "effective_inputs":
		value, err = ResearchEffectiveInputs(detail)
	case "data_inputs":
		value, err = researchDataInputsRecord(detail)
	case "source_inputs", "source_content":
		value, err = s.researchSourceRecord(ctx, detail, command)
	case "supplemental_materials", "supplemental_content":
		repo, ok := s.repository.(discussionMaterialsRepository)
		if !ok {
			return nil, fmt.Errorf("discussion material storage unavailable")
		}
		ids, readErr := repo.DiscussionAttachmentIDs(ctx, detail.Run.ID)
		if readErr != nil {
			return nil, readErr
		}
		if command.Section == "supplemental_content" {
			if !slices.Contains(ids, command.AttachmentID) {
				return nil, fmt.Errorf("该资料未在当前任务的用户对话中发送")
			}
			reader, ok := s.materialLoader.(interface {
				ReadDiscussionMaterial(context.Context, string, string, string) (json.RawMessage, error)
			})
			if !ok {
				return nil, fmt.Errorf("supplemental material reader unavailable")
			}
			value, err = reader.ReadDiscussionMaterial(ctx, detail.Run.ProjectID, detail.Run.ConversationID, command.AttachmentID)
		} else {
			value = rawObject(map[string]any{"attachmentIds": ids, "instruction": "These files were sent in user discussion, not automatically adopted. Read supplemental_content with attachmentId and offset before proposing. Literature uses attachmentIds and evidence_import. Tables use dataInputs (complete retainedPaths + attachmentIds) and data_preflight. Read data_inputs to identify the current data set. Single Python source uses sourceInputs.attachmentIds and method_implementation (or data_preflight with new tables); inspect dependencies and adapt, never execute the upload itself. source_inputs identifies the current confirmed reference; source_content reads it after confirmation. Only explicit user confirmation adopts a selection."})
		}
	case "outputs":
		value = detail.Run.Outputs
	case "revision_index":
		value, err = repository.ReadResearchDiscussionRecords(ctx, detail.Run.ID, command.Section, "")
	case "revision_stage_input", "revision_stage_output", "revision_inputs", "revision_source_inputs", "revision_artifacts":
		reader, ok := s.repository.(interface {
			ReadResearchRevisionSnapshot(context.Context, string, string) (json.RawMessage, error)
		})
		if !ok {
			return nil, fmt.Errorf("research revision reader unavailable")
		}
		var snapshot json.RawMessage
		snapshot, err = reader.ReadResearchRevisionSnapshot(ctx, detail.Run.ID, command.RevisionID)
		if err == nil {
			value, err = researchRevisionRecord(snapshot, command)
		}
	case "artifacts":
		value = rawObject(map[string]any{"artifacts": researchArtifactInventory(detail), "instruction": "Current completed-stage artifacts only. Use exact task workspace paths with existing read tools; binary content may require an appropriate reader. For older versions read revision_index then revision_artifacts with revisionId. This inventory does not prove files still exist or grant new permissions."})
	case "stage_input", "stage_output":
		step := findStepByNode(detail.Steps, command.NodeID)
		if step == nil {
			return nil, fmt.Errorf("请使用当前任务概况中的 nodeId")
		}
		if command.Section == "stage_input" {
			value = step.Input
		} else {
			value = step.Output
		}
	case "messages", "tool_calls", "attempts", "revisions":
		value, err = repository.ReadResearchDiscussionRecords(ctx, detail.Run.ID, command.Section, command.NodeID)
	default:
		return nil, fmt.Errorf("unknown task record section")
	}
	if err != nil {
		return nil, err
	}
	runes := []rune(string(value))
	if command.Offset < 0 || command.Offset > len(runes) {
		return nil, fmt.Errorf("任务记录分页位置已变化，请从 offset=0 重新读取")
	}
	return researchDiscussionPage(value, command), nil
}

func researchDiscussionPage(value json.RawMessage, command ResearchTaskReadCommand) json.RawMessage {
	runes := []rune(string(value))
	end := min(command.Offset+6000, len(runes))
	for {
		page := rawObject(map[string]any{"section": command.Section, "nodeId": command.NodeID, "revisionId": command.RevisionID, "offset": command.Offset, "content": string(runes[command.Offset:end]), "nextOffset": end, "totalCharacters": len(runes), "hasMore": end < len(runes), "sha256": hashJSON(value)})
		// Leave room for the generic tool-result envelope and JSON escaping.
		if len([]rune(string(page))) < tool.MaxModelContextRunes-1000 || end-command.Offset <= 1 {
			return page
		}
		end = command.Offset + (end-command.Offset)/2
	}
}

func researchDiscussionStages(detail RunDetail) []map[string]any {
	nodes := compilationNodeMap(detail.Run.Compilation)
	steps := append([]Step(nil), detail.Steps...)
	sort.Slice(steps, func(i, j int) bool { return steps[i].Ordinal < steps[j].Ordinal })
	result := make([]map[string]any, 0, len(steps))
	for _, step := range steps {
		result = append(result, map[string]any{"nodeId": step.NodeID, "name": nodes[step.NodeID].Name, "status": step.Status, "attempt": step.Attempt, "ordinal": step.Ordinal, "error": step.ErrorMessage})
	}
	return result
}

func pendingUserRevision(detail RunDetail, nodeID string) *ResearchRevisionProposal {
	step := findStepByNode(detail.Steps, nodeID)
	if step == nil {
		return nil
	}
	for i := len(detail.Events) - 1; i >= 0; i-- {
		if detail.Events[i].Type != "workflow.user_revision_queued" {
			continue
		}
		var saved struct {
			Proposal     ResearchRevisionProposal `json:"proposal"`
			StartOrdinal int                      `json:"startOrdinal"`
		}
		if json.Unmarshal(detail.Events[i].Payload, &saved) != nil {
			return nil
		}
		if step.Ordinal >= saved.StartOrdinal {
			return &saved.Proposal
		}
		return nil
	}
	return nil
}

func validateDiscussionDelivery(detail RunDetail) error {
	if detail.Run.Status == RunFailed {
		_, _, err := rejectedDiscussionReview(detail)
		return err
	}
	for _, name := range []string{"report_draft", "research_design", "interpretation"} {
		if _, found := researchOutputDeclaration(detail.Run.Compilation, name); found {
			return ValidateReviewedOutput(detail, name)
		}
	}
	return fmt.Errorf("当前任务没有可返修的已审查交付物")
}

func priorUserRevisionOutput(detail RunDetail, nodeID string) json.RawMessage {
	for i := len(detail.Events) - 1; i >= 0; i-- {
		if detail.Events[i].Type != "workflow.user_revision_queued" {
			continue
		}
		var saved struct {
			Proposal    ResearchRevisionProposal `json:"proposal"`
			PriorOutput json.RawMessage          `json:"priorOutput"`
		}
		if json.Unmarshal(detail.Events[i].Payload, &saved) == nil && saved.Proposal.NodeID == nodeID {
			return saved.PriorOutput
		}
		break
	}
	return nil
}

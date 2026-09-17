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
	ID                 string    `json:"id"`
	RunID              string    `json:"runId"`
	ChatRunID          string    `json:"chatRunId"`
	UserMessageID      string    `json:"userMessageId"`
	SourceCallID       string    `json:"-"`
	Status             string    `json:"status"`
	SnapshotSHA256     string    `json:"snapshotSha256"`
	NodeID             string    `json:"nodeId"`
	Label              string    `json:"label"`
	Summary            string    `json:"summary"`
	Changes            []string  `json:"changes"`
	Reason             string    `json:"reason"`
	AffectedStages     []string  `json:"affectedStages"`
	RepeatsSideEffects bool      `json:"repeatsSideEffects"`
	CreatedAt          time.Time `json:"createdAt"`
	CanConfirm         bool      `json:"canConfirm"`
}

type ProposeResearchRevisionCommand struct {
	NodeID  string   `json:"nodeId"`
	Summary string   `json:"summary"`
	Changes []string `json:"changes"`
	Reason  string   `json:"reason"`
}

type ConfirmResearchRevisionCommand struct {
	ProjectID  string `json:"projectId"`
	RunID      string `json:"runId"`
	ProposalID string `json:"proposalId"`
	Confirmed  bool   `json:"confirmed"`
}

// This capability is separate from Retry: completed deliveries are never
// reopened by a model call, and the storage transition must be atomic.
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
	return researchRevisionCandidates(detail, *producer, *gate)
}

func newResearchRevisionProposal(detail RunDetail, command ProposeResearchRevisionCommand) (ResearchRevisionProposal, error) {
	var target *ResearchRevisionTarget
	for _, value := range completedRevisionTargets(detail) {
		if value.NodeID == strings.TrimSpace(command.NodeID) {
			copy := value
			target = &copy
			break
		}
	}
	if target == nil {
		return ResearchRevisionProposal{}, fmt.Errorf("当前任务不能从该阶段返修；仅支持已交付但尚未登记的当前任务")
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
	start := findStepByNode(detail.Steps, proposal.NodeID)
	event, err := s.event(detail.Run.ID, "workflow.user_revision_queued", map[string]any{"proposal": proposal, "startOrdinal": start.Ordinal, "priorOutput": start.Output}, s.now())
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
	Section string `json:"section"`
	NodeID  string `json:"nodeId,omitempty"`
	Offset  int    `json:"offset,omitempty"`
}

func (s *RuntimeService) ReadResearchTask(ctx context.Context, chatRunID string, command ResearchTaskReadCommand) (json.RawMessage, error) {
	repository, ok := s.repository.(ResearchDiscussionRepository)
	if !ok {
		return nil, fmt.Errorf("research discussion storage is not configured")
	}
	binding, err := repository.ResolveResearchDiscussion(ctx, chatRunID)
	if err != nil {
		return nil, err
	}
	detail, err := s.repository.GetRun(ctx, binding.ProjectID, binding.WorkflowRunID)
	if err != nil {
		return nil, err
	}
	var value json.RawMessage
	switch command.Section {
	case "overview":
		value = rawObject(map[string]any{"runId": detail.Run.ID, "status": detail.Run.Status, "stageCount": len(detail.Steps), "stages": researchDiscussionStages(detail), "revisionTargets": completedRevisionTargets(detail), "registeredDeliverables": detail.RegisteredDeliverables})
	case "inputs":
		value = detail.Run.Inputs
	case "outputs":
		value = detail.Run.Outputs
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
		page := rawObject(map[string]any{"section": command.Section, "nodeId": command.NodeID, "offset": command.Offset, "content": string(runes[command.Offset:end]), "nextOffset": end, "totalCharacters": len(runes), "hasMore": end < len(runes), "sha256": hashJSON(value)})
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
	for _, name := range []string{"report_draft", "research_design"} {
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

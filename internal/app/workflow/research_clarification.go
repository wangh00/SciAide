package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/conversation"
)

func validateClarificationSelection(question ResearchClarificationQuestion) error {
	// Upload commands are not decisions. Reject rather than silently discard
	// them so the normal bounded output-repair path preserves the audit.
	if question.Kind == "research_direction" {
		for _, option := range question.Options {
			label := strings.ToLower(strings.TrimSpace(option.Label))
			for _, command := range []string{"立即上传", "现在上传", "稍后上传", "稍后再上传", "暂不上传", "暂缓上传", "upload now", "upload later"} {
				if strings.HasPrefix(label, command) {
					return fmt.Errorf("澄清问题 %q 将上传操作当作研究方向；请移除上传问答，将文件需求写入路线 requiredResources/blockers，用户采纳路线后由宿主选择数据文件；其他真正影响研究方向的问题应保留", question.ID)
				}
			}
		}
	}
	mode := question.SelectionMode
	if mode != "" && mode != "single" && mode != "multiple" {
		return fmt.Errorf("澄清问题 %q 的 selectionMode 必须为 single 或 multiple", question.ID)
	}
	text := strings.ToLower(question.Text)
	mentionsMultiple := strings.Contains(text, "多选") || strings.Contains(text, "select multiple") || strings.Contains(text, "select all") || strings.Contains(text, "multi-select")
	mentionsSingle := strings.Contains(text, "单选") || strings.Contains(text, "select one") || strings.Contains(text, "single choice")
	if mode != "multiple" && mentionsMultiple || mode == "multiple" && mentionsSingle {
		return fmt.Errorf("澄清问题 %q 的选择说明与 selectionMode 不一致；请重新规划，用结构化选择模式，勿在题目中声明冲突的单选/多选", question.ID)
	}
	// Explicitly labelled groups are a reproducible signal of a compound
	// question. Do not guess botanical metrics or infer arbitrary prose meaning.
	groups := map[string]int{}
	for _, option := range question.Options {
		label := strings.TrimSpace(option.Label)
		if i := strings.IndexAny(label, ":："); i > 0 {
			prefix := strings.TrimSpace(label[:i])
			if len([]rune(prefix)) <= 24 {
				groups[prefix]++
			}
		}
	}
	repeated := 0
	for _, count := range groups {
		if count >= 2 {
			repeated++
		}
	}
	if repeated >= 2 {
		return fmt.Errorf("澄清问题 %q 混合了多个独立维度；请将各组拆成独立问题，互斥方案用 single，可同时成立的指标用 multiple", question.ID)
	}
	return nil
}

type ReplanResearchStarterCommand struct {
	ProjectID    string `json:"projectId"`
	StarterRunID string `json:"starterRunId"`
}

// Replan preserves the original immutable attempt and task ownership. It is
// also the escape hatch for historical malformed clarification choices;
// those choices must never be silently repaired into user decisions.
func (s *StarterService) Replan(ctx context.Context, command ReplanResearchStarterCommand) (RunDetail, error) {
	s.adoptMu.Lock()
	defer s.adoptMu.Unlock()
	prior, err := s.runtime.Get(ctx, strings.TrimSpace(command.ProjectID), strings.TrimSpace(command.StarterRunID))
	if err != nil {
		return RunDetail{}, err
	}
	if prior.Run.WorkflowPurpose != PurposeResearchStarter || prior.Run.Status != RunCompleted {
		return RunDetail{}, fmt.Errorf("只能重新规划已完成的研究启动任务")
	}
	starter, _, execution, err := decodeStarterResult(prior)
	if err != nil {
		return RunDetail{}, err
	}
	taskID, err := s.runtime.resolveResearchTaskForRun(ctx, prior.Run.ProjectID, prior.Run)
	if err != nil {
		return RunDetail{}, err
	}
	snapshot, err := s.snapshotForTask(ctx, prior.Run.ProjectID, taskID)
	if err != nil {
		return RunDetail{}, err
	}
	return s.startReplannedResearch(ctx, prior, execution, starter.ResearchIdea, snapshot, nil, "refresh")
}

func (s *StarterService) startReplannedResearch(ctx context.Context, prior RunDetail, execution *AIExecution, idea string, snapshot ResourceSnapshot, answers map[string][]string, reason string) (RunDetail, error) {
	if _, err := s.runtime.resolveResearchTaskForRun(ctx, prior.Run.ProjectID, prior.Run); err != nil {
		return RunDetail{}, err
	}
	if code, err := s.runtime.validateRunSnapshots(ctx, prior); err != nil {
		return RunDetail{}, fmt.Errorf("%s: %w", code, err)
	}
	inputs, err := json.Marshal(map[string]any{"starter_context": ResearchStarterContext{
		ResearchIdea: idea, ResourceSnapshot: snapshot, StageCatalog: dynamicResearchStageCatalog(), PlannerVersion: dynamicResearchPlannerVersion,
		ClarificationAnswers: answers, PriorStarterRunID: prior.Run.ID,
	}})
	if err != nil {
		return RunDetail{}, err
	}
	template := ResearchStarterTemplate()
	// Include resource state and planner version, not just answers: after an
	// upload or application upgrade an old idempotency key must not return a
	// stale no-data planner result.
	key := "research-replan:" + prior.Run.ID + ":" + reason + ":" + template.Definition.Nodes[0].PromptVersion + ":" + hashJSON(inputs)
	if existing, found, err := s.runtime.repository.GetRunByCreationKey(ctx, prior.Run.ProjectID, key); err != nil {
		return RunDetail{}, err
	} else if found {
		return s.runtime.projectDetail(ctx, existing), nil
	}
	if err := s.ensureUnadoptedCurrentStarter(ctx, prior); err != nil {
		return RunDetail{}, err
	}
	saved, err := s.workflows.save(ctx, SaveCommand{ProjectID: prior.Run.ProjectID, Definition: template.Definition}, PurposeResearchStarter)
	if err != nil {
		return RunDetail{}, err
	}
	return s.runtime.Start(ctx, StartCommand{
		ProjectID: prior.Run.ProjectID, ResearchTaskID: prior.Run.ResearchTaskID, WorkflowID: saved.Workflow.ID, WorkflowVersionID: saved.Version.ID,
		Inputs: inputs, PermissionMode: conversation.PermissionFullAccess,
		ModelProfileID: execution.ModelProfileID, ModelID: execution.ModelID, ReasoningLevel: execution.ReasoningLevel, CreationKey: key,
	})
}

func (s *StarterService) ensureUnadoptedCurrentStarter(ctx context.Context, prior RunDetail) error {
	for _, event := range prior.Events {
		if event.Type == "research.route_adopted" {
			return fmt.Errorf("此科研任务已经采纳路线，请继续已采纳的任务；如需另一研究分支，请新建科研任务")
		}
	}
	return s.ensureCurrentStarter(ctx, prior)
}

func (s *StarterService) ensureCurrentStarter(ctx context.Context, prior RunDetail) error {
	reader, ok := s.runtime.repository.(interface {
		GetLatestTaskRun(context.Context, string, string) (Run, error)
	})
	if !ok {
		return fmt.Errorf("无法核对科研任务当前规划，请更新存储适配器")
	}
	latest, err := reader.GetLatestTaskRun(ctx, prior.Run.ProjectID, prior.Run.ResearchTaskID)
	if err != nil {
		return err
	}
	if latest.ID != prior.Run.ID {
		return fmt.Errorf("此规划已有后续任务记录，请打开当前科研任务，不要从旧规划重复创建执行分支")
	}
	return nil
}

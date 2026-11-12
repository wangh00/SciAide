package agent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
)

type plannerRecovery struct {
	repository chat.PlannerCheckpointRepository
	state      chat.PlannerCheckpoint
}

func (p *plannerRecovery) save(ctx context.Context, v chat.PlannerCheckpoint) error {
	if v.Candidate != p.state.Candidate {
		v.CandidateTurnIndex, v.CandidateSHA256 = 0, ""
		if v.Candidate != "" {
			v.CandidateTurnIndex = v.TurnIndex
			v.CandidateSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(v.Candidate)))
		}
	}
	if err := p.repository.SavePlannerCheckpoint(ctx, v); err != nil {
		return plannerCheckpointError("WORKFLOW_PLANNER_CHECKPOINT_SAVE_FAILED", err)
	}
	p.state = v
	return nil
}

func plannerCheckpointError(code string, cause error) error {
	return &apperr.Error{Code: code, UserMessage: "研究规划的纠错状态无法安全续接，已停止且不会重新计算纠错额度；原资料、已完成阶段和历史记录仍保留，可显式重试当前阶段。", Cause: cause}
}

// Read before processing any pending approved tools. Missing legacy state is
// not proof that zero corrections were spent; never guess from journal text.
func (l *Loop) loadPlannerCheckpoint(ctx context.Context, run chat.Run, contract chat.WorkflowAIExecution, found bool) (*plannerRecovery, error) {
	if !found || !workflow.IsResearchPlannerSchema(contract.OutputSchema) {
		return nil, nil
	}
	repository, ok := l.runs.(chat.PlannerCheckpointRepository)
	if !ok {
		return nil, plannerCheckpointError("WORKFLOW_PLANNER_CHECKPOINT_UNAVAILABLE", nil)
	}
	v, exists, err := repository.PlannerCheckpoint(ctx, run.ID)
	if err != nil {
		return nil, plannerCheckpointError("WORKFLOW_PLANNER_CHECKPOINT_LOAD_FAILED", err)
	}
	if !exists {
		if run.ModelTurns != 0 {
			return nil, plannerCheckpointError("WORKFLOW_PLANNER_CHECKPOINT_MISSING", nil)
		}
		v = chat.PlannerCheckpoint{Version: 1, RunID: run.ID, ExecutionID: contract.ID, WorkflowRunID: contract.WorkflowRunID, WorkflowStepID: contract.WorkflowStepID, Attempt: contract.Attempt,
			InputSHA256: contract.InputSHA256, PromptSHA256: contract.PromptSHA256, SchemaSHA256: contract.OutputSchemaSHA256}
	} else if !v.Matches(run.ID, contract) || chat.ValidatePlannerCheckpoint(v) != nil || v.TurnIndex != run.ModelTurns {
		return nil, plannerCheckpointError("WORKFLOW_PLANNER_CHECKPOINT_BINDING_INVALID", nil)
	} else if v.RequestInFlight {
		// A crash may occur after HTTP completion but before classification or
		// tool proposal. Refuse another paid call rather than renew a budget or
		// accept/re-execute the undecided response.
		return nil, plannerCheckpointError("WORKFLOW_PLANNER_CHECKPOINT_UNDECIDED", nil)
	}
	if err := repository.CheckPlannerCheckpointActive(ctx, run.ID, contract); err != nil {
		return nil, plannerCheckpointError("WORKFLOW_PLANNER_CHECKPOINT_INACTIVE", err)
	}
	return &plannerRecovery{repository: repository, state: v}, nil
}

const plannerSkillsNowSatisfied = "本次实际 Skill 加载记录已满足上次候选的加载声明，但上次候选仍未被接受，路线和科学结论也未获批准。请依据实际读取结果重新提交完整规划，保留所有原始格式和科学要求；不要重复已经完成的 Skill 加载。"

func boundedPlannerFeedback(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	// A very long property path can precede the important error category.
	// Preserve both ends, not just an unusable prefix of that path.
	marker := fmt.Sprintf("\n[Planner feedback truncated; feedback SHA256=%x. The complete rejected candidate remains in its original model-turn journal; original validation and correction limits still apply.]\n", sha256.Sum256([]byte(value)))
	available := limit - utf8.RuneCountInString(marker)
	if available <= 0 {
		return string([]rune(marker)[:limit])
	}
	head, tail := available/2, available-available/2
	return string(runes[:head]) + marker + string(runes[len(runes)-tail:])
}

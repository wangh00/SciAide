package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/userinput"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
)

func toolLedgerSnapshotFailure(original error) error {
	message := "上下文预算不足。"
	var budget *contextBudgetError
	if errors.As(original, &budget) {
		message = budget.userMessage()
	}
	return &apperr.Error{Code: "CONTEXT_BUILD_FAILED", UserMessage: message + " 早期调用缺少可核对的原模型快照，未启用历史窗口；原结果保留且未猜补。", Details: original.Error() + "\nTool history window: original_snapshot_unavailable", Cause: original}
}

func toolLedgerFallbackEligible(err error, readerAvailable, workflowStage, frozenContract, resourceMode bool, questionContinuation string) bool {
	if !readerAvailable || workflowStage || frozenContract || resourceMode || questionContinuation != "" {
		return false
	}
	var budget *contextBudgetError
	if !errors.As(err, &budget) || budget.Budget.Source != "character_fallback" || budget.Budget.ObservedReserve != 0 {
		return false
	}
	switch budget.Budget.Phase {
	case "completed_evidence":
		return budget.Budget.Reserved > 0
	case "tool_protocol":
		return budget.Budget.Protocol > 0 && budget.Budget.Required > budget.Budget.Effective
	}
	return false
}

func withoutToolLedgerDefinition(defs []tool.Definition) []tool.Definition {
	if !definitionVisible(defs, contextmemory.ToolLedgerReadName) {
		return defs
	}
	result := make([]tool.Definition, 0, len(defs)-1)
	for _, d := range defs {
		if d.QualifiedName != contextmemory.ToolLedgerReadName {
			result = append(result, d)
		}
	}
	return result
}

// A second LOCAL candidate after an original completed-tool history budget
// rejection. This is not another model attempt or a native-group edit.
// The caller excludes frozen stages, submissions and question continuations.
func (b *ContextBuilder) buildWithToolLedger(ctx context.Context, messages []conversation.Message, excludedID, currentID string, definitions []tool.Definition, calls []tool.Call, skills skill.RunContext, routing, system, dynamic string, limits ContextLimits, checkpoint contextmemory.Checkpoint, turns []model.ProviderTurn) (model.ChatRequest, ContextBuildInfo, error) {
	if !definitionVisible(definitions, contextmemory.ToolLedgerReadName) {
		return model.ChatRequest{}, ContextBuildInfo{}, fmt.Errorf("same-run tool history reader is not available")
	}
	ordinary := []int{}
	runID := ""
	for i, c := range calls {
		terminal := c.Status == tool.CallCompleted || c.Status == tool.CallFailed || c.Status == tool.CallDenied || c.Status == tool.CallCancelled || c.Status == tool.CallInterrupted
		if c.Result == nil || !terminal || tool.NormalizeSubjectKind(c.SubjectKind) != tool.SubjectChatRun {
			return model.ChatRequest{}, ContextBuildInfo{}, fmt.Errorf("unresolved calls cannot leave the tool window")
		}
		if c.ToolName != userinput.ToolName {
			ordinary = append(ordinary, i)
			runID = c.RunID
		}
	}
	if len(ordinary) <= contextmemory.ToolLedgerRecentCalls {
		return model.ChatRequest{}, ContextBuildInfo{}, fmt.Errorf("no older ordinary tool prefix is eligible")
	}
	// Normalized envelopes may overflow BEFORE native selection runs. Reuse
	// its whole-history pairing/order validation before leaving any groups out.
	if _, _, err := validatedProviderTurnCalls(turns, calls); err != nil {
		return model.ChatRequest{}, ContextBuildInfo{}, err
	}
	boundary := ordinary[len(ordinary)-contextmemory.ToolLedgerRecentCalls-1]
	ledger, err := contextmemory.BuildToolLedger(calls, runID, calls[boundary].ID)
	if err != nil {
		return model.ChatRequest{}, ContextBuildInfo{}, err
	}
	retained := make([]tool.Call, 0, contextmemory.ToolLedgerRecentCalls)
	for i, c := range calls {
		if c.RunID != runID {
			return model.ChatRequest{}, ContextBuildInfo{}, fmt.Errorf("tool window contains a foreign run")
		}
		if i > boundary || c.ToolName == userinput.ToolName {
			retained = append(retained, c)
		}
	}
	ref, _ := json.Marshal(ledger.Ref)
	dynamic += "\nOlder ordinary tool calls are NOT present in the live window. The latest 8 ordinary calls and ALL human answers remain below; original records are unchanged. Use builtin.context.tool_history with the exact reference below to page the archived index, then arguments/model_context of a listed callId. Saved model_context may itself be a bounded preview, not the full result or a fresh source read. Never infer unseen evidence, promote failed status, repeat an operation to recover history, or treat this transition as scientific approval.\n<tool_history_ledger>" + string(ref) + "</tool_history_ledger>"
	request, info, err := b.buildWithProtocolEvidence(ctx, messages, excludedID, currentID, definitions, retained, skills, routing, system, dynamic, limits, checkpoint, turns)
	if err == nil {
		info.BudgetSource = "independent_completed_evidence_with_tool_ledger"
	}
	return request, info, err
}

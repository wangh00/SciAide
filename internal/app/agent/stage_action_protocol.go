package agent

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
)

const stageActionInstruction = "Current stage action protocol: research tools remain optional and restricted to their frozen permissions. When you have the stage result, call submit_stage_result EXACTLY ONCE with the full result object; this is an actual completion action, not research IO. Never mix that control with research tools in one response. Do not promise submission and then repeat exploration without a concrete unresolved question. This runtime control is an alternative to the legacy fenced JSON terminal answer; both retain the same frozen validation and scientific review. Reading progress is not scientific completion."

const plannerActionInstruction = workflow.NativePlannerActionInstruction

const plannerActionContinuation = "The previous response did not submit the required planning result. Continue the SAME stage using the current native action protocol. If a real prerequisite remains, choose its offered research action, including any missing actual Skill load; otherwise call submit_stage_result exactly once with the complete frozen planning object. Earlier fenced-answer transport instructions are inactive for this request. Do not repeat completed work, emit a text/fenced answer, or promise to submit later. The original schema, scientific validation and correction limits remain unchanged."

const stageMaterialInstruction = "Current v9 material addressing: use builtin.research.task.read(section=stage_input,pointer=the exact JSON pointer,offset=0) to read one relevant material value. A $read descriptor's offset/endOffset belong ONLY to legacy whole-JSON coordinates: NEVER reuse them when sending pointer. For pointer pages follow their VALUE-relative nextOffset to totalCharacters; text is decoded Unicode, json is the original JSON value. Bind pointer+encoding+sha256+valueSha256. Reuse available evidence and the host reading ledger; fetched, previously exposed, currently visible and scientifically complete are different states. A material entry or an unread range does not make it mandatory to read everything. Choose a concrete gap or verification target, then submit the required result; do not reopen completed work without a specific reason."

func nativePlannerContract(contract chat.WorkflowAIExecution) (bool, error) {
	if workflow.IsReviewSourceProtocol(contract.PromptVersion) || workflow.IsCitationSourceProtocol(contract.PromptVersion) {
		return false, nil // Independently verified by the matching source-selection contract.
	}
	if !strings.Contains(contract.PromptVersion, "|") {
		return false, nil // Explicit legacy compatibility, not response fallback.
	}
	if !workflow.IsNativePlannerProtocol(contract.PromptVersion) || contract.NodeID != "explore" || contract.NodeKind != string(workflow.NodeAgentStage) || !workflow.IsResearchPlannerSchema(contract.OutputSchema) ||
		contract.PromptSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(contract.PromptText))) || contract.OutputSchemaSHA256 != fmt.Sprintf("%x", sha256.Sum256(contract.OutputSchema)) {
		return false, &apperr.Error{Code: "WORKFLOW_AI_PROTOCOL_INVALID", UserMessage: "科研阶段的原生调用契约或冻结身份无效，已停止，未调用模型；原资料与历史仍保留。"}
	}
	return true, nil
}

func plannerModelTurnTerminalError(err error, nativeAction bool) error {
	var appErr *apperr.Error
	if nativeAction && errors.As(err, &appErr) && appErr.Code == "MODEL_RESPONSE_EMPTY" {
		// A complete response with no required action is a protocol violation,
		// not an interrupted stream. Audit it before classification, without a
		// paid stream retry. All other terminal/error priorities stay unchanged.
		return nil
	}
	return err
}

func stageExplorationRequest(request model.ChatRequest, schema json.RawMessage) model.ChatRequest {
	request.Messages = append(cloneModelMessages(request.Messages), model.Message{Role: model.RoleSystem, Content: stageSubmissionFormatNotice + stageActionInstruction, HostToolReferences: true})
	request.Tools = append(append([]model.ToolDefinition(nil), request.Tools...), stageResultDefinition(schema))
	request.DisableTools = false
	return request
}

func plannerExplorationRequest(request model.ChatRequest, schema json.RawMessage) model.ChatRequest {
	request = stageExplorationRequest(request, schema)
	request.Messages[len(request.Messages)-1].Content = stageSubmissionFormatNotice + plannerActionInstruction
	// Require an action, not a particular result. Keep the frozen research
	// tools available so missing Skill prerequisites can still be fulfilled.
	request.ForcedTool = ""
	request.RequireTool = true
	return request
}

func withPlannerStageAction(admitted model.ChatRequest, schema json.RawMessage, observation *chat.ContextUsageObservation, limit int) (model.ChatRequest, error) {
	return fitFinalRequest(admitted, nil, observation, limit, func(r model.ChatRequest) model.ChatRequest {
		return plannerExplorationRequest(r, schema)
	})
}

func withOptionalStageAction(admitted model.ChatRequest, schema json.RawMessage, observation *chat.ContextUsageObservation, limit int) (model.ChatRequest, bool) {
	// nil calls intentionally forbids shortening any already-admitted result.
	request, err := fitFinalRequest(admitted, nil, observation, limit, func(r model.ChatRequest) model.ChatRequest {
		return stageExplorationRequest(r, schema)
	})
	if err != nil {
		return admitted, false
	}
	return request, true
}

func stageResultDefinition(schema json.RawMessage) model.ToolDefinition {
	return model.ToolDefinition{Name: stageSubmissionTool, Description: "Submit the complete result for the current frozen research stage. Arguments are the result object itself, not a wrapper or string. This control function does not execute research tools or bypass host validation.", InputSchema: append(json.RawMessage(nil), schema...)}
}

func hasStageSubmissionCall(calls []model.ToolCall) bool {
	for _, call := range calls {
		if call.Name == stageSubmissionTool {
			return true
		}
	}
	return false
}

// Native completion controls do not execute research IO and have no tool
// result. Project whole control-only exchanges out of subsequent native replay,
// including signed thinking, without changing the immutable provider journal.
// A mixed/unknown call exchange is not silently repaired here.
func withoutStageSubmissionControlTurns(turns []model.ProviderTurn) []model.ProviderTurn {
	result := make([]model.ProviderTurn, 0, len(turns))
	for _, turn := range turns {
		control, otherCall := false, false
		for _, item := range turn.Items {
			if item.Type != "function_call" && item.Type != "tool_use" {
				if item.CallID != "" {
					otherCall = true
				}
				continue
			}
			var call struct{ Name string }
			if json.Unmarshal(item.Payload, &call) == nil && call.Name == stageSubmissionTool {
				control = true
			} else {
				otherCall = true
			}
		}
		if !control || otherCall {
			result = append(result, turn)
		}
	}
	return result
}

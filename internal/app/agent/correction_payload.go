package agent

import (
	"encoding/json"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
)

// A gateway can remove the native control name. Recognize only an admitted
// whole object with root fields from the CURRENT repair schema. This decides
// transport routing, never scientific acceptance or a new correction budget.
func textCorrectionControl(text string, full, repair json.RawMessage) (string, json.RawMessage, error) {
	body, ok := stageSubmissionObject(text)
	if !ok {
		return "", nil, nil
	}
	var fields map[string]json.RawMessage
	var fullSchema, repairSchema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(full, &fullSchema); err != nil {
		return "", nil, &apperr.Error{Code: "WORKFLOW_AI_SCHEMA_INVALID", UserMessage: "当前完整提交 Schema 无法解析，已停止。", Cause: err}
	}
	if err := json.Unmarshal(repair, &repairSchema); err != nil {
		return "", nil, &apperr.Error{Code: "WORKFLOW_AI_SCHEMA_INVALID", UserMessage: "当前修正提交 Schema 无法解析，已停止。", Cause: err}
	}
	_ = json.Unmarshal(body, &fields) // stageSubmissionObject admits only valid objects.
	known, exclusive := false, false
	for key := range fields {
		if _, ok := repairSchema.Properties[key]; !ok {
			continue
		}
		known = true
		if _, shared := fullSchema.Properties[key]; !shared {
			exclusive = true
		}
	}
	if !known {
		return "", nil, nil // Unknown/full prose remains owned by the original validator.
	}
	patchErr := (tool.JSONSchemaValidator{}).Validate(repair, body)
	// Preserve the existing full-output normalization rules, including legacy
	// review wire forms. Only the live host can accept the complete result.
	_, _, fullErr := workflow.NormalizeAIStageSubmission(string(body), full)
	if fullErr != nil {
		_, _, fullErr = workflow.NormalizeAIStageSubmission(string(body), workflow.ReviewSubmissionSchema(full))
	}
	if fullErr != nil {
		// A canonical historical review may legally omit the host-owned hash,
		// even though the current model wire schema is issue-oriented. Use the
		// workflow's identity-optional shape, never fabricate an input identity.
		_, _, fullErr = workflow.NormalizeAIStageSubmission(string(body), workflow.ReviewCanonicalSubmissionPreflightSchema(full))
	}
	if fullErr == nil {
		if patchErr == nil {
			return "", nil, fmt.Errorf("correction JSON matches both submit_stage_result and submit_stage_repair; explicitly call exactly one control")
		}
		return stageSubmissionTool, body, nil
	}
	if !exclusive {
		// Shared fields alone cannot identify a partial correction. Preserve
		// their original owner, rather than guessing from property names.
		return "", nil, nil
	}
	if patchErr == nil {
		return stageRepairTool, body, nil
	}
	if _, hasBase := fields["baseSHA256"]; hasBase {
		// The existing merger can now recognize this as a patch and owns the
		// exact schema/domain error (for example quotation length/selection).
		// Do not flatten those typed diagnostics into a generic format error.
		return stageRepairTool, body, nil
	}
	// A malformed, recognizable patch is still a correction-format error. Do
	// not send it to the full validator and replace the bound rejected base.
	return stageRepairTool, body, patchErr
}

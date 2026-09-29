package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/apperr"
)

const aiSchemaInvalidCode = "WORKFLOW_AI_SCHEMA_INVALID"

// A broken host contract is not a model response that can be corrected.
func ValidateAIStageSchema(schema json.RawMessage) error {
	err := (tool.JSONSchemaValidator{}).ValidateSchema(schema)
	if err == nil {
		var root struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(schema, &root)
		if root.Type != "" && root.Type != "object" {
			err = fmt.Errorf("AI stage output schema must describe an object")
		}
	}
	if err != nil {
		return &apperr.Error{Code: aiSchemaInvalidCode, UserMessage: "程序生成的科研阶段输出契约无效，已停止；这不是模型回答错误，不会要求 AI 重试。", Details: err.Error(), Cause: err}
	}
	return nil
}

// AIStageNormalization records representation changes or explicitly named host
// provenance additions. Original text remains in the execution/message audit.
// Hashes avoid copying long scientific text into normalization events.
type AIStageNormalization struct {
	Path         string `json:"path"`
	Rule         string `json:"rule"`
	BeforeSHA256 string `json:"beforeSha256"`
	AfterSHA256  string `json:"afterSha256"`
}

// NormalizeAIStageSubmission parses the existing Workflow submission protocols,
// applies only unambiguous representation repairs, and validates the complete
// frozen JSON schema. A non-nil error always means the candidate is unaccepted;
// the returned candidate/changes may still be useful for a bounded diagnostic.
// This does not replace input-aware scientific, Skill, or citation validation.
// Callers must retain text verbatim rather than replacing the model's response.
// Supply nodeID when known to enforce a particular stage protocol. Without it,
// this is a schema/protocol preflight, not proof of a live stage binding.
func NormalizeAIStageSubmission(text string, schema json.RawMessage, nodeID ...string) (json.RawMessage, []AIStageNormalization, error) {
	node := CompiledNode{OutputSchema: schema}
	if len(nodeID) > 1 {
		return nil, nil, fmt.Errorf("科研提交只能绑定一个阶段")
	}
	if len(nodeID) == 1 {
		node.ID = nodeID[0]
		return normalizeWorkflowAIStageSubmission(text, node)
	}
	// A custom node can legitimately declare the same property names without
	// using the method stage's split protocol. Never infer its identity solely
	// from a schema fingerprint. The live validator supplies the actual node.
	if schemaDeclaresProperty(schema, "code") && schemaDeclaresProperty(schema, "analysisInput") && schemaDeclaresProperty(schema, "methodSummary") {
		if _, found, _ := extractSplitDynamicImplementationSyntax(text); found {
			node.ID = "method_implementation"
			return normalizeWorkflowAIStageSubmission(text, node)
		}
	}
	if value, changes, err := normalizeWorkflowAIStageSubmission(text, node); err == nil {
		return value, changes, nil
	}
	if IsResearchPlannerSchema(schema) {
		node.ID = "explore"
	}
	return normalizeWorkflowAIStageSubmission(text, node)
}

// ValidateResearchSubmission is a read-only pre-commit check for the same Chat
// run's correction loop. It does not complete a step, start a model, write an
// audit event, create a workspace, or weaken final Skill/citation validation.
func (s *RuntimeService) ValidateResearchSubmission(ctx context.Context, conversationID, workflowRunID, workflowStepID, text string) (json.RawMessage, error) {
	conversationID, workflowRunID, workflowStepID = strings.TrimSpace(conversationID), strings.TrimSpace(workflowRunID), strings.TrimSpace(workflowStepID)
	if s == nil || s.repository == nil || conversationID == "" || workflowRunID == "" || workflowStepID == "" {
		return nil, fmt.Errorf("科研提交缺少完整的会话、任务和阶段绑定")
	}
	projectID, err := s.repository.ProjectIDForWorkflowRun(ctx, workflowRunID)
	if err != nil {
		return nil, fmt.Errorf("读取科研提交绑定失败：%w", err)
	}
	detail, err := s.repository.GetRun(ctx, projectID, workflowRunID)
	if err != nil {
		return nil, fmt.Errorf("读取科研提交阶段失败：%w", err)
	}
	if detail.Run.ID != workflowRunID || detail.Run.ProjectID != projectID || detail.Run.ConversationID != conversationID || detail.Run.Status.Terminal() {
		return nil, fmt.Errorf("科研提交与当前运行中的会话绑定不一致")
	}
	step := findStep(detail.Steps, workflowStepID)
	if step == nil || step.Status != StepRunning || step.Ordinal != detail.Run.CurrentStep || step.InputSHA256 == "" || hashJSON(step.Input) != step.InputSHA256 {
		return nil, fmt.Errorf("科研提交阶段或冻结输入已变化，请使用当前阶段")
	}
	if detail.Run.InputsSHA256 == "" || hashJSON(detail.Run.Inputs) != detail.Run.InputsSHA256 {
		return nil, fmt.Errorf("科研任务输入快照校验失败")
	}
	compilation := detail.Run.Compilation
	if detail.Run.CompilationSHA256 == "" || compilation.CompilationSHA256 != detail.Run.CompilationSHA256 {
		return nil, fmt.Errorf("科研任务冻结方案摘要不一致")
	}
	compilation.CompilationSHA256 = ""
	encoded, err := canonicalJSON(compilation)
	if err != nil || hashBytes(encoded) != detail.Run.CompilationSHA256 {
		return nil, fmt.Errorf("科研任务冻结方案校验失败")
	}
	node, exists := compilationNodeMap(compilation)[step.NodeID]
	// The verified compilation covers both schema bytes and their original
	// identifier. Do not re-hash RawMessage formatting here: persistence may
	// compact an originally pretty-printed schema without changing its meaning.
	if !exists || (node.Kind != NodeAIAnalysis && node.Kind != NodeAgentStage) || node.OutputSchemaSHA256 == "" || !json.Valid(node.OutputSchema) {
		return nil, fmt.Errorf("科研提交阶段的冻结输出契约无效")
	}
	if err := validateAIStageProtocol(node); err != nil {
		return nil, err
	}
	node = literaturePhaseNode(node, step.Input)
	node = selectedEvidencePhaseNode(node, step.Input)
	if err := ValidateAIStageSchema(node.OutputSchema); err != nil {
		return nil, err
	}
	value, _, err := normalizeWorkflowAIStageSubmissionForInput(text, node, step.Input)
	if err != nil {
		return nil, &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", UserMessage: "科研阶段提交未通过输出检查", Details: err.Error(), Cause: err}
	}
	chatRunID := ""
	for _, execution := range detail.AIExecutions {
		if execution.WorkflowStepID == step.ID && execution.Attempt == step.Attempt && execution.InputSHA256 == step.InputSHA256 {
			chatRunID = execution.ChatRunID
			value = canonicalCitationSubmission(value, detail, *step, node, execution.ChatRunID)
			break
		}
	}
	changes, refreshErr := s.changedEvidenceMaterials(ctx, detail, *step, node)
	if refreshErr != nil {
		return nil, refreshErr
	}
	// The terminal response remains an audit draft, never a completed result.
	// The host will rewind evidence stages instead of validating stale claims.
	if len(changes) > 0 {
		return value, nil
	}
	if err := validateWorkflowAIStageOutput(node, value, step.Input); err != nil {
		var quoteErr *CitationQuoteError
		if errors.As(err, &quoteErr) {
			err = modelVisibleQuoteError(quoteErr, workflowCitationSeedsFromSteps(detail.Run.Compilation, detail.Steps, *step), chatRunID)
		}
		return nil, &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", UserMessage: "科研阶段提交未通过业务检查", Details: err.Error(), Cause: err}
	}
	return value, nil
}

func normalizeWorkflowAIStageSubmissionForInput(text string, node CompiledNode, input json.RawMessage) (json.RawMessage, []AIStageNormalization, error) {
	value, changes, err := normalizeWorkflowAIStageSubmission(text, node)
	if err == nil && usesReportProvenance(node) {
		var additions []AIStageNormalization
		value, additions, err = enrichReportProvenance(value, input)
		changes = append(changes, additions...)
		if err == nil {
			err = (tool.JSONSchemaValidator{}).Validate(node.OutputSchema, value)
		}
		return value, changes, err
	}
	if err != nil || node.ID != "candidate_screening" || node.PromptVersion != literatureScreeningVersion {
		return value, changes, err
	}
	value, quoteChanges, err := normalizeLiteratureQuotes(value, input)
	changes = append(changes, quoteChanges...)
	if err == nil {
		err = (tool.JSONSchemaValidator{}).Validate(node.OutputSchema, value)
	}
	return value, changes, err
}

func normalizeWorkflowAIStageSubmission(text string, node CompiledNode) (json.RawMessage, []AIStageNormalization, error) {
	if err := ValidateAIStageSchema(node.OutputSchema); err != nil {
		return nil, nil, err
	}
	// Keep all accepted historical protocol forms stable. In particular, do not
	// canonicalize a value that already satisfies its enum or schema.
	value, originalErr := extractWorkflowAIStageOutputStrict(text, node)
	if originalErr == nil {
		return value, nil, nil
	}
	var candidate json.RawMessage
	if node.ID == "method_implementation" {
		envelopeJSON, found, err := extractSplitDynamicImplementationSyntax(text)
		if !found || err != nil {
			return nil, nil, originalErr
		}
		var envelope struct {
			Metadata json.RawMessage `json:"metadata"`
			Code     string          `json:"code"`
		}
		if json.Unmarshal(envelopeJSON, &envelope) != nil || strings.TrimSpace(envelope.Code) == "" {
			return nil, nil, originalErr
		}
		// Preserve the split protocol's prohibition against code inside metadata.
		// Otherwise a duplicated metadata code could silently be overwritten.
		var metadata map[string]json.RawMessage
		if json.Unmarshal(envelope.Metadata, &metadata) != nil || metadata == nil || len(metadata["code"]) != 0 {
			return nil, nil, originalErr
		}
		code, _ := json.Marshal(envelope.Code)
		metadata["code"] = code
		candidate, _ = json.Marshal(metadata)
	} else {
		// The existing envelope parser still owns syntax, size, suffix, and
		// nested-fence checks; only field representation is relaxed here.
		var err error
		candidate, err = extractStageJSON(text, raw(`{"type":"object","additionalProperties":true}`))
		if err != nil {
			return nil, nil, originalErr
		}
	}
	changes := []AIStageNormalization{}
	normalized, err := normalizeAIStageJSONValue(candidate, node.OutputSchema, "$", 0, &changes)
	if err != nil {
		return nil, nil, err
	}
	if len(changes) == 0 {
		return nil, nil, originalErr
	}
	if err := (tool.JSONSchemaValidator{}).Validate(node.OutputSchema, normalized); err != nil {
		return normalized, changes, fmt.Errorf("AI 阶段输出不符合 Schema: %w", err)
	}
	return normalized, changes, nil
}

func normalizeAIStageJSONValue(value, schema json.RawMessage, path string, depth int, changes *[]AIStageNormalization) (json.RawMessage, error) {
	if depth > 64 || len(*changes) > 256 {
		return nil, fmt.Errorf("AI 阶段格式规范化超过安全复杂度限制")
	}
	var spec map[string]json.RawMessage
	if json.Unmarshal(schema, &spec) != nil || spec == nil {
		return value, nil
	}
	// Never choose a union branch or resolve an unknown reference by guessing.
	for _, keyword := range []string{"$ref", "oneOf", "anyOf", "allOf", "if", "not"} {
		if _, exists := spec[keyword]; exists {
			return value, nil
		}
	}
	var kind string
	_ = json.Unmarshal(spec["type"], &kind)
	var textValue string
	isString := json.Unmarshal(value, &textValue) == nil && strings.HasPrefix(strings.TrimSpace(string(value)), `"`)
	if isString && kind == "array" {
		var items map[string]json.RawMessage
		var itemType string
		if json.Unmarshal(spec["items"], &items) == nil {
			_ = json.Unmarshal(items["type"], &itemType)
		}
		if itemType == "string" && len(spec["prefixItems"]) == 0 {
			next, _ := json.Marshal([]json.RawMessage{value})
			*changes = append(*changes, AIStageNormalization{Path: path, Rule: "string_to_string_array", BeforeSHA256: hashJSON(value), AfterSHA256: hashJSON(next)})
			value = next
		}
	} else if isString && kind == "string" {
		var options []string
		if json.Unmarshal(spec["enum"], &options) == nil && len(options) > 0 {
			matched, count, exact := "", 0, false
			for _, option := range options {
				if textValue == option {
					exact = true
					break
				}
				if strings.EqualFold(strings.TrimSpace(textValue), strings.TrimSpace(option)) {
					matched, count = option, count+1
				}
			}
			if !exact && count == 1 {
				next, _ := json.Marshal(matched)
				*changes = append(*changes, AIStageNormalization{Path: path, Rule: "enum_canonical_value", BeforeSHA256: hashJSON(value), AfterSHA256: hashJSON(next)})
				value = next
			}
		}
	}
	if kind == "object" {
		var object map[string]json.RawMessage
		var properties map[string]json.RawMessage
		if json.Unmarshal(value, &object) != nil || object == nil || json.Unmarshal(spec["properties"], &properties) != nil {
			return value, nil
		}
		keys := make([]string, 0, len(properties))
		for key := range properties {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		before := len(*changes)
		for _, key := range keys {
			child, exists := object[key]
			if !exists {
				continue // Never fill a required semantic field, including defaults.
			}
			next, err := normalizeAIStageJSONValue(child, properties[key], path+"."+key, depth+1, changes)
			if err != nil {
				return nil, err
			}
			object[key] = next
		}
		if before != len(*changes) {
			return json.Marshal(object) // Unknown fields are retained for validation.
		}
	} else if kind == "array" {
		var array []json.RawMessage
		if json.Unmarshal(value, &array) != nil || array == nil {
			return value, nil
		}
		before := len(*changes)
		for index, child := range array {
			next, err := normalizeAIStageJSONValue(child, spec["items"], fmt.Sprintf("%s[%d]", path, index), depth+1, changes)
			if err != nil {
				return nil, err
			}
			array[index] = next
		}
		if before != len(*changes) {
			return json.Marshal(array)
		}
	}
	return value, nil
}

// Count actual queued format repairs for this immutable input, independently
// of model/network failures and user-triggered attempts. Legacy events lacked
// an input hash; use the corresponding frozen execution to identify them.
func automaticAIOutputRepairCount(detail RunDetail, step Step) int {
	if strings.TrimSpace(step.InputSHA256) == "" {
		return 0
	}
	seen := map[int]bool{}
	for _, event := range detail.Events {
		if event.Type != "workflow.ai_output_repair_queued" {
			continue
		}
		var queued struct {
			StepID      string `json:"stepId"`
			Attempt     int    `json:"attempt"`
			InputSHA256 string `json:"inputSha256"`
		}
		if json.Unmarshal(event.Payload, &queued) != nil || queued.StepID != step.ID || queued.Attempt <= 0 || queued.Attempt >= step.Attempt {
			continue
		}
		if queued.InputSHA256 == "" {
			for _, execution := range detail.AIExecutions {
				if execution.WorkflowStepID == step.ID && execution.Attempt == queued.Attempt {
					queued.InputSHA256 = execution.InputSHA256
					break
				}
			}
		}
		if queued.InputSHA256 == step.InputSHA256 {
			seen[queued.Attempt] = true
		}
	}
	return len(seen)
}

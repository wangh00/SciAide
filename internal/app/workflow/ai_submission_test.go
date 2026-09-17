package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/apperr"
)

func TestNormalizeAIStageSubmissionEquivalentRepresentations(t *testing.T) {
	schema := raw(`{"type":"object","additionalProperties":false,"required":["selectedSkills","confidence","n"],"properties":{"selectedSkills":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["name","limitations"],"properties":{"name":{"type":"string"},"limitations":{"type":"array","items":{"type":"string"}}}}},"confidence":{"type":"string","enum":["low","medium","high"]},"n":{"type":"integer"}}}`)
	text := "```json\n" + `{"selectedSkills":[{"name":"Exact_Name","limitations":"one; two\nthree, four"}],"confidence":" HIGH ","n":9007199254740993}` + "\n```"
	normalized, changes, err := NormalizeAIStageSubmission(text, schema)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		SelectedSkills []struct {
			Name        string   `json:"name"`
			Limitations []string `json:"limitations"`
		} `json:"selectedSkills"`
		Confidence string      `json:"confidence"`
		N          json.Number `json:"n"`
	}
	if json.Unmarshal(normalized, &result) != nil || len(result.SelectedSkills) != 1 || result.SelectedSkills[0].Name != "Exact_Name" || len(result.SelectedSkills[0].Limitations) != 1 || result.SelectedSkills[0].Limitations[0] != "one; two\nthree, four" || result.Confidence != "high" || result.N.String() != "9007199254740993" {
		t.Fatalf("changed scientific content: %s", normalized)
	}
	if len(changes) != 2 {
		t.Fatalf("changes=%+v", changes)
	}
	for _, change := range changes {
		if change.Path == "" || len(change.BeforeSHA256) != 64 || len(change.AfterSHA256) != 64 || change.BeforeSHA256 == change.AfterSHA256 {
			t.Fatalf("missing audit=%+v", change)
		}
	}
	again, secondChanges, err := NormalizeAIStageSubmission(string(normalized), schema)
	if err != nil || len(secondChanges) != 0 || !rawJSONEqual(again, normalized) {
		t.Fatalf("not idempotent: %s %+v %v", again, secondChanges, err)
	}
}

func TestNormalizeAIStageSubmissionDoesNotGuessMeaning(t *testing.T) {
	for _, tt := range []struct{ name, text, schema string }{
		{"boolean string", `{"approved":"true"}`, `{"type":"object","required":["approved"],"properties":{"approved":{"type":"boolean"}}}`},
		{"number string", `{"n":"42"}`, `{"type":"object","properties":{"n":{"type":"integer"}}}`},
		{"unknown field", `{"values":"x","invented":true}`, `{"type":"object","additionalProperties":false,"properties":{"values":{"type":"array","items":{"type":"string"}}}}`},
		{"missing semantic required", `{"values":"x"}`, `{"type":"object","required":["decision"],"properties":{"decision":{"type":"boolean"},"values":{"type":"array","items":{"type":"string"}}}}`},
		{"ambiguous enum", `{"value":" a "}`, `{"type":"object","properties":{"value":{"type":"string","enum":["a","A"]}}}`},
		{"no enum name guessing", `{"name":" Wrong Name "}`, `{"type":"object","properties":{"name":{"type":"string","pattern":"^[a-z_-]+$"}}}`},
		{"no content splitting", `{"values":"x,y"}`, `{"type":"object","properties":{"values":{"type":"array","minItems":2,"items":{"type":"string"}}}}`},
		{"unknown enum", `{"value":"maybe"}`, `{"type":"object","properties":{"value":{"type":"string","enum":["yes","no"]}}}`},
		{"no union guessing", `{"value":"x"}`, `{"type":"object","properties":{"value":{"oneOf":[{"type":"array","items":{"type":"string"}},{"type":"integer"}]}}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			candidate, _, err := NormalizeAIStageSubmission(tt.text, raw(tt.schema))
			if err == nil {
				t.Fatalf("unsafe repair accepted: %s", candidate)
			}
			if strings.Contains(tt.text, "invented") && len(candidate) > 0 && !strings.Contains(string(candidate), "invented") {
				t.Fatal("unknown field discarded")
			}
		})
	}
}

func TestNormalizeAIStageSubmissionPreservesSplitImplementationProtocol(t *testing.T) {
	text := "```json\n" + `{"methodSummary":"compute summary","dependencies":"numpy","analysisInput":{}}` + "\n```\n```python\nsummary = {\"n\": 1}\nsummary\n```"
	value, changes, err := NormalizeAIStageSubmission(text, dynamicImplementationSchema())
	if err != nil || len(changes) != 1 || changes[0].Path != "$.dependencies" {
		t.Fatalf("split=%s changes=%+v err=%v", value, changes, err)
	}
	var result struct {
		Code         string   `json:"code"`
		Dependencies []string `json:"dependencies"`
	}
	if json.Unmarshal(value, &result) != nil || result.Code != "summary = {\"n\": 1}\nsummary" || len(result.Dependencies) != 1 || result.Dependencies[0] != "numpy" {
		t.Fatalf("split lost code: %s", value)
	}
	duplicated := strings.Replace(text, `"analysisInput":{}`, `"analysisInput":{},"code":"different code"`, 1)
	if value, _, err := NormalizeAIStageSubmission(duplicated, dynamicImplementationSchema()); err == nil {
		t.Fatalf("metadata code silently overwritten: %s", value)
	}
}

func TestNormalizeAIStageSubmissionDoesNotInferCustomNodeIdentityFromSchema(t *testing.T) {
	text := `{"methodSummary":"custom JSON-only output","dependencies":[],"analysisInput":{},"code":"summary = {}"}`
	if _, _, err := NormalizeAIStageSubmission(text, dynamicImplementationSchema()); err != nil {
		t.Fatalf("custom schema incorrectly forced split protocol: %v", err)
	}
	if _, _, err := NormalizeAIStageSubmission(text, dynamicImplementationSchema(), "custom_node"); err != nil {
		t.Fatalf("explicit custom node rejected: %v", err)
	}
	if _, _, err := NormalizeAIStageSubmission(text, dynamicImplementationSchema(), "method_implementation"); err != nil {
		t.Fatalf("complete structured implementation rejected: %v", err)
	}
}

func TestAIStageCandidateAfterProseKeepsNewlineBoundary(t *testing.T) {
	schema := raw(`{"type":"object"}`)
	for _, text := range []string{"这里是结果。\n{\"broken\":", "解释文字\r\n  [invalid", "说明\n\t{wrong}"} {
		if !HasAIStageOutputCandidate(text, schema) {
			t.Fatalf("malformed submission misclassified as progress: %q", text)
		}
	}
	if HasAIStageOutputCandidate("正在检查 {字段} 的定义。", schema) {
		t.Fatal("inline explanatory braces misclassified as a submission")
	}
}

type submissionRuntimeRepository struct {
	repairRecoveryRepository
	events  []RuntimeEvent
	repairs []RuntimeEvent
}

func (r *submissionRuntimeRepository) RecordEvent(_ context.Context, e RuntimeEvent) error {
	r.events = append(r.events, e)
	return nil
}
func (r *submissionRuntimeRepository) QueueAutomaticAIOutputRepair(_ context.Context, _, _ string, _ int, _ time.Time, e RuntimeEvent) error {
	r.repairs = append(r.repairs, e)
	return nil
}
func submissionRuntimeService(r *submissionRuntimeRepository) *RuntimeService {
	return &RuntimeService{repository: r, now: time.Now, newID: func() (string, error) { return "event", nil }}
}

func TestInvalidHostSchemaDoesNotRequestAIRepair(t *testing.T) {
	for _, finish := range []bool{true, false} {
		detail, step, node, execution := repairRecoveryFixture()
		node.OutputSchema = raw(`{"type":"object","required":["summary","summary"]}`)
		r := &submissionRuntimeRepository{}
		if err := submissionRuntimeService(r).completeAIExecution(context.Background(), detail, step, node, execution, finish); err != nil {
			t.Fatal(err)
		}
		if r.failure != aiSchemaInvalidCode || len(r.repairs) != 0 || len(r.output) != 0 {
			t.Fatalf("host error requested repair or committed output: %+v", r)
		}
		if finish && (r.execution.ErrorCode != aiSchemaInvalidCode || r.execution.OutputText != execution.OutputText) {
			t.Fatal("raw model response or error classification was lost")
		}
		_, _, err := NormalizeAIStageSubmission("not JSON", node.OutputSchema)
		var classified *apperr.Error
		if !errors.As(err, &classified) || classified.Code != aiSchemaInvalidCode {
			t.Fatalf("normalizer blamed model output for host schema: %v", err)
		}
	}
}

func TestCompleteAIExecutionAuditsNormalizationWithoutLosingRawText(t *testing.T) {
	detail, step, node, execution := repairRecoveryFixture()
	execution.OutputText = `{"summary":"unchanged scientific result","queries":"ok"}`
	r := &submissionRuntimeRepository{}
	if err := submissionRuntimeService(r).completeAIExecution(context.Background(), detail, step, node, execution, true); err != nil {
		t.Fatal(err)
	}
	if r.execution.Status != "completed" || r.execution.OutputText != execution.OutputText || len(r.events) != 1 || r.events[0].Type != "workflow.ai_output_normalized" || len(r.repairs) != 0 {
		t.Fatalf("execution=%+v events=%+v", r.execution, r.events)
	}
	if !strings.Contains(string(r.execution.Output), `"queries":["ok"]`) || !strings.Contains(string(r.events[0].Payload), "string_to_string_array") {
		t.Fatalf("output=%s event=%s", r.execution.Output, r.events[0].Payload)
	}
}

func TestCompleteAIExecutionDoesNotMergeOrReuseScientificResponses(t *testing.T) {
	detail, step, node, execution := repairRecoveryFixture()
	execution.OutputText = `{"summary":42,"queries":["ok"]}`
	detail.AIExecutions = []AIExecution{
		{WorkflowStepID: step.ID, Attempt: 1, InputSHA256: step.InputSHA256, OutputText: `{"summary":"old result","queries":["toolong"]}`, ErrorCode: "WORKFLOW_AI_OUTPUT_INVALID", ErrorMessage: "$.queries[0]: violates maxLength"},
		{WorkflowStepID: step.ID, Attempt: 2, InputSHA256: step.InputSHA256, OutputText: `{"summary":"different old result","queries":["ok"]}`},
	}
	r := &submissionRuntimeRepository{}
	if err := submissionRuntimeService(r).completeAIExecution(context.Background(), detail, step, node, execution, true); err != nil {
		t.Fatal(err)
	}
	if r.execution.Status != "failed" || len(r.output) != 0 || len(r.repairs) != 1 || r.execution.OutputText != execution.OutputText {
		t.Fatalf("old answer reused: %+v", r)
	}
}

func TestAutomaticAIOutputRepairBudgetIgnoresNetworkAndManualAttempts(t *testing.T) {
	detail, step, node, execution := repairRecoveryFixture()
	execution.OutputText = `{"summary":42,"queries":["ok"]}`
	detail.AIExecutions = []AIExecution{{WorkflowStepID: step.ID, Attempt: 1, InputSHA256: step.InputSHA256, ErrorCode: "MODEL_UNAVAILABLE"}, {WorkflowStepID: step.ID, Attempt: 2, InputSHA256: step.InputSHA256, ErrorCode: "MODEL_UNAVAILABLE"}}
	detail.Events = []RuntimeEvent{{Type: "workflow.step_retry_queued", Payload: rawObject(map[string]any{"stepId": step.ID, "attempt": 2})}}
	r := &submissionRuntimeRepository{}
	if err := submissionRuntimeService(r).completeAIExecution(context.Background(), detail, step, node, execution, true); err != nil {
		t.Fatal(err)
	}
	if len(r.repairs) != 1 || !strings.Contains(string(r.repairs[0].Payload), `"repairNumber":1`) {
		t.Fatalf("attempt3 got no format repair: %+v", r)
	}
	// A duplicate event is not another repair; a different input does not spend
	// this input's budget. Legacy events acquire provenance from AI executions.
	event := RuntimeEvent{Type: "workflow.ai_output_repair_queued", Payload: rawObject(map[string]any{"stepId": step.ID, "attempt": 3, "inputSha256": step.InputSHA256})}
	detail.Events = append(detail.Events, event, event, RuntimeEvent{Type: "workflow.ai_output_repair_queued", Payload: rawObject(map[string]any{"stepId": step.ID, "attempt": 4, "inputSha256": "other"})})
	step.Attempt = 7
	execution.Attempt = 7
	if count := automaticAIOutputRepairCount(detail, step); count != 1 {
		t.Fatalf("count=%d", count)
	}
	detail.Events = append(detail.Events, RuntimeEvent{Type: "workflow.ai_output_repair_queued", Payload: rawObject(map[string]any{"stepId": step.ID, "attempt": 5})})
	detail.AIExecutions = append(detail.AIExecutions, AIExecution{WorkflowStepID: step.ID, Attempt: 5, InputSHA256: step.InputSHA256})
	r = &submissionRuntimeRepository{}
	if err := submissionRuntimeService(r).completeAIExecution(context.Background(), detail, step, node, execution, true); err != nil {
		t.Fatal(err)
	}
	if len(r.repairs) != 0 || r.failure != "WORKFLOW_AI_OUTPUT_INVALID" || automaticAIOutputRepairCount(detail, step) != 2 {
		t.Fatalf("format budget exceeded: %+v", r)
	}
}

func TestAgentContentRepairExhaustionNeverStartsWorkflowRepairBudget(t *testing.T) {
	detail, step, node, execution := repairRecoveryFixture()
	execution.Status = "failed"
	execution.ErrorCode = "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED"
	execution.ErrorMessage = "candidate still invalid"
	r := &submissionRuntimeRepository{}
	if err := submissionRuntimeService(r).projectFinishedAIExecution(context.Background(), detail, step, node, execution); err != nil {
		t.Fatal(err)
	}
	if r.failure != execution.ErrorCode || len(r.repairs) != 0 {
		t.Fatalf("agent exhaustion restarted: %+v", r)
	}
}

type submissionValidationRepository struct {
	RuntimeRepository
	detail RunDetail
	err    error
}

func (r *submissionValidationRepository) ProjectIDForWorkflowRun(context.Context, string) (string, error) {
	return r.detail.Run.ProjectID, r.err
}
func (r *submissionValidationRepository) GetRun(context.Context, string, string) (RunDetail, error) {
	return r.detail, r.err
}
func submissionValidationFixture(t *testing.T) (*RuntimeService, *submissionValidationRepository, string) {
	t.Helper()
	detail := recommendationFixture(t)
	detail.Run.ProjectID = "project"
	detail.Run.ConversationID = "conversation"
	detail.Run.Status = RunRunning
	detail.Run.CurrentStep = 4
	detail.Steps[4].Status = StepRunning
	detail.Run.InputsSHA256 = hashJSON(detail.Run.Inputs)
	for i := range detail.Run.Compilation.Nodes {
		detail.Run.Compilation.Nodes[i].OutputSchemaSHA256 = hashBytes(detail.Run.Compilation.Nodes[i].OutputSchema)
	}
	encoded, err := canonicalJSON(detail.Run.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	detail.Run.CompilationSHA256 = hashBytes(encoded)
	detail.Run.Compilation.CompilationSHA256 = detail.Run.CompilationSHA256
	full, err := outputPort(detail.Steps[4].Output, "analysis")
	if err != nil {
		t.Fatal(err)
	}
	r := &submissionValidationRepository{detail: detail}
	return &RuntimeService{repository: r}, r, string(full)
}

func TestValidateResearchSubmissionReadOnlyBusinessFeedback(t *testing.T) {
	s, r, text := submissionValidationFixture(t)
	before, _ := json.Marshal(r.detail)
	value, err := s.ValidateResearchSubmission(context.Background(), "conversation", "run", r.detail.Steps[4].ID, text)
	if err != nil || !rawJSONEqual(value, raw(text)) {
		t.Fatalf("valid read-only submission=%s %v", value, err)
	}
	invalid := strings.Replace(text, `"nodeId":"method_selection"`, `"nodeId":"imaginary_stage"`, 1)
	_, err = s.ValidateResearchSubmission(context.Background(), "conversation", "run", r.detail.Steps[4].ID, invalid)
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "WORKFLOW_AI_SUBMISSION_INVALID" || !strings.Contains(err.Error(), "imaginary_stage") {
		t.Fatalf("business feedback=%v", err)
	}
	after, _ := json.Marshal(r.detail)
	if !rawJSONEqual(before, after) {
		t.Fatal("precommit validation mutated persisted state")
	}
}

func TestLiveSubmissionRejectsHostSchemaWithoutContentCorrection(t *testing.T) {
	s, r, text := submissionValidationFixture(t)
	node := &r.detail.Run.Compilation.Nodes[4]
	node.OutputSchema = raw(`{"type":"object","required":["summary","summary"]}`)
	node.OutputSchemaSHA256 = hashBytes(node.OutputSchema)
	r.detail.Run.Compilation.CompilationSHA256 = ""
	encoded, err := canonicalJSON(r.detail.Run.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	r.detail.Run.CompilationSHA256 = hashBytes(encoded)
	r.detail.Run.Compilation.CompilationSHA256 = r.detail.Run.CompilationSHA256
	_, err = s.ValidateResearchSubmission(context.Background(), "conversation", "run", r.detail.Steps[4].ID, text)
	var classified *apperr.Error
	if !errors.As(err, &classified) || classified.Code != aiSchemaInvalidCode {
		t.Fatalf("live host contract failure was misclassified: %v", err)
	}
}

func TestValidateResearchSubmissionRejectsBindingAndSnapshotFailuresAsFatal(t *testing.T) {
	for _, tt := range []struct {
		name                    string
		mutate                  func(*submissionValidationRepository)
		conversation, run, step string
	}{
		{name: "conversation", conversation: "wrong"},
		{name: "run", run: "wrong"},
		{name: "step", step: "wrong"},
		{name: "input hash", mutate: func(r *submissionValidationRepository) { r.detail.Steps[4].Input = raw(`{}`) }},
		{name: "compilation hash", mutate: func(r *submissionValidationRepository) { r.detail.Run.Compilation.Nodes[4].OutputSchema = raw(`{}`) }},
		{name: "terminal run", mutate: func(r *submissionValidationRepository) { r.detail.Run.Status = RunFailed }},
		{name: "storage failure", mutate: func(r *submissionValidationRepository) { r.err = errors.New("storage unavailable") }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, r, text := submissionValidationFixture(t)
			if tt.mutate != nil {
				tt.mutate(r)
			}
			conversation, run, step := "conversation", "run", r.detail.Steps[4].ID
			if tt.conversation != "" {
				conversation = tt.conversation
			}
			if tt.run != "" {
				run = tt.run
			}
			if tt.step != "" {
				step = tt.step
			}
			_, err := s.ValidateResearchSubmission(context.Background(), conversation, run, step, text)
			var appErr *apperr.Error
			if err == nil || errors.As(err, &appErr) && appErr.Code == "WORKFLOW_AI_SUBMISSION_INVALID" {
				t.Fatalf("fatal misclassified as repairable: %v", err)
			}
		})
	}
}

package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Scripted HTTP + real adapters/validator/SQLite/gate, not model quality proof.
func TestReviewCriterionRuntimePersistsRejectionAndBoundedRepair(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		for _, mode := range []string{"exact", "mixed", "exhausted_then_retry"} {
			t.Run(string(protocol)+"/"+mode, func(t *testing.T) {
				testReviewCriterionRuntime(t, protocol, mode)
			})
		}
	}
}

func testReviewCriterionRuntime(t *testing.T, protocol modelprofile.APIProtocol, mode string) {
	t.Helper()
	var requests atomic.Int32
	var retrying atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		n := int(requests.Add(1))
		prompt := workflowAITestPrompt(contextPipelineCanonicalRequest(t, body))
		input := workflowAITestStageInput(prompt)
		criteria := input["acceptanceCriteria"].([]any)
		quote := criteria[0].(map[string]any)["text"].(string)
		if mode == "mixed" && n == 2 {
			for _, required := range []string{"needsUserInput", "/researchHandoff/attributionObservations/0/writtenDois/0", "Host submission validation failed"} {
				if !strings.Contains(prompt, required) {
					t.Errorf("same correction missing independent error: %s", required)
				}
			}
		}
		if mode == "exhausted_then_retry" && !retrying.Load() {
			quote = "Invented requirement that does not occur in the current input."
		}
		history := input["reviewHistory"].(map[string]any)
		findingID := history["issuePrefix"].(string) + "1"
		finding := workflowAITestTrackedFinding(findingID, "open", "methodIssues", "Missing actual screening detail", map[string]any{"pointer": "/context/markdown", "quote": "Screening detail missing."}, "new", "First detection from this frozen draft", 0)
		finding["basis"] = []any{map[string]any{"pointer": "/context/markdown", "quote": "Screening detail missing."}, map[string]any{"pointer": "/acceptanceCriteria/1/text", "quote": quote}}
		plan := map[string]any{"correctionIndex": 0, "nodeId": "", "reason": "The supplied report needs a screening description", "whyNotLater": "No report-editing stage was offered in this isolated route", "needsUserInput": true, "requiredInput": "Provide the missing screening detail"}
		if mode == "mixed" && n == 1 {
			delete(plan, "needsUserInput")
			delete(plan, "requiredInput")
			finding["basis"] = append(finding["basis"].([]any), map[string]any{"pointer": "/researchHandoff/attributionObservations/0/writtenDois/0", "quote": "Derived observation, not evidence"})
		}
		output := map[string]any{
			"approved": false, "reviewedInputSha256": workflowAITestStageInputSHA256(prompt),
			"verifiedClaims": []string{}, "requiredCorrections": []string{"Describe the actual screening decisions and exclusion reasons"}, "confidence": "medium", "limitations": []string{},
			"acceptanceChecks": []any{map[string]any{"criterionId": "criterion-1", "status": "not_met", "basis": findingID + ": screening detail is missing"}, map[string]any{"criterionId": "criterion-2", "status": "met", "basis": "Limited scope disclosed"}},
			"reviewFindings":   []any{finding}, "suggestions": []string{}, "revisionPlan": []any{plan},
		}
		emitQuestionHandoffFixture(t, w, protocol, n, "```json\n"+string(mustBootstrapJSON(t, output))+"\n```", "", "")
	}))
	defer server.Close()
	options := Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}}
	app, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	app.Startup(context.Background())
	profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "criterion review fixture", BaseURL: server.URL + "/v1", APIProtocol: protocol, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true, ContextWindowTokens: 272000, AutoCompactTokenLimit: 244800}}, TimeoutSeconds: 10, Enabled: true, IsDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "criterion review isolated"})
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("testdata/review-kernel/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: project.ID, Definition: workflow.Definition{
		SchemaVersion: 1, Name: "Review frozen criteria",
		Inputs: []workflow.Port{{Name: "context", Type: workflow.TypeObject, Required: true}, {Name: "research_contract", Type: workflow.TypeObject, Required: true}},
		Nodes: []workflow.Node{
			{ID: "independent_review", Name: "Independent review", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: "criterion-review-fixture-v1", Prompt: "Independently assess the frozen report against its actual requirements.", ReviewPolicy: workflow.AIReviewAuto, AllowedTools: []string{workflow.ResearchTaskReadTool}, OutputSchema: schema},
			{ID: "delivery_gate", Name: "Delivery gate", Kind: workflow.NodeTool, ToolName: "builtin.research.workflow.review.gate", Arguments: json.RawMessage(`{"subject":{}}`)},
		},
		Edges: []workflow.Edge{
			{FromNode: "$input", FromPort: "context", ToNode: "independent_review", ToPort: "context"},
			{FromNode: "$input", FromPort: "research_contract", ToNode: "independent_review", ToPort: "researchContract"},
			{FromNode: "independent_review", FromPort: "analysis", ToNode: "delivery_gate", ToPort: "review"},
		},
		Outputs: []workflow.Output{{Name: "review", Type: workflow.TypeObject, FromNode: "independent_review", FromPort: "analysis", Required: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	inputs := mustBootstrapJSON(t, map[string]any{"context": map[string]any{"markdown": "Screening detail missing. Limited scope disclosed."}, "research_contract": map[string]any{"successCriteria": []string{"Report actual screening decisions and exclusion reasons.", "Disclose the limited scope."}}})
	started, err := app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: project.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: inputs, ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: conversation.PermissionFullAccess})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunFailed, 25*time.Second)
	wantRequests := int32(1)
	if mode == "mixed" {
		wantRequests = 2
	}
	if mode == "exhausted_then_retry" {
		if requests.Load() != 3 || final.Run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || final.Steps[0].Status != workflow.StepFailed || final.Steps[1].Attempt != 0 || !strings.Contains(final.Run.ErrorMessage, "不代表报告已被判定不合格") {
			t.Fatal("invalid review accepted, mislabeled, or retry budget changed", final.Run.ErrorCode, final.Run.ErrorMessage, requests.Load())
		}
		before := string(mustBootstrapJSON(t, final.Steps))
		if err := app.Close(); err != nil {
			t.Fatal(err)
		}
		app, err = New(options)
		if err != nil {
			t.Fatal(err)
		}
		app.Startup(context.Background())
		reopened, err := app.WorkflowFacade.GetRun(project.ID, final.Run.ID)
		if err != nil || requests.Load() != 3 || string(mustBootstrapJSON(t, reopened.Steps)) != before {
			t.Fatal("restart changed failed review or reran model", err)
		}
		retrying.Store(true)
		if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: final.Run.ID, StepID: final.Steps[0].ID}); err != nil {
			t.Fatal(err)
		}
		final = waitForWorkflowState(t, app, project.ID, final.Run.ID, workflow.RunFailed, 25*time.Second)
		wantRequests = 4
	}
	if final.Run.ErrorCode != "RESEARCH_REVIEW_NOT_APPROVED" || final.Steps[0].Status != workflow.StepCompleted || requests.Load() != wantRequests {
		t.Fatal("scientific verdict turned into program failure", final.Run.ErrorCode, final.Run.ErrorMessage, requests.Load())
	}
	execution := final.AIExecutions[len(final.AIExecutions)-1]
	wantTurns, wantAttempt, wantExecutions := 1, 1, 1
	if mode == "mixed" {
		wantTurns = 2
	}
	if mode == "exhausted_then_retry" {
		wantAttempt, wantExecutions = 2, 2
	}
	if execution.ModelTurns != wantTurns || execution.Attempt != wantAttempt || len(final.AIExecutions) != wantExecutions {
		t.Fatal("correction restarted whole stage or retry restarted completed work", execution.ModelTurns, execution.Attempt, len(final.AIExecutions))
	}
	var review map[string]any
	if err := json.Unmarshal(execution.Output, &review); err != nil {
		t.Fatal(err)
	}
	finding := review["reviewFindings"].([]any)[0].(map[string]any)
	basis := finding["basis"].([]any)[1].(map[string]any)
	if execution.Status != "completed" || review["approved"] != false || len(review["methodIssues"].([]any)) != 1 || basis["pointer"] != "/acceptanceCriteria/0/text" || basis["quote"] != "Report actual screening decisions and exclusion reasons." {
		t.Fatal("rejection or exact provenance lost", review)
	}
	var journal string
	if err := app.store.DB().QueryRow(`SELECT draft_text FROM model_turn_journal WHERE run_id=? AND turn_index=?`, execution.ChatRunID, execution.ModelTurns).Scan(&journal); err != nil || !strings.Contains(journal, `"pointer":"/acceptanceCriteria/1/text"`) {
		t.Fatal("raw model response not preserved", err)
	}
	digest := sha256.Sum256([]byte(journal))
	audits := 0
	for _, event := range final.Events {
		if event.Type != "workflow.ai_output_normalized" {
			continue
		}
		var payload struct {
			StepID, ExecutionID, InputSHA256, RawOutputTextSHA256 string
			Changes                                               []workflow.AIStageNormalization
		}
		if json.Unmarshal(event.Payload, &payload) != nil || payload.ExecutionID != execution.ID {
			continue
		}
		for _, change := range payload.Changes {
			if change.Rule == "frozen_criterion_index" {
				audits++
				if payload.InputSHA256 != final.Steps[0].InputSHA256 || payload.RawOutputTextSHA256 != hex.EncodeToString(digest[:]) || payload.StepID != final.Steps[0].ID {
					t.Fatal("audit not bound to the exact input/raw response", payload)
				}
			}
		}
	}
	if audits != 1 {
		t.Fatal("missing or duplicate pointer audit", audits)
	}
	var reads int
	if err := app.store.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE tool_name=?`, workflow.ResearchTaskReadTool).Scan(&reads); err != nil || reads != 0 {
		t.Fatal("supplied inputs unnecessarily reread", reads, err)
	}
	before := string(mustBootstrapJSON(t, final.Steps))
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	reopened, err := app.WorkflowFacade.GetRun(project.ID, final.Run.ID)
	if err != nil || requests.Load() != wantRequests || string(mustBootstrapJSON(t, reopened.Steps)) != before {
		t.Fatal("restart changed saved scientific rejection", err)
	}
}

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

// Real adapters + production submission validation + temporary SQLite + gate.
// The model is scripted; this is not a scientific/paid-model quality test.
func TestKernelReviewPointerAliasPersistsWithoutExtraModelCorrection(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		for _, forgedFirst := range []bool{false, true} {
			name := string(protocol) + "/exact"
			if forgedFirst {
				name = string(protocol) + "/forged_first"
			}
			t.Run(name, func(t *testing.T) { testKernelReviewPointerRuntime(t, protocol, forgedFirst) })
		}
	}
}

func TestKernelReviewLongPromptVersionPersistsAndSurvivesRestart(t *testing.T) {
	testKernelReviewPointerRuntime(t, modelprofile.ProtocolOpenAIChat, false, true)
}

func testKernelReviewPointerRuntime(t *testing.T, protocol modelprofile.APIProtocol, forgedFirst bool, longVersion ...bool) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		prompt := workflowAITestPrompt(contextPipelineCanonicalRequest(t, body))
		input := workflowAITestStageInput(prompt)
		for _, text := range []string{"Kernel evidence navigation:", `"valuePointer":"/computedResults/value"`, "do not fetch the same stage_input"} {
			if !strings.Contains(prompt, text) {
				t.Errorf("provider missing actual guidance: %s", text)
			}
		}
		n := int(requests.Add(1))
		history, _ := input["reviewHistory"].(map[string]any)
		prefix, _ := history["issuePrefix"].(string)
		quote := "3"
		if forgedFirst && n == 1 {
			quote = "invented 3"
		}
		finding := workflowAITestTrackedFinding(prefix+"1", "open", "numericIssues", "Reported mean differs from computed result", map[string]any{"pointer": "/context/markdown", "quote": "Result: 5"}, "new", "First detection from the actual current values", 0)
		finding["basis"] = []any{map[string]any{"pointer": "/context/markdown", "quote": "Result: 5"}, map[string]any{"pointer": "/computedResults/mean", "quote": quote}}
		output := map[string]any{
			"approved": false, "reviewedInputSha256": workflowAITestStageInputSHA256(prompt),
			"verifiedClaims": []string{}, "requiredCorrections": []string{"Correct the reported mean, preserving the actual calculation"}, "confidence": "medium", "limitations": []string{},
			"acceptanceChecks": []any{map[string]any{"criterionId": "criterion-1", "status": "not_met", "basis": "Reported mean differs from the current result"}},
			"reviewFindings":   []any{finding}, "suggestions": []string{},
			"revisionPlan": []any{map[string]any{"correctionIndex": 0, "nodeId": "", "reason": "This isolated review has no report-writing repair stage", "whyNotLater": "No repair-capable stage was offered", "needsUserInput": true, "requiredInput": "Provide a corrected report with mean 3"}},
		}
		answer := "```json\n" + string(mustBootstrapJSON(t, output)) + "\n```"
		emitQuestionHandoffFixture(t, w, protocol, n, answer, "", "")
	}))
	defer server.Close()
	root := t.TempDir()
	app, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	app.Startup(context.Background())
	profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "kernel review fixture", BaseURL: server.URL + "/v1", APIProtocol: protocol, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true, ContextWindowTokens: 272000, AutoCompactTokenLimit: 244800}}, TimeoutSeconds: 10, Enabled: true, IsDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "kernel pointer review"})
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("testdata/review-kernel/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	promptVersion := "kernel-review-fixture-v1"
	nodePrompt := "Independently compare the frozen report and numerical result."
	if len(longVersion) > 0 && longVersion[0] {
		promptVersion = strings.Repeat("r", 64)
		// Exact host-guidance fixture exercises the 129-character combination
		// through the actual compiler/chat/SQLite path. It supplies no approval
		// record; scientific rejection must still survive storage and restart.
		guidance, err := os.ReadFile("testdata/review-kernel/implementation-decision-guidance.txt")
		if err != nil {
			t.Fatal(err)
		}
		nodePrompt += string(guidance)
	}
	saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: project.ID, Definition: workflow.Definition{
		SchemaVersion: 1, Name: "Review actual Kernel envelope",
		Inputs: []workflow.Port{{Name: "context", Type: workflow.TypeObject, Required: true}, {Name: "computed_results", Type: workflow.TypeObject, Required: true}, {Name: "research_contract", Type: workflow.TypeObject, Required: true}},
		Nodes: []workflow.Node{
			{ID: "independent_review", Name: "Independent review", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: promptVersion, Prompt: nodePrompt, ReviewPolicy: workflow.AIReviewAuto, AllowedTools: []string{workflow.ResearchTaskReadTool}, OutputSchema: schema},
			{ID: "delivery_gate", Name: "Delivery gate", Kind: workflow.NodeTool, ToolName: "builtin.research.workflow.review.gate", Arguments: json.RawMessage(`{"subject":{}}`)},
		},
		Edges: []workflow.Edge{
			{FromNode: "$input", FromPort: "context", ToNode: "independent_review", ToPort: "context"},
			{FromNode: "$input", FromPort: "computed_results", ToNode: "independent_review", ToPort: "computedResults"},
			{FromNode: "$input", FromPort: "research_contract", ToNode: "independent_review", ToPort: "researchContract"},
			{FromNode: "independent_review", FromPort: "analysis", ToNode: "delivery_gate", ToPort: "review"},
		},
		Outputs: []workflow.Output{{Name: "review", Type: workflow.TypeObject, FromNode: "independent_review", FromPort: "analysis", Required: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	inputs := mustBootstrapJSON(t, map[string]any{
		"context":           map[string]any{"markdown": "Result: 5. Keep this report frozen during review."},
		"computed_results":  map[string]any{"kernelId": "kernel", "executionId": "execution", "status": "success", "valueType": "dict", "codeSha256": strings.Repeat("a", 64), "environmentFingerprint": strings.Repeat("b", 64), "value": map[string]any{"mean": 3}},
		"research_contract": map[string]any{"successCriteria": []string{"Report numerical results accurately"}},
	})
	started, err := app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: project.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: inputs, ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: conversation.PermissionFullAccess})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunFailed, 20*time.Second)
	if final.Run.ErrorCode != "RESEARCH_REVIEW_NOT_APPROVED" || len(final.AIExecutions) != 1 || final.Steps[0].Status != workflow.StepCompleted {
		t.Fatalf("scientific rejection turned into a program failure: %s %s", final.Run.ErrorCode, final.Run.ErrorMessage)
	}
	wantRequests := int32(1)
	if forgedFirst {
		wantRequests = 2
	}
	execution := final.AIExecutions[0]
	isLongVersion := len(longVersion) > 0 && longVersion[0]
	hasKernelVersion := strings.Contains(execution.PromptVersion, "review-kernel-path-v1")
	if isLongVersion {
		hasKernelVersion = strings.HasSuffix(execution.PromptVersion, "-kr1") || strings.Contains(execution.PromptVersion, "-kr1-")
	}
	if requests.Load() != wantRequests || execution.ModelTurns != int(wantRequests) || execution.Status != "completed" || !hasKernelVersion {
		t.Fatal("unexpected content correction/identity", requests.Load(), execution.ModelTurns, execution.Status, execution.PromptVersion)
	}
	if isLongVersion {
		baseHash := sha256.Sum256([]byte(promptVersion))
		expectedPrefix := "p-" + hex.EncodeToString(baseHash[:]) + "-"
		hasDecisionVersion := strings.HasSuffix(execution.PromptVersion, "-id1") || strings.Contains(execution.PromptVersion, "-id1-")
		if len(execution.PromptVersion) > 128 || !strings.HasPrefix(execution.PromptVersion, expectedPrefix) || !hasDecisionVersion || len(execution.PromptSHA256) != 64 || final.Run.Compilation.Nodes[0].PromptVersion != promptVersion {
			t.Fatal("long effective identity not persisted without changing frozen version", execution.PromptVersion)
		}
	}
	var review map[string]any
	if err := json.Unmarshal(execution.Output, &review); err != nil {
		t.Fatal(err)
	}
	finding := review["reviewFindings"].([]any)[0].(map[string]any)
	basis := finding["basis"].([]any)[1].(map[string]any)
	if review["approved"] != false || len(review["numericIssues"].([]any)) != 1 || basis["pointer"] != "/computedResults/value/mean" || basis["quote"] != "3" {
		t.Fatal("host changed the defect or failed exact alias", review)
	}
	var journal string
	if err := app.store.DB().QueryRow(`SELECT draft_text FROM model_turn_journal WHERE run_id=? AND turn_index=?`, execution.ChatRunID, wantRequests).Scan(&journal); err != nil || !strings.Contains(journal, `"pointer":"/computedResults/mean"`) {
		t.Fatal("original model pointer not preserved in journal", err, journal)
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
		if json.Unmarshal(event.Payload, &payload) != nil {
			t.Fatal("invalid normalization event")
		}
		for _, change := range payload.Changes {
			if change.Rule == "frozen_kernel_value_path" {
				audits++
				if payload.StepID != final.Steps[0].ID || payload.ExecutionID != execution.ID || payload.InputSHA256 != final.Steps[0].InputSHA256 || payload.RawOutputTextSHA256 != hex.EncodeToString(digest[:]) {
					t.Fatal("normalization audit not bound to the actual source", payload)
				}
			}
		}
	}
	if audits != 1 {
		t.Fatal("missing/duplicate pointer audit", audits)
	}
	before := string(mustBootstrapJSON(t, final.Steps))
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	reopened, err := app.WorkflowFacade.GetRun(project.ID, started.Run.ID)
	if err != nil || string(mustBootstrapJSON(t, reopened.Steps)) != before || requests.Load() != wantRequests {
		t.Fatal("restart changed report/review or reran the model", err)
	}
}

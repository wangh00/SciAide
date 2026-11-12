package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Real HTTP adapters and temporary SQLite only. Scientific decisions are the
// original scripted negative/positive review, never real-model acceptance.
func TestWideReviewNavigationHTTPRestartRepairAndGateAcrossProtocols(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) { testResearchHandoffRepair(t, protocol, true, false, false, true) })
	}
}

// Seed only a synthetic terminal v1 execution, never a real task or material.
// The production Retry path must leave that frozen row and prior work intact.
func TestFailedV1ReviewExplicitRetryCreatesV2WithoutReplayingPriorWork(t *testing.T) {
	var requests atomic.Int32
	server := newWorkflowAITestServer(t, func(_ map[string]any, prompt string) {
		requests.Add(1)
		if !strings.Contains(prompt, "Current independent-review completion contract (review-source-v2)") || strings.Contains(prompt, "(review-source-v1)") {
			t.Error("explicit retry reused the frozen v1 completion contract")
		}
	})
	options := Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}}
	app, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	app.Startup(context.Background())
	profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "review retry fixture", BaseURL: server.URL + "/v1", APIProtocol: modelprofile.ProtocolOpenAIChat, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true, ContextWindowTokens: 400000, AutoCompactTokenLimit: 360000}}, TimeoutSeconds: 10, Enabled: true, IsDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Legacy review technical retry fixture"})
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("testdata/review-kernel/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: project.ID, Definition: workflow.Definition{
		SchemaVersion: 1, Name: "Legacy review explicit retry",
		Inputs: []workflow.Port{{Name: "contract", Type: workflow.TypeObject, Required: true}},
		Nodes: []workflow.Node{
			{ID: "report_drafting", Name: "Saved report", Kind: workflow.NodeAIAnalysis, Arguments: json.RawMessage(`{}`), PromptVersion: "fixture-v1", Prompt: "Never rerun the saved report", ReviewPolicy: workflow.AIReviewAuto, OutputSchema: json.RawMessage(`{"type":"object"}`)},
			{ID: "independent_review", Name: "Independent review", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: "dynamic-independent-review-v9", Prompt: "Assess the original frozen report against its requirements", ReviewPolicy: workflow.AIReviewAuto, AllowedTools: []string{workflow.ResearchTaskReadTool}, OutputSchema: schema},
		},
		Edges:   []workflow.Edge{{FromNode: "report_drafting", FromPort: "analysis", ToNode: "independent_review", ToPort: "context"}, {FromNode: "$input", FromPort: "contract", ToNode: "independent_review", ToPort: "researchContract"}},
		Outputs: []workflow.Output{{Name: "review", Type: workflow.TypeObject, FromNode: "independent_review", FromPort: "analysis", Required: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	digest := func(value []byte) string { return fmt.Sprintf("%x", sha256.Sum256(value)) }
	now := time.Now().UTC()
	inputs := json.RawMessage(`{"contract":{"successCriteria":["State the saved limited result and its scope."]}}`)
	oldInput := json.RawMessage(`{"context":{"markdown":"Saved limited result; no empirical generalization."}}`)
	oldVersion := "dynamic-independent-review-v9|review-source-v1"
	oldPrompt := workflow.ReviewSourceInstruction(oldVersion) + "\n<stage_input>\n" + string(oldInput) + "\n</stage_input>"
	run := workflow.Run{ID: "review-retry-run", ResearchTaskID: "review-retry-run", ConversationID: "review-retry-conversation", ProjectID: project.ID, WorkflowID: saved.Workflow.ID, WorkflowVersionID: saved.Version.ID, WorkflowName: saved.Workflow.Name, WorkflowPurpose: saved.Workflow.Purpose, Status: workflow.RunFailed, PermissionMode: conversation.PermissionFullAccess, Inputs: inputs, InputsSHA256: digest(inputs), Compilation: saved.Version.Compilation, CompilationSHA256: saved.Version.CompilationSHA256, Outputs: json.RawMessage(`{}`), CurrentStep: 1, ErrorCode: "WORKFLOW_REVIEW_SOURCE_CONTEXT_LIMIT", ErrorMessage: "Synthetic terminal v1 navigation budget failure", CreatedAt: now, UpdatedAt: now, CompletedAt: &now}
	steps := []workflow.Step{
		{ID: "saved-report-step", WorkflowRunID: run.ID, NodeID: "report_drafting", NodeKind: workflow.NodeAIAnalysis, Ordinal: 0, Attempt: 1, Status: workflow.StepCompleted, Input: json.RawMessage(`{}`), Output: json.RawMessage(`{"analysis":{"markdown":"Saved limited result; no empirical generalization."}}`), UpdatedAt: now, CompletedAt: &now},
		{ID: "failed-review-step", WorkflowRunID: run.ID, NodeID: "independent_review", NodeKind: workflow.NodeAgentStage, Ordinal: 1, Attempt: 1, Status: workflow.StepFailed, Input: oldInput, InputSHA256: digest(oldInput), Output: json.RawMessage(`{}`), ErrorCode: run.ErrorCode, ErrorMessage: run.ErrorMessage, UpdatedAt: now, CompletedAt: &now},
	}
	conv := conversation.Conversation{ID: run.ConversationID, ProjectID: project.ID, Title: "Synthetic legacy review", PermissionMode: run.PermissionMode, ReasoningLevel: "medium", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewWorkflowRuntimeRepository(app.store.DB()).CreateResearchRun(context.Background(), run, steps, workflow.RuntimeEvent{ID: "review-retry-created", WorkflowRunID: run.ID, Type: "workflow.created", Payload: json.RawMessage(`{}`), CreatedAt: now}, conv); err != nil {
		t.Fatal(err)
	}
	stamp := now.Format(time.RFC3339Nano)
	_, err = app.store.DB().Exec(`INSERT INTO workflow_ai_executions(id,workflow_run_id,workflow_step_id,attempt,node_kind,model_profile_id,model_id,reasoning_level,prompt_version,prompt_text,prompt_sha256,input_sha256,allowed_tools_json,output_schema_json,output_schema_sha256,status,error_code,error_message,created_at,started_at,completed_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'failed',?,?,?,?,?,?)`, "frozen-v1-review", run.ID, steps[1].ID, 1, workflow.NodeAgentStage, profile.ID, workflowAITestModelID, "medium", oldVersion, oldPrompt, digest([]byte(oldPrompt)), digest(oldInput), string(mustBootstrapJSON(t, []string{workflow.ResearchTaskReadTool})), string(schema), digest(schema), run.ErrorCode, run.ErrorMessage, stamp, stamp, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
	snapshotOldRow := func() string {
		rows, err := app.store.DB().Query(`SELECT * FROM workflow_ai_executions WHERE id='frozen-v1-review'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		columns, err := rows.Columns()
		if err != nil || !rows.Next() {
			t.Fatal("missing frozen v1 row", err)
		}
		values, targets := make([]any, len(columns)), make([]any, len(columns))
		for i := range targets {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			t.Fatal(err)
		}
		return string(mustBootstrapJSON(t, values))
	}
	before, err := app.WorkflowFacade.GetRun(project.ID, run.ID)
	if err != nil || len(before.AIExecutions) != 1 {
		t.Fatal("terminal v1 fixture not visible", err)
	}
	frozenRow, priorStep := snapshotOldRow(), string(mustBootstrapJSON(t, before.Steps[0]))
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	reopened, err := app.WorkflowFacade.GetRun(project.ID, run.ID)
	if err != nil || reopened.Run.Status != workflow.RunFailed || requests.Load() != 0 || snapshotOldRow() != frozenRow {
		t.Fatal("restart resumed or changed the failed v1 execution", err)
	}
	if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: run.ID, StepID: steps[1].ID}); err != nil {
		t.Fatal(err)
	}
	complete := waitForWorkflowState(t, app, project.ID, run.ID, workflow.RunCompleted, 25*time.Second)
	if requests.Load() != 1 || len(complete.AIExecutions) != 2 || complete.Steps[1].Attempt != 2 || complete.Run.CompilationSHA256 != before.Run.CompilationSHA256 || string(complete.Run.Inputs) != string(before.Run.Inputs) || string(mustBootstrapJSON(t, complete.Steps[0])) != priorStep || snapshotOldRow() != frozenRow {
		t.Fatal("retry modified original work, reused v1, or reran an earlier stage", requests.Load(), complete.Run.ErrorCode)
	}
	for _, execution := range complete.AIExecutions {
		if execution.ID != "frozen-v1-review" && (execution.Attempt != 2 || workflow.ReviewSourceVersion(execution.PromptVersion) != workflow.ReviewSourceProtocolVersion || execution.Status != "completed") {
			t.Fatal("new explicit attempt did not use the current v2 review contract", execution)
		}
	}
}

func wideReviewFixtureArtifacts() map[string]any {
	rows := []any{}
	for i := 0; i < 100; i++ {
		values := map[string]any{}
		for j := 0; j < 25; j++ {
			values[fmt.Sprintf("originalSavedProviderCatalogAttribute%02d", j)] = fmt.Sprintf("synthetic-record-%d-value-%d", i, j)
		}
		rows = append(rows, map[string]any{"originalProviderSnapshot": values})
	}
	return map[string]any{"syntheticNavigationStressOnly": rows}
}

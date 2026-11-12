package bootstrap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/debugapi"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// This is a scripted HTTP/temporary-SQLite regression, not real-model or
// scientific acceptance. It never inherits the user's model configuration.
func TestInheritedDebugModelWorkflowRetryPreservesTaskAndLocalRepair(t *testing.T) {
	for _, missingStub := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing_stub_%t", missingStub), func(t *testing.T) {
			testInheritedDebugWorkflowRetry(t, missingStub)
		})
	}
}

func testInheritedDebugWorkflowRetry(t *testing.T, missingStub bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	data, err := os.ReadFile("testdata/inherited-debug-numeric.json")
	if err != nil {
		t.Fatal(err)
	}
	var prototype map[string]any
	if err := json.Unmarshal(data, &prototype); err != nil {
		t.Fatal(err)
	}
	original := prototype["context"].(map[string]any)["markdown"].(string)
	facts := prototype["dataPreflight"].(map[string]any)
	records := facts["syntheticMeasurementRecords"].([]any)
	var sum float64
	for _, record := range records {
		identity := record.(map[string]any)["syntheticRecordIdentity"].(map[string]any)
		sum += identity["measurement"].(map[string]any)["score"].(float64)
	}
	if len(records) != 4 || sum != 52 || sum/float64(len(records)) != 13 {
		t.Fatal("synthetic source no longer has the intended independent arithmetic oracle")
	}
	fixed := strings.Replace(original, "平均值为 15", "平均值为 13", 1)
	if original == fixed {
		t.Fatal("synthetic fixture lost its one intentional numeric defect")
	}
	var requests, reports, reviews atomic.Int32
	server := newWorkflowAITestServer(t, func(output map[string]any, prompt string) {
		requests.Add(1)
		input := workflowAITestStageInput(prompt)
		_, report := output["markdown"]
		for key := range output {
			delete(output, key)
		}
		if report {
			reports.Add(1)
			markdown := original
			// A second request alone must not make the scripted defect disappear.
			if input["_reviewCorrections"] != nil || input["_reviewRevision"] != nil {
				markdown = fixed
			}
			output["markdown"], output["confidence"] = markdown, "medium"
			output["claimSummary"] = []string{"Synthetic descriptive fixture only."}
			output["limitations"] = []string{"Four artificial records; no population or causal inference."}
			return
		}
		reviews.Add(1)
		fillInheritedNumericReviewFixture(t, output, input, original, fixed)
	})

	// Only this synthetic source is inherited. No SaveModelProfile call in the
	// target database may hide the lazy source-overlay FK preparation boundary.
	source, root := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "data"), 0700); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(source, "data", "sciaide.db")
	sourceDB, err := sqlite.Open(ctx, sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	profile := modelprofile.Profile{
		ID: "synthetic-inherited-numeric", Name: "Synthetic inherited numeric fixture",
		ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: server.URL + "/v1",
		APIProtocol: modelprofile.ProtocolOpenAIResponses, ModelID: workflowAITestModelID,
		Models:         []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true, ContextWindowTokens: 400000, AutoCompactTokenLimit: 360000}},
		TimeoutSeconds: 10, Enabled: true, IsDefault: true,
		CustomHeaders: map[string]string{"X-Synthetic-Private": "SYNTHETIC_HEADER_SENTINEL"},
		SecretRef:     fmt.Sprintf("synthetic-unused:%x", sha256.Sum256([]byte(source))),
	}
	err = sqlite.NewModelProfileRepository(sourceDB.DB()).Save(ctx, profile)
	closeErr := sourceDB.Close()
	if err != nil || closeErr != nil {
		t.Fatal("save synthetic source", err, closeErr)
	}
	sourceBefore, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	session := startInheritedDebugFixture(t, ctx, deadline, root, source)
	app := session.app
	prepare := func() {
		profiles, err := app.ModelFacade.ListModelProfiles()
		if err != nil || len(profiles) != 1 || profiles[0].ID != profile.ID || !profiles[0].Enabled {
			t.Fatal("inherited model admission failed", err)
		}
		stub, err := sqlite.NewModelProfileRepository(app.store.DB()).Get(ctx, profile.ID)
		if err != nil || stub.BaseURL != "https://research-debug.invalid/v1" || len(stub.CustomHeaders) != 0 || stub.SecretRef == profile.SecretRef || !strings.HasPrefix(stub.SecretRef, "research-debug-unused:") {
			t.Fatal("source overlay did not prepare only a redacted local FK stub", err)
		}
	}
	if !missingStub {
		prepare()
	}
	project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Inherited numeric fixture"})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: project.ID, Definition: inheritedDebugNumericDefinition(t, app)})
	if err != nil {
		t.Fatal(err)
	}
	started, err := app.WorkflowFacade.Start(workflow.StartCommand{
		ProjectID: project.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID,
		Inputs:         mustBootstrapJSON(t, map[string]any{"draft": prototype["context"], "facts": prototype["dataPreflight"], "contract": prototype["researchContract"], "request": prototype["originalRequest"]}),
		ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: conversation.PermissionFullAccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	if missingStub {
		failed := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunFailed, 15*time.Second)
		if failed.Run.ErrorCode != "WORKFLOW_AI_START_FAILED" || !strings.Contains(failed.Run.ErrorMessage, "FOREIGN KEY constraint failed") || len(failed.AIExecutions) != 0 || requests.Load() != 0 {
			t.Fatal("missing source admission did not fail before model HTTP", failed.Run.ErrorCode, requests.Load())
		}
		prepare()
		if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: started.Run.ID, StepID: inheritedDebugStep(t, failed, "report_drafting").ID}); err != nil {
			t.Fatal(err)
		}
	}
	negative := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunFailed, 15*time.Second)
	if negative.Run.ErrorCode != "RESEARCH_REVIEW_NOT_APPROVED" || requests.Load() != 2 {
		for _, execution := range negative.AIExecutions {
			var details string
			if app.store.DB().QueryRow("SELECT error_details FROM runs WHERE id=?", execution.ChatRunID).Scan(&details) == nil && details != "" {
				t.Logf("synthetic run diagnostics: %.2400s", details)
			}
		}
		t.Fatalf("scientific rejection did not reach the original gate: %s: %s (requests=%d)", negative.Run.ErrorCode, negative.Run.ErrorMessage, requests.Load())
	}
	initialReport := inheritedDebugAnalysis(t, inheritedDebugStep(t, negative, "report_drafting"))
	negativeReview := inheritedDebugAnalysis(t, inheritedDebugStep(t, negative, "independent_review"))
	if initialReport["markdown"] != original || negativeReview["approved"] != false {
		t.Fatal("negative review lost the original numeric defect")
	}
	finding := negativeReview["reviewFindings"].([]any)[0].(map[string]any)
	meanBasis := finding["basis"].([]any)[0].(map[string]any)
	if finding["status"] != "open" || meanBasis["pointer"] != "/dataPreflight/syntheticMeasurementSummary/singleOutcome/score/descriptiveStatistics/mean" || meanBasis["quote"] != "13" {
		t.Fatal("negative review did not select the original numeric scalar")
	}
	objectCounts := func() map[string]int {
		counts := map[string]int{}
		for _, table := range []string{"projects", "research_tasks", "workflow_runs", "conversations"} {
			var count int
			if err := app.store.DB().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
				t.Fatal(err)
			}
			counts[table] = count
		}
		return counts
	}
	countsBefore := objectCounts()
	recommendation := negative.ReviewRevisionRecommendation
	if recommendation == nil || recommendation.NodeID != "report_drafting" {
		t.Fatal("numeric finding has no original report-owner revision recommendation")
	}

	// Close/reopen preserves the TWO admitted requests and never resumes a
	// failed workflow merely because the debug root was reopened.
	session.close()
	session = startInheritedDebugFixture(t, ctx, deadline, root, source)
	app = session.app
	reopened, err := app.WorkflowFacade.GetRun(project.ID, started.Run.ID)
	if err != nil || reopened.Run.Status != workflow.RunFailed || requests.Load() != 2 || !bytes.Equal(mustBootstrapJSON(t, negative.AIExecutions), mustBootstrapJSON(t, reopened.AIExecutions)) {
		t.Fatal("reopen changed or automatically resumed the rejected task", err)
	}
	budgetBytes, err := os.ReadFile(filepath.Join(root, "model-budget.json"))
	var budget struct{ Used, Limit int }
	if err != nil || json.Unmarshal(budgetBytes, &budget) != nil || budget.Used != 2 || budget.Limit != 4 {
		t.Fatal("reopen reset the durable request ledger", err)
	}
	gate := inheritedDebugStep(t, reopened, "delivery_gate")
	cmd := workflow.RetryCommand{ProjectID: project.ID, RunID: started.Run.ID, StepID: gate.ID, UseRecommendation: true, ExpectedReviewSHA256: strings.Repeat("0", 64)}
	if _, err := app.WorkflowFacade.Retry(cmd); err == nil || requests.Load() != 2 {
		t.Fatal("stale scientific-review confirmation was accepted or invoked a model")
	}
	prepare()
	cmd.ExpectedReviewSHA256 = recommendation.ReviewOutputSHA256
	if _, err := app.WorkflowFacade.Retry(cmd); err != nil {
		t.Fatal(err)
	}
	final := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunCompleted, 15*time.Second)
	if requests.Load() != 4 || reports.Load() != 2 || reviews.Load() != 2 {
		t.Fatal("unexpected replay or extra model request", requests.Load(), reports.Load(), reviews.Load())
	}
	if final.Run.ID != negative.Run.ID || final.Run.ResearchTaskID != negative.Run.ResearchTaskID || final.Run.ConversationID != negative.Run.ConversationID || final.Run.InputsSHA256 != negative.Run.InputsSHA256 || final.Run.CompilationSHA256 != negative.Run.CompilationSHA256 {
		t.Fatal("repair replaced the original task, frozen input or graph")
	}
	if !bytes.Equal(mustBootstrapJSON(t, countsBefore), mustBootstrapJSON(t, objectCounts())) || final.ArtifactCount != 0 {
		t.Fatal("repair created another project/task/workflow/conversation or publication")
	}
	finalReport := inheritedDebugAnalysis(t, inheritedDebugStep(t, final, "report_drafting"))
	initialReport["markdown"] = fixed
	if !bytes.Equal(mustBootstrapJSON(t, initialReport), mustBootstrapJSON(t, finalReport)) {
		t.Fatal("repair changed unrelated report fields")
	}
	for _, old := range negative.AIExecutions {
		found := false
		for _, current := range final.AIExecutions {
			if old.ID == current.ID {
				found = bytes.Equal(mustBootstrapJSON(t, old), mustBootstrapJSON(t, current))
			}
		}
		if !found {
			t.Fatal("repair changed or lost an earlier AI execution")
		}
	}
	reviewBindings := map[string]bool{}
	for _, execution := range final.AIExecutions {
		if execution.ModelTurns != 1 {
			t.Fatal("controlled stage needed unexpected recovery requests")
		}
		if !strings.Contains(execution.PromptVersion, "dynamic-independent-review-v9") {
			continue
		}
		if workflow.ReviewSourceVersion(execution.PromptVersion) != workflow.ReviewSourceProtocolVersion {
			t.Fatal("review did not use the current source-selection protocol")
		}
		var text string
		if err := app.store.DB().QueryRow("SELECT draft_text FROM model_turn_journal WHERE run_id=? AND turn_index=1", execution.ChatRunID).Scan(&text); err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if json.Unmarshal([]byte(text), &wire) != nil || len(wire) != 7 {
			t.Fatal("scripted review did not submit the native seven-field contract")
		}
		basis := wire["reviewFindings"].([]any)[0].(map[string]any)["basis"].([]any)[0].(map[string]any)
		ids, ok := basis["sourceSegmentIds"].([]any)
		if !ok || len(basis) != 1 || len(ids) != 1 {
			t.Fatal("numeric evidence was not selected as one complete source scalar")
		}
		id := ids[0].(string)
		binding := id[:strings.LastIndex(id, "_")]
		if reviewBindings[binding] {
			t.Fatal("rereview reused another execution's source binding")
		}
		reviewBindings[binding] = true
	}
	if len(reviewBindings) != 2 {
		t.Fatal("missing native negative/positive review bindings")
	}
	positive := inheritedDebugAnalysis(t, inheritedDebugStep(t, final, "independent_review"))
	resolved := positive["reviewFindings"].([]any)[0].(map[string]any)
	if positive["approved"] != true || resolved["id"] != finding["id"] || resolved["status"] != "resolved" || resolved["origin"] != "existing" {
		t.Fatal("rereview did not resolve the original numeric finding")
	}
	expectedReportAttempt := 2
	if missingStub {
		expectedReportAttempt++
	}
	if inheritedDebugStep(t, final, "report_drafting").Attempt != expectedReportAttempt || inheritedDebugStep(t, final, "independent_review").Attempt != 2 {
		t.Fatal("unexpected report/review attempt count")
	}
	gate = inheritedDebugStep(t, final, "delivery_gate")
	var gateInput map[string]any
	_ = json.Unmarshal(gate.Input, &gateInput)
	var reviewInput any
	_ = json.Unmarshal(inheritedDebugStep(t, final, "independent_review").Input, &reviewInput)
	if !bytes.Equal(mustBootstrapJSON(t, gateInput["subject"]), mustBootstrapJSON(t, reviewInput)) {
		t.Fatal("gate is not bound to the full frozen review input")
	}
	session.close()
	after, err := os.ReadFile(sourcePath)
	if err != nil || !bytes.Equal(sourceBefore, after) {
		t.Fatal("inherited source database was changed", err)
	}
	if _, err := os.Stat(filepath.Join(root, "debug-connection.json")); !os.IsNotExist(err) {
		t.Fatal("debug descriptor survived clean shutdown", err)
	}
	budgetBytes, err = os.ReadFile(filepath.Join(root, "model-budget.json"))
	if err != nil || json.Unmarshal(budgetBytes, &budget) != nil || budget.Used != 4 || budget.Limit != 4 {
		t.Fatal("final request ledger differs from four controlled HTTP requests", err)
	}
}

type inheritedDebugFixtureSession struct {
	app   *Application
	close func()
}

func startInheritedDebugFixture(t *testing.T, ctx context.Context, deadline time.Time, root, source string) inheritedDebugFixtureSession {
	t.Helper()
	space, err := debugapi.OpenWorkspace(root, source, true, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	sessionCtx, cancel := context.WithCancel(ctx)
	app, err := New(Options{RootDir: space.Root, EventPublisher: workflowAITestPublisher{}, ModelRequestGate: space.Gate, ModelProfileOverlay: space.ProfileOverlay, DisableAuxiliaryModels: true})
	if err != nil {
		cancel()
		_ = space.Close()
		t.Fatal(err)
	}
	app.Startup(sessionCtx)
	server, err := debugapi.Start(sessionCtx, debugapi.Services{Projects: app.ProjectFacade, Conversations: app.ConversationFacade, Chat: app.ChatFacade, Workflow: app.WorkflowFacade, Models: app.ModelFacade, Permissions: app.PermissionFacade}, space, debugapi.Options{MaxDuration: time.Minute, Deadline: deadline, AllowFullAccess: true, OnStop: func() { app.BeginShutdown(); cancel() }, OnClose: app.Close})
	if err != nil {
		cancel()
		_ = app.Close()
		_ = space.Close()
		t.Fatal(err)
	}
	var once sync.Once
	close := func() { once.Do(func() { server.Close(); cancel(); _ = app.Close(); _ = space.Close() }) }
	t.Cleanup(close)
	return inheritedDebugFixtureSession{app: app, close: close}
}

func inheritedDebugNumericDefinition(t *testing.T, app *Application) workflow.Definition {
	t.Helper()
	var reportSchema json.RawMessage
	for _, node := range referenceWorkflowTemplate(t, app, "trusted-research-closure").Definition.Nodes {
		if node.ID == "draft_report" {
			reportSchema = node.OutputSchema
		}
	}
	reviewSchema, err := os.ReadFile("testdata/review-kernel/schema.json")
	if err != nil || len(reportSchema) == 0 {
		t.Fatal("original report/review schema missing", err)
	}
	return workflow.Definition{
		SchemaVersion: 1, Name: "Inherited debug numeric repair fixture",
		Inputs: []workflow.Port{{Name: "draft", Type: workflow.TypeObject, Required: true}, {Name: "facts", Type: workflow.TypeObject, Required: true}, {Name: "contract", Type: workflow.TypeObject, Required: true}, {Name: "request", Type: workflow.TypeString, Required: true}},
		Nodes: []workflow.Node{
			{ID: "report_drafting", Kind: workflow.NodeAgentStage, Name: "Synthetic draft intake and local repair", Arguments: json.RawMessage(`{}`), PromptVersion: "dynamic-report-v6", Prompt: "Preserve the supplied synthetic draft until a confirmed independent correction; repair only the substantiated defect.", ReviewPolicy: workflow.AIReviewAuto, AllowedTools: []string{workflow.ResearchTaskReadTool}, OutputSchema: reportSchema},
			{ID: "independent_review", Kind: workflow.NodeAgentStage, Name: "Independent numeric review", Arguments: json.RawMessage(`{}`), PromptVersion: "dynamic-independent-review-v9", Prompt: "Independently compare the supplied synthetic records and report; select actual current evidence and do not assume approval.", ReviewPolicy: workflow.AIReviewAuto, AllowedTools: []string{workflow.ResearchTaskReadTool}, OutputSchema: reviewSchema},
			{ID: "delivery_gate", Kind: workflow.NodeTool, Name: "Original delivery gate", ToolName: "builtin.research.workflow.review.gate", Arguments: json.RawMessage(`{}`)},
		},
		Edges: []workflow.Edge{
			{FromNode: "$input", FromPort: "draft", ToNode: "report_drafting", ToPort: "context"},
			{FromNode: "$input", FromPort: "facts", ToNode: "report_drafting", ToPort: "dataPreflight"}, {FromNode: "$input", FromPort: "facts", ToNode: "independent_review", ToPort: "dataPreflight"},
			{FromNode: "$input", FromPort: "contract", ToNode: "report_drafting", ToPort: "researchContract"}, {FromNode: "$input", FromPort: "contract", ToNode: "independent_review", ToPort: "researchContract"},
			{FromNode: "$input", FromPort: "request", ToNode: "report_drafting", ToPort: "originalRequest"}, {FromNode: "$input", FromPort: "request", ToNode: "independent_review", ToPort: "originalRequest"},
			{FromNode: "report_drafting", FromPort: "analysis", ToNode: "independent_review", ToPort: "context"}, {FromNode: "report_drafting", FromPort: "analysis", ToNode: "delivery_gate", ToPort: "subject"}, {FromNode: "independent_review", FromPort: "analysis", ToNode: "delivery_gate", ToPort: "review"},
		},
		Outputs: []workflow.Output{{Name: "report", Type: workflow.TypeObject, FromNode: "report_drafting", FromPort: "analysis", Required: true}, {Name: "gate", Type: workflow.TypeObject, FromNode: "delivery_gate", FromPort: "structured", Required: true}},
	}
}

func fillInheritedNumericReviewFixture(t *testing.T, output, input map[string]any, original, fixed string) {
	t.Helper()
	markdown := input["context"].(map[string]any)["markdown"].(string)
	approved := markdown == fixed
	if markdown != original && !approved {
		t.Error("scripted review received an unexpected report")
	}
	output["approved"], output["confidence"] = approved, "medium"
	output["verifiedClaims"] = []string{"Four artificial records support only a descriptive arithmetic check."}
	output["limitations"] = []string{"Synthetic data; no empirical, population or causal conclusion."}
	output["suggestions"] = []string{}
	checks := []any{}
	for index, value := range input["acceptanceCriteria"].([]any) {
		criterion := value.(map[string]any)
		status := "met"
		if index == 0 && !approved {
			status = "not_met"
		}
		checks = append(checks, map[string]any{"criterionId": criterion["id"], "status": status, "basis": "Compare only the frozen artificial scores, arithmetic mean and stated scope."})
	}
	output["acceptanceChecks"] = checks
	for _, category := range []string{"citationIssues", "methodIssues", "numericIssues", "unsupportedClaims", "requiredCorrections", "revisionPlan"} {
		output[category] = []any{}
	}
	history := input["reviewHistory"].(map[string]any)
	prefix, _ := history["issuePrefix"].(string)
	id, status, origin, index, change := prefix+"1", "open", "new", 0, "The current frozen mean contradicts the original draft's mean."
	if approved {
		previous := history["findings"].([]any)
		if len(previous) != 1 {
			t.Error("rereview lost the one original numeric finding")
		} else {
			id = previous[0].(map[string]any)["id"].(string)
		}
		status, origin, index, change = "resolved", "existing", -1, "The corrected draft now agrees with the same frozen mean."
	}
	const summary = "The original draft's mean 15 differs from 52/4=13."
	output["reviewFindings"] = []any{map[string]any{
		"id": id, "status": status, "origin": origin, "category": "numericIssues", "summary": summary,
		"location": "context.markdown arithmetic mean", "reason": "10+12+14+16=52 and 52/4=13.", "changeReason": change, "correctionIndex": index,
		"basis": []any{map[string]any{"pointer": "/dataPreflight/syntheticMeasurementSummary/singleOutcome/score/descriptiveStatistics/mean", "quote": "13"}, map[string]any{"pointer": "/context/markdown", "quote": markdown}},
	}}
	if !approved {
		output["numericIssues"] = []string{summary}
		output["requiredCorrections"] = []string{"Change only the draft mean from 15 to 13; preserve unrelated content and metadata."}
		output["revisionPlan"] = []any{map[string]any{"correctionIndex": 0, "nodeId": "report_drafting", "reason": "The defect is confined to the report's mean.", "whyNotLater": "The report owner is the last sufficient producer of this number.", "needsUserInput": false, "requiredInput": ""}}
	}
}

func inheritedDebugStep(t *testing.T, d workflow.RunDetail, node string) workflow.Step {
	t.Helper()
	for _, s := range d.Steps {
		if s.NodeID == node {
			return s
		}
	}
	t.Fatalf("missing workflow step %q", node)
	return workflow.Step{}
}

func inheritedDebugAnalysis(t *testing.T, s workflow.Step) map[string]any {
	t.Helper()
	var output map[string]any
	if err := json.Unmarshal(s.Output, &output); err != nil {
		t.Fatal(err)
	}
	return output["analysis"].(map[string]any)
}

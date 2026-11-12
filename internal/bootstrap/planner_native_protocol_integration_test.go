package bootstrap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestNativePlannerMissingActionAcrossProtocolsPreservesRawAndStopsOnce(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		for _, kind := range []string{"valid_json", "bad_json", "empty", "refusal"} {
			t.Run(string(protocol)+"/"+kind, func(t *testing.T) {
				var requests atomic.Int32
				var served atomic.Value
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
					requests.Add(1)
					if !plannerActionRequiredOnWire(protocol, body) {
						t.Error("new built-in planner did not actually require native action with Skill IO")
					}
					prompt := workflowAITestPrompt(contextPipelineCanonicalRequest(t, body))
					if strings.Contains(prompt, "End the response with exactly one fenced json object") || strings.Contains(prompt, "必须在同一响应末尾提交") || strings.Contains(prompt, "你是 SciAide 科研模式的动态研究架构师。") {
						t.Error("actual first request retained conflicting trusted transport/identity wording")
					}
					text := ""
					switch kind {
					case "valid_json":
						data, _ := json.Marshal(workflowAISchemaFixture(mustWorkflowAITestSchema(t, prompt)))
						text = "  ```json\n" + string(data) + "\n```  "
					case "bad_json":
						text = `{"clarification":{},"routes":[]}`
					case "refusal":
						text = "I cannot comply with this request."
					}
					served.Store(text)
					// n=2 deliberately omits the helper's unrelated opening commentary.
					emitQuestionHandoffFixture(t, w, protocol, 2, text, "", "")
				}))
				defer server.Close()
				options := Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}, DisableAuxiliaryModels: true}
				app, err := New(options)
				if err != nil {
					t.Fatal(err)
				}
				defer app.Close()
				app.Startup(context.Background())
				profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "native contract fixture", BaseURL: server.URL, APIProtocol: protocol, ModelID: workflowAITestModelID, TimeoutSeconds: 5, Enabled: true, IsDefault: true})
				if err != nil {
					t.Fatal(err)
				}
				project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "原生调用协议分流"})
				if err != nil {
					t.Fatal(err)
				}
				started, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "只设计笔记方式与即时回忆的方案，不执行研究或检索。", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
				if err != nil {
					t.Fatal(err)
				}
				end := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunFailed, 15*time.Second)
				want := "WORKFLOW_AI_NATIVE_ACTION_MISSING"
				if kind == "refusal" {
					want = "WORKFLOW_AI_MODEL_REFUSED"
				}
				if end.Run.ErrorCode != want || requests.Load() != 1 || len(end.AIExecutions) != 1 {
					t.Fatal("missing native action entered content/stream retry or lost refusal priority", end.Run.ErrorCode, requests.Load())
				}
				execution := end.AIExecutions[0]
				journal, found, err := sqlite.NewRunRepository(app.store.DB()).LatestModelTurnJournal(context.Background(), execution.ChatRunID)
				if err != nil || !found || journal.DraftText != served.Load().(string) || journal.Status != "completed" {
					t.Fatal("original complete text was changed or not journaled", err)
				}
				var version, promptHash, schemaHash string
				if err := app.store.DB().QueryRow(`SELECT prompt_version,prompt_sha256,output_schema_sha256 FROM workflow_ai_executions WHERE id=?`, execution.ID).Scan(&version, &promptHash, &schemaHash); err != nil || !workflow.IsNativePlannerProtocol(version) {
					t.Fatal("new execution lost its explicit protocol identity", err)
				}
				var calls int
				if err := app.store.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE run_id=?`, execution.ChatRunID).Scan(&calls); err != nil || calls != 0 || end.Recovery == nil || end.Recovery.FailureKind != "model_protocol" && kind != "refusal" {
					t.Fatal("protocol failure created IO or hid its technical recovery classification", calls, err)
				}
				if protocol == modelprofile.ProtocolOpenAIResponses && kind == "valid_json" {
					// Restart is read-only for a stopped case. Only an explicit retry
					// may create a new execution, never rewrite the failed one.
					before := nativePlannerExecutionSnapshot(t, app, execution.ID)
					if err := app.Close(); err != nil {
						t.Fatal(err)
					}
					restarted, err := New(options)
					if err != nil {
						t.Fatal(err)
					}
					defer restarted.Close()
					restarted.Startup(context.Background())
					if requests.Load() != 1 || nativePlannerExecutionSnapshot(t, restarted, execution.ID) != before {
						t.Fatal("restart resumed a stopped stage or rewrote its frozen history")
					}
					if _, err := restarted.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: end.Run.ID, StepID: end.Steps[0].ID, Note: "controlled fixture explicit retry"}); err != nil {
						t.Fatal(err)
					}
					retried := waitForWorkflowState(t, restarted, project.ID, end.Run.ID, workflow.RunFailed, 15*time.Second)
					if requests.Load() != 2 || len(retried.AIExecutions) != 2 || retried.AIExecutions[1].Attempt != 2 || nativePlannerExecutionSnapshot(t, restarted, execution.ID) != before {
						t.Fatal("explicit retry changed old execution or did not create one bounded new attempt")
					}
				}
			})
		}
	}
}

func nativePlannerExecutionSnapshot(t *testing.T, app *Application, executionID string) string {
	t.Helper()
	var version, prompt, promptHash, inputHash, schema, schemaHash, output, outputJSON, code string
	if err := app.store.DB().QueryRow(`SELECT prompt_version,prompt_text,prompt_sha256,input_sha256,output_schema_json,output_schema_sha256,output_text,output_json,error_code FROM workflow_ai_executions WHERE id=?`, executionID).Scan(&version, &prompt, &promptHash, &inputHash, &schema, &schemaHash, &output, &outputJSON, &code); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal([]string{version, prompt, promptHash, inputHash, schema, schemaHash, output, outputJSON, code})
	return string(data)
}

func mustWorkflowAITestSchema(t *testing.T, prompt string) map[string]any {
	t.Helper()
	schema, err := workflowAITestSchema(prompt)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

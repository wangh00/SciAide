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

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Real HTTP adapters, frozen host validation, SQLite and explicit retry. The
// model responses are scripted; this is not a real-model scientific evaluation.
func TestEarlyStageSubmissionTransitionAcrossProtocols(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		for _, mode := range []string{"corrected", "raw_compatible", "exhausted_then_retry", "large_control_audit"} {
			t.Run(string(protocol)+"/"+mode, func(t *testing.T) {
				testEarlyStageSubmissionTransition(t, protocol, mode)
			})
		}
	}
}

func testEarlyStageSubmissionTransition(t *testing.T, protocol modelprofile.APIProtocol, mode string) {
	t.Helper()
	good := `{"summary":"保留已完成的研究证据","limitations":["受控测试，不是科学质量证明"]}`
	const originalCommentary = " \nOriginal visible control commentary.\r\n "
	if mode == "large_control_audit" {
		data, _ := json.Marshal(map[string]any{"summary": strings.Repeat("<", 30000), "limitations": []string{}})
		good = string(data)
	}
	bad := "```json\n" + good + "\"\n```"
	var requests atomic.Int32
	var retrying atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request struct {
			Tools      []json.RawMessage `json:"tools"`
			ToolChoice json.RawMessage   `json:"tool_choice"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Error(err)
			return
		}
		n := int(requests.Add(1))
		if mode == "large_control_audit" {
			if n != 1 || !stageToolSelectionOptionalOnWire(protocol, body) || !stageToolOfferedOnWire(protocol, body) {
				t.Error("large native candidate was not admitted without additional requests")
			}
			emitPlannerControlWithText(t, w, protocol, originalCommentary, good)
			return
		}
		text, toolName, arguments := bad, "", ""
		if n > 1 && !retrying.Load() {
			if !singleStageToolForcedOnWire(protocol, body) {
				t.Error("early rejection did not enter the single forced submission interface")
			}
			prompt := workflowAITestPrompt(contextPipelineCanonicalRequest(t, body))
			if !strings.Contains(prompt, "Host submission validation failed") || !strings.Contains(prompt, "frozen") {
				t.Error("same-stage correction lost validation or frozen-contract guidance")
			}
			// Completed controls must not become unpaired native IO on any wire.
			for _, liveBlock := range []string{`"type":"function_call"`, `"type":"function_call_output"`, `"type":"tool_use"`, `"type":"tool_result"`, `"role":"tool"`, `"tool_calls":`} {
				if strings.Contains(string(body), liveBlock) {
					t.Errorf("submission correction replayed an orphan native control: %s", liveBlock)
				}
			}
			text, toolName, arguments = "", "submit_stage_result", good
			if mode == "raw_compatible" {
				text, toolName, arguments = "```json\n"+good+"\n```", "", ""
			}
			if mode == "exhausted_then_retry" {
				arguments = `{"summary":{},"limitations":[]}`
				if n == 3 {
					text, toolName, arguments = bad, "", ""
				}
			}
		} else if retrying.Load() {
			if !stageToolSelectionOptionalOnWire(protocol, body) {
				t.Error("explicit retry inherited a stale correction latch")
			}
			text = good // A valid early result must still need no extra model turn.
		}
		emitQuestionHandoffFixture(t, w, protocol, n, text, toolName, arguments)
	}))
	defer server.Close()
	options := Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}}
	app, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	app.Startup(context.Background())
	profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "submission transition fixture", BaseURL: server.URL + "/v1", APIProtocol: protocol, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true, ContextWindowTokens: 272000, AutoCompactTokenLimit: 244800}}, TimeoutSeconds: 10, Enabled: true, IsDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "isolated early submission"})
	if err != nil {
		t.Fatal(err)
	}
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary","limitations"],"properties":{"summary":{"type":"string"},"limitations":{"type":"array","items":{"type":"string"}}}}`)
	saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: project.ID, Definition: workflow.Definition{
		SchemaVersion: 1, Name: "Early method result fixture",
		Inputs:  []workflow.Port{{Name: "context", Type: workflow.TypeObject, Required: true}},
		Nodes:   []workflow.Node{{ID: "method_selection", Name: "Method selection", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: "early-submission-fixture-v1", Prompt: "Summarize the frozen context; disclose limitations honestly.", ReviewPolicy: workflow.AIReviewAuto, AllowedTools: []string{}, OutputSchema: schema}},
		Edges:   []workflow.Edge{{FromNode: "$input", FromPort: "context", ToNode: "method_selection", ToPort: "context"}},
		Outputs: []workflow.Output{{Name: "result", Type: workflow.TypeObject, FromNode: "method_selection", FromPort: "analysis", Required: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	started, err := app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: project.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"context":{"evidence":"frozen local fixture"}}`), ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: conversation.PermissionFullAccess})
	if err != nil {
		t.Fatal(err)
	}
	wantStatus := workflow.RunCompleted
	if mode == "exhausted_then_retry" {
		wantStatus = workflow.RunFailed
	}
	final := waitForWorkflowState(t, app, project.ID, started.Run.ID, wantStatus, 25*time.Second)
	execution := final.AIExecutions[0]
	var original string
	wantOriginal := bad
	if mode == "large_control_audit" {
		wantOriginal = good
	} else if protocol == modelprofile.ProtocolOpenAIResponses {
		// Structured-stage journals preserve raw streamed text deltas without
		// adding the paragraph separators used by ordinary conversation UI.
		wantOriginal = "正在核对文献摘要。" + bad
	}
	if err := app.store.DB().QueryRow(`SELECT draft_text FROM model_turn_journal WHERE run_id=? AND turn_index=1`, execution.ChatRunID).Scan(&original); err != nil || original != wantOriginal {
		t.Fatalf("raw rejected provider candidate was not preserved: err=%v got=%q want=%q", err, original, wantOriginal)
	}
	if mode == "exhausted_then_retry" {
		var diagnostic string
		if err := app.store.DB().QueryRow(`SELECT error_details FROM runs WHERE id=?`, execution.ChatRunID).Scan(&diagnostic); err != nil {
			t.Fatal(err)
		}
		if requests.Load() != 3 || execution.ModelTurns != 3 || final.Run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || !strings.Contains(final.Run.ErrorMessage, "提交格式") || !strings.Contains(final.Run.ErrorMessage, "不代表") || !strings.Contains(diagnostic, "UTF-8") || execution.OutputSHA256 != "" {
			t.Fatal("bad format was accepted, mislabeled, or given a renewed retry budget", final.Run.ErrorCode, final.Run.ErrorMessage, execution.ModelTurns, execution.OutputSHA256, diagnostic, requests.Load())
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
			t.Fatal("restart rewrote the failure or silently invoked the model", err)
		}
		retrying.Store(true)
		if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: final.Run.ID, StepID: final.Steps[0].ID}); err != nil {
			t.Fatal(err)
		}
		final = waitForWorkflowState(t, app, project.ID, final.Run.ID, workflow.RunCompleted, 25*time.Second)
		if requests.Load() != 4 || len(final.AIExecutions) != 2 || final.AIExecutions[1].Attempt != 2 || final.AIExecutions[1].ModelTurns != 1 || final.Steps[0].InputSHA256 != reopened.Steps[0].InputSHA256 {
			t.Fatal("explicit retry did not resume the same frozen stage", final.Run, requests.Load())
		}
	} else if mode == "large_control_audit" {
		var commentary string
		if err := app.store.DB().QueryRow(`SELECT submission_commentary FROM model_turn_journal WHERE run_id=? AND turn_index=1`, execution.ChatRunID).Scan(&commentary); err != nil || commentary != originalCommentary || requests.Load() != 1 || execution.ModelTurns != 1 {
			t.Fatal("large native result/commentary was expanded, lost or retried", err, requests.Load())
		}
	} else if requests.Load() != 2 || execution.ModelTurns != 2 || execution.Attempt != 1 {
		t.Fatal("early correction restarted the task or performed extra model work", execution, requests.Load())
	}
	var ioCalls int
	if err := app.store.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE run_id IN (SELECT b.chat_run_id FROM workflow_ai_chat_runs b JOIN workflow_ai_executions e ON e.id=b.execution_id WHERE e.workflow_run_id=?)`, final.Run.ID).Scan(&ioCalls); err != nil || ioCalls != 0 {
		t.Fatal("submission control executed as research IO", ioCalls, err)
	}
}

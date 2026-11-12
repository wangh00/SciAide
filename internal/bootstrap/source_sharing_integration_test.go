package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
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
	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Three production adapters, SQLite, restart/retry, original task-reader pages
// and same-run schema repair. Missing provider usage makes admission exercise
// first-turn accounting, not borrowed measurements. The model is scripted.
func TestRepeatedSourcesHTTPReadRestartRetryAndFullSubmission(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) {
			query := "FIRST-QUERY-" + strings.Repeat("完整检索与来源范围😀", 110) + "-LAST-QUERY"
			entries := []any{}
			for i := 0; i < 60; i++ {
				entries = append(entries, map[string]any{"documentId": fmt.Sprintf("document-%d", i), "queries": []string{query}, "fullTextRead": false})
			}
			inputs := mustBootstrapJSON(t, map[string]any{"sources": map[string]any{"coverage": entries}, "goal": "完整用户目标不能代换", "report": "当前交付不能截断或代换"})
			var requests atomic.Int32
			var firstRawInput, firstRawHash string
			var gotOriginalRead atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				n := int(requests.Add(1))
				canonical := contextPipelineCanonicalRequest(t, body)
				prompt := workflowAITestPrompt(canonical)
				input := workflowAITestStageInput(prompt)
				if input["originalRequest"] != "完整用户目标不能代换" || input["context"] != "当前交付不能截断或代换" {
					t.Error("complete goal/report or declared source encoding lost")
				}
				source := input["researchSourceContext"].(map[string]any)
				// Budget admission may replace source sharing with original-input
				// paging. Both are lossless wire views, not a different inventory.
				// This fixture reads one relevant original page, not every duplicate.
				switch coverage := source["retrievedEvidence"].(map[string]any)["coverage"].(type) {
				case []any:
					if !strings.Contains(prompt, "source-sharing-v1") || !strings.Contains(prompt, "stage_input_shared_paths") || len(coverage) != 60 || coverage[0].(map[string]any)["queries"].([]any)[0] != query || coverage[59].(map[string]any)["queries"].(map[string]any)["$same"] != float64(0) {
						t.Error("literal source/shared reference missing")
					}
				case map[string]any:
					link, ok := coverage["$read"].(map[string]any)
					if !ok || coverage["items"] != float64(60) || link["pointer"] != "/researchSourceContext/retrievedEvidence/coverage" || link["section"] != "stage_input" || !strings.Contains(prompt, "stage-input-pages-v1") {
						t.Error("original source inventory page link missing")
					}
				default:
					t.Error("source inventory missing")
				}
				switch n {
				case 1, 3:
					var wire struct {
						Tools []struct{ Function struct{ Name string } }
					}
					_ = json.Unmarshal(canonical, &wire)
					name := ""
					for _, d := range wire.Tools {
						if d.Function.Name == "research_task_read" {
							name = d.Function.Name
						}
					}
					if name == "" {
						t.Error("existing task reader unavailable")
					}
					emitThresholdFixture(t, w, protocol, n, name, `{"section":"stage_input","offset":0}`)
				case 2:
					http.Error(w, `{"error":{"type":"invalid_request_error","message":"fixed failure after original read"}}`, 400)
				case 4:
					emitThresholdFixture(t, w, protocol, n, "", "")
				case 5, 6:
					if !strings.Contains(string(body), "dedicated stage submission interface") || !strings.Contains(string(body), "submit_stage_result") {
						t.Error("final schema interface missing")
					}
					args := `{}`
					if n == 6 {
						if !strings.Contains(prompt, "Host submission validation failed") {
							t.Error("schema correction diagnostic lost")
						}
						args = `{"summary":"已读取相关原始来源页；要求和历史仍完整保存"}`
					}
					emitThresholdFixture(t, w, protocol, n, "submit_stage_result", args)
				default:
					t.Errorf("extra model/compaction request %d", n)
					http.Error(w, "unexpected request", 400)
				}
			}))
			defer server.Close()
			options := Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}}
			app, err := New(options)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = app.Close() }()
			app.Startup(context.Background())
			profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "source sharing fixture", APIProtocol: protocol, BaseURL: server.URL + "/v1", ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true, ContextWindowTokens: 42000, AutoCompactTokenLimit: 25000}}, TimeoutSeconds: 10, Enabled: true, IsDefault: true})
			if err != nil {
				t.Fatal(err)
			}
			project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "source sharing isolated"})
			if err != nil {
				t.Fatal(err)
			}
			saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: project.ID, Definition: workflow.Definition{SchemaVersion: 1, Name: "Repeated sources are a wire encoding, not missing evidence",
				Inputs: []workflow.Port{{Name: "sources", Type: workflow.TypeObject, Required: true}, {Name: "goal", Type: workflow.TypeString, Required: true}, {Name: "report", Type: workflow.TypeString, Required: true}},
				Nodes: []workflow.Node{
					{ID: "evidence_search", Name: "Saved retrieval fixture", Kind: workflow.NodeAIAnalysis, Arguments: json.RawMessage(`{}`), PromptVersion: "fixture-source-v1", Prompt: "Fixture of an already completed source stage.", ReviewPolicy: workflow.AIReviewAuto, OutputSchema: json.RawMessage(`{"type":"object","properties":{"summary":{"type":"string"}}}`)},
					{ID: "method_selection", Name: "Verify current report", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: "sharing-fixture-v1", Prompt: "Read the original current stage once, then submit the complete required summary.", AllowedTools: []string{workflow.ResearchTaskReadTool, resource.OpenTool}, ReviewPolicy: workflow.AIReviewAuto, OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string","minLength":1}}}`)},
				},
				Edges:   []workflow.Edge{{FromNode: "$input", FromPort: "sources", ToNode: "evidence_search", ToPort: "researchContext"}, {FromNode: "evidence_search", FromPort: "analysis", ToNode: "method_selection", ToPort: "evidenceScreening"}, {FromNode: "$input", FromPort: "goal", ToNode: "method_selection", ToPort: "originalRequest"}, {FromNode: "$input", FromPort: "report", ToNode: "method_selection", ToPort: "context"}},
				Outputs: []workflow.Output{{Name: "result", Type: workflow.TypeObject, FromNode: "method_selection", FromPort: "analysis", Required: true}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			// Seed an isolated PAUSED run with a completed source-tool record.
			// This exercises the same production source-context builder as actual
			// imported tasks without changing compiler ports or formal app state.
			runID, _ := id.New()
			convID, _ := id.New()
			eventID, _ := id.New()
			now := time.Now().UTC()
			hash := fmt.Sprintf("%x", sha256.Sum256(inputs))
			run := workflow.Run{ID: runID, ResearchTaskID: runID, ConversationID: convID, ProjectID: project.ID, WorkflowID: saved.Workflow.ID, WorkflowVersionID: saved.Version.ID, WorkflowName: saved.Workflow.Name, WorkflowPurpose: saved.Workflow.Purpose, Status: workflow.RunPaused, ResumeStatus: workflow.RunQueued, PermissionMode: conversation.PermissionFullAccess, Inputs: inputs, InputsSHA256: hash, Compilation: saved.Version.Compilation, CompilationSHA256: saved.Version.CompilationSHA256, Outputs: json.RawMessage(`{}`), CurrentStep: 1, CreatedAt: now, UpdatedAt: now}
			steps := []workflow.Step{}
			for ordinal, n := range run.Compilation.Nodes {
				stepID, _ := id.New()
				s := workflow.Step{ID: stepID, WorkflowRunID: run.ID, NodeID: n.ID, NodeKind: n.Kind, Ordinal: ordinal, Status: workflow.StepQueued, Input: json.RawMessage(`{}`), Output: json.RawMessage(`{}`), UpdatedAt: now}
				if n.ID == "evidence_search" {
					s.Status, s.Attempt, s.CompletedAt = workflow.StepCompleted, 1, &now
					s.Output = mustBootstrapJSON(t, map[string]any{"analysis": map[string]any{"summary": "Saved retrieval, not proof of scientific adequacy"}, "structured": map[string]any{"coverage": entries}})
				}
				steps = append(steps, s)
			}
			conv := conversation.Conversation{ID: convID, ProjectID: project.ID, Title: "Isolated saved source task", PermissionMode: run.PermissionMode, ReasoningLevel: "medium", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, CreatedAt: now, UpdatedAt: now}
			if err := sqlite.NewWorkflowRuntimeRepository(app.store.DB()).CreateResearchRun(context.Background(), run, steps, workflow.RuntimeEvent{ID: eventID, WorkflowRunID: runID, Type: "workflow.created", Payload: json.RawMessage(`{}`), CreatedAt: now}, conv); err != nil {
				t.Fatal(err)
			}
			started, err := app.WorkflowFacade.Resume(project.ID, runID)
			if err != nil {
				t.Fatal(err)
			}
			failed := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunFailed, 25*time.Second)
			if len(failed.AIExecutions) == 1 {
				var result string
				if err := app.store.DB().QueryRow(`SELECT r.structured_json FROM tool_results r JOIN tool_calls c ON c.id=r.tool_call_id WHERE c.run_id=? AND c.tool_name=?`, failed.AIExecutions[0].ChatRunID, workflow.ResearchTaskReadTool).Scan(&result); err != nil {
					t.Fatal(err)
				}
				var page struct{ Section, Content, SHA256 string }
				if json.Unmarshal([]byte(result), &page) == nil && page.Section == "stage_input" && strings.Contains(page.Content, query) && !strings.Contains(page.Content, "$same") {
					gotOriginalRead.Store(true)
				}
			}
			if requests.Load() != 2 || failed.Run.ErrorCode == "CONTEXT_BUILD_FAILED" || len(failed.AIExecutions) != 1 || !gotOriginalRead.Load() {
				t.Fatal("did not reach provider after original source read", requests.Load(), failed.Run.ErrorCode, failed.Run.ErrorMessage)
			}
			failedStep := currentWorkflowStep(t, failed)
			if err := app.store.DB().QueryRow(`SELECT input_json,input_sha256 FROM workflow_steps WHERE id=?`, failedStep.ID).Scan(&firstRawInput, &firstRawHash); err != nil {
				t.Fatal(err)
			}
			if len([]rune(firstRawInput)) < 42000 || strings.Contains(firstRawInput, "$same") || !strings.Contains(failed.AIExecutions[0].PromptVersion, "source-sharing-v1") {
				t.Fatal("fixture did not distinguish original overflow from projected input")
			}
			var original struct {
				ResearchSourceContext struct {
					RetrievedEvidence struct {
						Coverage []struct {
							DocumentID string `json:"documentId"`
							Queries    []string
						}
					}
				}
			}
			if err := json.Unmarshal([]byte(firstRawInput), &original); err != nil || len(original.ResearchSourceContext.RetrievedEvidence.Coverage) != 60 {
				t.Fatal("complete original source inventory not retained", err)
			}
			for i, row := range original.ResearchSourceContext.RetrievedEvidence.Coverage {
				if row.DocumentID != fmt.Sprintf("document-%d", i) || len(row.Queries) != 1 || row.Queries[0] != query {
					t.Fatalf("original source row %d changed", i)
				}
			}
			oldChat := failed.AIExecutions[0].ChatRunID
			old, err := app.ChatFacade.GetRunSnapshot(oldChat)
			if err != nil || len(old.ToolCalls) != 1 {
				t.Fatal("original read audit missing", err)
			}
			before, _ := json.Marshal(old.ToolCalls)
			if err := app.Close(); err != nil {
				t.Fatal(err)
			}
			app, err = New(options)
			if err != nil {
				t.Fatal(err)
			}
			app.Startup(context.Background())
			if requests.Load() != 2 {
				t.Fatal("restart automatically ran failed stage")
			}
			if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: failed.Run.ID, StepID: failedStep.ID}); err != nil {
				t.Fatal(err)
			}
			completed := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunCompleted, 25*time.Second)
			if requests.Load() != 6 || len(completed.AIExecutions) != 2 || completed.Steps[1].InputSHA256 != firstRawHash {
				t.Fatal("unexpected requests/lost original input identity", requests.Load(), completed.Run.ErrorCode)
			}
			var persistedInput string
			if err := app.store.DB().QueryRow(`SELECT input_json FROM workflow_steps WHERE id=?`, failedStep.ID).Scan(&persistedInput); err != nil || persistedInput != firstRawInput {
				t.Fatal("frozen source bytes changed across retry", err)
			}
			var summaries, reads int
			for kind, dest := range map[string]*int{"compaction": &summaries} {
				if err := app.store.DB().QueryRow(`SELECT COUNT(*) FROM model_request_usage WHERE request_kind=? AND run_id IN (SELECT l.chat_run_id FROM workflow_ai_chat_runs l JOIN workflow_ai_executions e ON e.id=l.execution_id WHERE e.workflow_run_id=?)`, kind, runID).Scan(dest); err != nil {
					t.Fatal(err)
				}
			}
			if err := app.store.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE tool_name=? AND run_id IN (SELECT l.chat_run_id FROM workflow_ai_chat_runs l JOIN workflow_ai_executions e ON e.id=l.execution_id WHERE e.workflow_run_id=?)`, workflow.ResearchTaskReadTool, runID).Scan(&reads); err != nil || reads != 2 || summaries != 0 {
				t.Fatal("extra compaction or rereads", reads, summaries, err)
			}
			old, err = app.ChatFacade.GetRunSnapshot(oldChat)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(old.ToolCalls)
			if string(before) != string(after) {
				t.Fatal("retry mutated old completed read")
			}
			t.Logf("%s: raw input=%d chars >42000; requests=6; original task reader, restart, explicit retry, full submission and schema correction passed", protocol, len([]rune(firstRawInput)))
		})
	}
}

// Close the missed growth path: completed report -> rejected review -> original
// confirmation/restart -> substantially longer replacement -> review -> gate.
// The HTTP model decides the verdict; sharing must not auto-approve anything.
func TestSourceSharingGrowingReportRepairReviewAndGate(t *testing.T) {
	query := "ORIGINAL-QUERY " + strings.Repeat("实际查询范围与检索记录不可篡改😀", 80) + " END-QUERY"
	entries := []any{}
	for i := 0; i < 60; i++ {
		entries = append(entries, map[string]any{"documentId": fmt.Sprintf("document-%d", i), "queries": []string{query}, "fullTextRead": false})
	}
	var reports, reviews atomic.Int32
	var reviewRawSizes []int
	var pageReads atomic.Int32
	var gotRevisedSource atomic.Bool
	readCache := map[string]map[int]string{}
	var oldMethod, oldReport workflow.Step
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		prompt := workflowAITestPrompt(body)
		input := workflowAITestStageInput(prompt)
		schema, err := workflowAITestSchema(prompt)
		if err != nil {
			t.Error(err)
			return
		}
		output := workflowAISchemaFixture(schema).(map[string]any)
		properties := schema["properties"].(map[string]any)
		if _, method := properties["qualityMethod"]; method {
			output["qualityMethod"] = "Judge only supported domains; leave unknown domains unjudged."
		} else if _, report := properties["markdown"]; report {
			n := reports.Add(1)
			output["markdown"] = "# Synthetic report\nMissing promised assessment."
			if n > 1 {
				t.Logf("repair source channels: shared=%v pages=%v corrections=%v revision=%v", strings.Contains(prompt, "source-sharing-v1"), strings.Contains(prompt, "stage-input-pages-v1"), input["_reviewCorrections"] != nil, input["_reviewRevision"] != nil)
				if !strings.Contains(prompt, "source-sharing-v1") || input["_reviewCorrections"] == nil && input["_reviewRevision"] == nil {
					t.Error("report repair lost source encoding or rejection history")
				}
				output["markdown"] = "# Synthetic report\nLimited assessment: supported domain remains supported; remaining domains are unknown.\n" + strings.Repeat("Longer synthetic report content; not a scientific finding.\n", 260) + "END OF COMPLETE REVISED REPORT"
			}
			output["claimSummary"] = []string{"Synthetic fixture, not a scientific conclusion"}
			output["limitations"] = []string{"Original range, query and unknown domains retained"}
		} else {
			if !strings.Contains(prompt, "source-sharing-v1") && !strings.Contains(prompt, "stage-input-pages-v1") {
				t.Error("review exact source view not supplied")
			}
			report, readArguments, err := growingReviewMarkdown(body, input, readCache)
			if err != nil {
				t.Error(err)
				http.Error(w, err.Error(), 400)
				return
			}
			if readArguments != "" {
				pageReads.Add(1)
				emitThresholdFixture(t, w, modelprofile.ProtocolOpenAIChat, int(pageReads.Load()), "research_task_read", readArguments)
				return
			}
			// Shared report input can become a paged review input as it grows.
			// Read the original pointer through the real tool, and use only the
			// query actually returned to this revision's provider request.
			sourceQuery := ""
			if reviews.Load() > 0 {
				sourceQuery, readArguments, err = growingReviewSourceQuery(body, workflowAITestStageInputSHA256(prompt))
				if err != nil {
					t.Error(err)
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				if readArguments != "" {
					pageReads.Add(1)
					emitThresholdFixture(t, w, modelprofile.ProtocolOpenAIChat, int(pageReads.Load()), "research_task_read", readArguments)
					return
				}
				if sourceQuery != query {
					t.Error("original query changed in the provider-visible task read")
				}
				gotRevisedSource.Store(true)
			}
			n := reviews.Add(1)
			if n > 1 && !strings.HasSuffix(report, "END OF COMPLETE REVISED REPORT") {
				t.Error("revised report was silently truncated")
			}
			output["reviewedInputSha256"] = workflowAITestStageInputSHA256(prompt)
			output["approved"] = n > 1
			output["requiredCorrections"] = []string{}
			output["acceptanceChecks"] = []any{map[string]any{"criterionId": "criterion-1", "status": "met", "basis": "Supported limited assessment is present"}}
			if n == 1 {
				output["requiredCorrections"] = []string{"Add the promised limited assessment without redoing correct upstream work"}
				output["methodIssues"] = []string{"Missing promised assessment"}
				output["acceptanceChecks"] = []any{map[string]any{"criterionId": "criterion-1", "status": "not_met", "basis": "Missing promised assessment"}}
			}
			applyTrackedReviewFixture(output, prompt)
			if n == 1 {
				plan := output["revisionPlan"].([]any)[0].(map[string]any)
				plan["nodeId"], plan["reason"], plan["whyNotLater"] = "report_drafting", "Accepted correct method can be reused", "Report owns the missing promised assessment"
			} else {
				findings := output["reviewFindings"].([]any)
				if len(findings) != 1 || findings[0].(map[string]any)["status"] != "resolved" {
					t.Error("prior finding was not explicitly preserved and resolved")
				}
				// This original pointer traverses a shared wire subtree. Host basis
				// validation must still see the original scalar, not the reference.
				findings[0].(map[string]any)["basis"] = []any{map[string]any{"pointer": "/context/markdown", "quote": "Limited assessment"}, map[string]any{"pointer": "/researchSourceContext/retrievedEvidence/coverage/59/queries/0", "quote": sourceQuery}}
			}
			for _, category := range []string{"unsupportedClaims", "citationIssues", "numericIssues", "methodIssues"} {
				delete(output, category)
			}
		}
		answer := "```json\n" + string(mustBootstrapJSON(t, output)) + "\n```"
		emitQuestionHandoffFixture(t, w, modelprofile.ProtocolOpenAIChat, int(reports.Load()+reviews.Load()), answer, "", "")
	}))
	defer server.Close()
	options := Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}}
	app, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	app.Startup(context.Background())
	// This tests exact source/history and >80k-character input growth, not an
	// 80k-token model SLA. Leave room for current host guidance and the required
	// complete replacement; separate 42k-window fixtures still test tight budgets.
	profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "growing review fixture", BaseURL: server.URL + "/v1", ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true, ContextWindowTokens: 120000, AutoCompactTokenLimit: 90000}}, TimeoutSeconds: 10, Enabled: true, IsDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Growing report repair isolated"})
	if err != nil {
		t.Fatal(err)
	}
	reviewSchema, err := os.ReadFile("testdata/review-kernel/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var reportSchema json.RawMessage
	for _, n := range referenceWorkflowTemplate(t, app, "trusted-research-closure").Definition.Nodes {
		if n.ID == "draft_report" {
			reportSchema = n.OutputSchema
		}
	}
	saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: project.ID, Definition: workflow.Definition{SchemaVersion: 1, Name: "Growing report retains exact sources and full history",
		Inputs: []workflow.Port{{Name: "sources", Type: workflow.TypeObject, Required: true}, {Name: "contract", Type: workflow.TypeObject, Required: true}},
		Nodes: []workflow.Node{
			{ID: "evidence_search", Name: "Saved sources", Kind: workflow.NodeAIAnalysis, Arguments: json.RawMessage(`{}`), PromptVersion: "saved-source-fixture-v1", Prompt: "Saved source fixture", ReviewPolicy: workflow.AIReviewAuto, OutputSchema: json.RawMessage(`{"type":"object","properties":{"summary":{"type":"string"}}}`)},
			{ID: "method_selection", Name: "Quality method", Kind: workflow.NodeAIAnalysis, Arguments: json.RawMessage(`{}`), PromptVersion: "scope-fixture-method-v1", Prompt: "Describe a limited quality assessment method", ReviewPolicy: workflow.AIReviewAuto, OutputSchema: json.RawMessage(`{"type":"object","required":["qualityMethod"],"properties":{"qualityMethod":{"type":"string"}},"additionalProperties":false}`)},
			{ID: "report_drafting", Name: "Current report", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: "dynamic-report-v6", Prompt: "Produce a complete limited assessment", ReviewPolicy: workflow.AIReviewAuto, AllowedTools: []string{workflow.ResearchTaskReadTool}, OutputSchema: reportSchema},
			{ID: "independent_review", Name: "Independent review", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: "growing-review-fixture-v1", Prompt: "Independently check current report against unchanged promise", ReviewPolicy: workflow.AIReviewAuto, AllowedTools: []string{workflow.ResearchTaskReadTool}, OutputSchema: reviewSchema},
			{ID: "delivery_gate", Name: "Delivery gate", Kind: workflow.NodeTool, ToolName: "builtin.research.workflow.review.gate", Arguments: json.RawMessage(`{}`)},
		},
		Edges: []workflow.Edge{
			{FromNode: "$input", FromPort: "sources", ToNode: "evidence_search", ToPort: "researchContext"}, {FromNode: "evidence_search", FromPort: "analysis", ToNode: "method_selection", ToPort: "evidenceScreening"},
			{FromNode: "$input", FromPort: "contract", ToNode: "report_drafting", ToPort: "researchContract"}, {FromNode: "$input", FromPort: "contract", ToNode: "independent_review", ToPort: "researchContract"},
			{FromNode: "method_selection", FromPort: "analysis", ToNode: "report_drafting", ToPort: "methodContext"}, {FromNode: "method_selection", FromPort: "analysis", ToNode: "independent_review", ToPort: "methodContext"},
			{FromNode: "report_drafting", FromPort: "analysis", ToNode: "independent_review", ToPort: "context"}, {FromNode: "report_drafting", FromPort: "analysis", ToNode: "delivery_gate", ToPort: "subject"}, {FromNode: "independent_review", FromPort: "analysis", ToNode: "delivery_gate", ToPort: "review"},
		},
		Outputs: []workflow.Output{{Name: "report_draft", Type: workflow.TypeObject, FromNode: "report_drafting", FromPort: "analysis", Required: true}, {Name: "delivery_gate", Type: workflow.TypeObject, FromNode: "delivery_gate", FromPort: "structured", Required: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	runID, _ := id.New()
	convID, _ := id.New()
	eventID, _ := id.New()
	inputs := mustBootstrapJSON(t, map[string]any{"sources": map[string]any{"coverage": entries}, "contract": map[string]any{"successCriteria": []string{"Present a supported limited assessment"}}})
	run := workflow.Run{ID: runID, ResearchTaskID: runID, ConversationID: convID, ProjectID: project.ID, WorkflowID: saved.Workflow.ID, WorkflowVersionID: saved.Version.ID, WorkflowName: saved.Workflow.Name, WorkflowPurpose: saved.Workflow.Purpose, Status: workflow.RunPaused, ResumeStatus: workflow.RunQueued, PermissionMode: conversation.PermissionFullAccess, Inputs: inputs, InputsSHA256: fmt.Sprintf("%x", sha256.Sum256(inputs)), Compilation: saved.Version.Compilation, CompilationSHA256: saved.Version.CompilationSHA256, Outputs: json.RawMessage(`{}`), CurrentStep: 1, CreatedAt: now, UpdatedAt: now}
	steps := []workflow.Step{}
	for ordinal, n := range run.Compilation.Nodes {
		stepID, _ := id.New()
		s := workflow.Step{ID: stepID, WorkflowRunID: runID, NodeID: n.ID, NodeKind: n.Kind, Ordinal: ordinal, Status: workflow.StepQueued, Input: json.RawMessage(`{}`), Output: json.RawMessage(`{}`), UpdatedAt: now}
		if n.ID == "evidence_search" {
			s.Status, s.Attempt, s.CompletedAt = workflow.StepCompleted, 1, &now
			s.Output = mustBootstrapJSON(t, map[string]any{"analysis": map[string]any{"summary": "Saved source fixture"}, "structured": map[string]any{"coverage": entries}})
		}
		steps = append(steps, s)
	}
	conv := conversation.Conversation{ID: convID, ProjectID: project.ID, Title: "Growing report", PermissionMode: run.PermissionMode, ReasoningLevel: "medium", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewWorkflowRuntimeRepository(app.store.DB()).CreateResearchRun(context.Background(), run, steps, workflow.RuntimeEvent{ID: eventID, WorkflowRunID: runID, Type: "workflow.created", Payload: json.RawMessage(`{}`), CreatedAt: now}, conv); err != nil {
		t.Fatal(err)
	}
	if _, err := app.WorkflowFacade.Resume(project.ID, runID); err != nil {
		t.Fatal(err)
	}
	failed := waitForWorkflowState(t, app, project.ID, runID, workflow.RunFailed, 30*time.Second)
	recommendation := failed.ReviewRevisionRecommendation
	if failed.Run.ErrorCode != "RESEARCH_REVIEW_NOT_APPROVED" || recommendation == nil || recommendation.NodeID != "report_drafting" || len(recommendation.AffectedStages) != 3 {
		t.Fatal("first review did not reject and target report only", failed.Run.ErrorCode, failed.Run.ErrorMessage, recommendation)
	}
	for _, s := range failed.Steps {
		switch s.NodeID {
		case "method_selection":
			oldMethod = s
		case "report_drafting":
			oldReport = s
		case "independent_review":
			reviewRawSizes = append(reviewRawSizes, len([]rune(string(s.Input))))
		}
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	if reports.Load() != 1 || reviews.Load() != 1 {
		t.Fatal("restart executed a repair without confirmation")
	}
	if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: runID, StepID: currentWorkflowStep(t, failed).ID, UseRecommendation: true, ExpectedReviewSHA256: recommendation.ReviewOutputSHA256}); err != nil {
		t.Fatal(err)
	}
	completed := waitForWorkflowState(t, app, project.ID, runID, workflow.RunCompleted, 30*time.Second)
	for _, s := range completed.Steps {
		if s.NodeID == "method_selection" && (s.Attempt != oldMethod.Attempt || string(s.Output) != string(oldMethod.Output)) {
			t.Fatal("unnecessary upstream rerun")
		}
		if s.NodeID == "report_drafting" && len(s.Output) <= len(oldReport.Output)+10000 {
			t.Fatal("replacement report did not substantially grow")
		}
		if s.NodeID == "independent_review" {
			reviewRawSizes = append(reviewRawSizes, len([]rune(string(s.Input))))
			if !strings.Contains(string(s.Input), "reviewHistory") || !strings.Contains(string(s.Input), "Missing promised assessment") || strings.Contains(string(s.Input), "$same") {
				t.Fatal("frozen repaired review lost original input/history")
			}
		}
	}
	if reports.Load() != 2 || reviews.Load() != 2 || len(reviewRawSizes) != 2 || reviewRawSizes[1] <= reviewRawSizes[0]+10000 || reviewRawSizes[1] <= 80000 {
		t.Fatal("growth path not exercised or extra model requests", reports.Load(), reviews.Load(), reviewRawSizes)
	}
	if err := workflow.ValidateReviewedOutput(completed, "report_draft"); err != nil {
		t.Fatal("review/gate did not validate full replacement", err)
	}
	if !gotRevisedSource.Load() {
		t.Fatal("revised review never read the original source query")
	}
	t.Logf("source view: first review input=%d revised=%d chars >80000; actual page reads=%d; complete report retained, report-only repair, prior finding and original basis verified", reviewRawSizes[0], reviewRawSizes[1], pageReads.Load())
}

func growingReviewSourceQuery(body []byte, inputSHA256 string) (string, string, error) {
	const pointer = "/researchSourceContext/retrievedEvidence/coverage/59/queries/0"
	var request struct {
		Messages []struct {
			Role    string
			Content json.RawMessage
		}
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return "", "", err
	}
	for _, message := range request.Messages {
		if message.Role != "tool" {
			continue
		}
		var text string
		if json.Unmarshal(message.Content, &text) != nil {
			continue
		}
		text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "<untrusted_tool_result>"), "</untrusted_tool_result>"))
		var result struct {
			Structured struct {
				Section, SHA256, Pointer, Encoding, Content, ValueSHA256 string
				Offset, NextOffset, TotalCharacters                      int
				HasMore                                                  bool
			}
		}
		if json.Unmarshal([]byte(text), &result) != nil {
			continue
		}
		page := result.Structured
		if page.Section != "stage_input" || page.SHA256 != inputSHA256 || page.Pointer != pointer {
			continue
		}
		if page.Encoding != "text" || page.Offset != 0 || page.HasMore || page.NextOffset != len([]rune(page.Content)) || page.TotalCharacters != page.NextOffset || page.ValueSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(page.Content))) {
			return "", "", fmt.Errorf("original source query page is incomplete or its hash does not match")
		}
		return page.Content, "", nil
	}
	args, _ := json.Marshal(map[string]any{"section": "stage_input", "pointer": pointer, "offset": 0})
	return "", string(args), nil
}

// The controlled model reads every byte of a paged report before reviewing it.
// It accumulates only pages actually visible in requests; source fixtures and
// model judgments are synthetic, never a claim of scientific comprehension.
func growingReviewMarkdown(body []byte, input map[string]any, cache map[string]map[int]string) (string, string, error) {
	value := input["context"].(map[string]any)["markdown"]
	if text, ok := value.(string); ok {
		return text, "", nil
	}
	descriptor, ok := value.(map[string]any)
	if !ok {
		return "", "", fmt.Errorf("missing report value")
	}
	raw, _ := descriptor["$read"].(map[string]any)
	if raw == nil {
		return "", "", fmt.Errorf("missing report page link")
	}
	hash, _ := raw["sha256"].(string)
	start := int(raw["offset"].(float64))
	end := int(raw["endOffset"].(float64))
	if cache[hash] == nil {
		cache[hash] = map[int]string{}
	}
	var request struct {
		Messages []struct {
			Role    string
			Content json.RawMessage
		}
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return "", "", err
	}
	for _, message := range request.Messages {
		if message.Role != "tool" {
			continue
		}
		var text string
		if json.Unmarshal(message.Content, &text) != nil {
			continue
		}
		text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "<untrusted_tool_result>"), "</untrusted_tool_result>"))
		var result struct {
			Structured struct {
				Section, SHA256, Content string
				Offset, NextOffset       int
			}
		}
		if json.Unmarshal([]byte(text), &result) != nil {
			continue
		}
		page := result.Structured
		if page.Section == "stage_input" && page.SHA256 == hash && page.NextOffset == page.Offset+len([]rune(page.Content)) {
			if err := cacheVisibleReviewPage(cache[hash], page.Offset, page.Content); err != nil {
				return "", "", err
			}
		}
	}
	var assembled strings.Builder
	position := start
	for position < end {
		content, ok := cache[hash][position]
		if !ok {
			break
		}
		runes := []rune(content)
		if len(runes) == 0 {
			return "", "", fmt.Errorf("empty input page")
		}
		n := min(len(runes), end-position)
		assembled.WriteString(string(runes[:n]))
		position += n
	}
	if position < end {
		args, _ := json.Marshal(map[string]any{"section": "stage_input", "offset": position})
		return "", string(args), nil
	}
	var text string
	if err := json.Unmarshal([]byte(assembled.String()), &text); err != nil {
		return "", "", err
	}
	return text, "", nil
}

func cacheVisibleReviewPage(cache map[int]string, offset int, content string) error {
	old := cache[offset]
	if strings.HasPrefix(content, old) {
		cache[offset] = content
	} else if !strings.HasPrefix(old, content) {
		return fmt.Errorf("conflicting visible page at Unicode offset %d", offset)
	}
	return nil
}

func TestGrowingReviewCacheRetainsOnlyObservedConsistentUnicodePrefixes(t *testing.T) {
	cache := map[int]string{}
	for _, text := range []string{"中🙂文", "中", "中🙂文字"} {
		if err := cacheVisibleReviewPage(cache, 7, text); err != nil {
			t.Fatal(err)
		}
	}
	if cache[7] != "中🙂文字" {
		t.Fatal("shorter preview erased earlier observed suffix")
	}
	if err := cacheVisibleReviewPage(cache, 7, "中🙂伪"); err == nil {
		t.Fatal("conflicting page was silently combined")
	}
}

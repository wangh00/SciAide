package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestLiteratureIdentityCorrectionAndPolicyRecoveryAcrossProtocols(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		for _, mode := range []string{"patch", "policy_rejected", "exhausted"} {
			t.Run(string(protocol)+"/"+mode, func(t *testing.T) {
				var requests, firstBatchReads, corrections atomic.Int32
				var resumed atomic.Bool
				firstBatch, brokenBatch, actualID := "", "", ""
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					b, _ := io.ReadAll(r.Body)
					canonical := contextPipelineCanonicalRequest(t, b)
					prompt := workflowAITestPrompt(canonical)
					n := int(requests.Add(1))
					var req struct {
						Tools []struct {
							Function struct {
								Name       string                     `json:"name"`
								Parameters map[string]json.RawMessage `json:"parameters"`
							} `json:"function"`
						} `json:"tools"`
					}
					_ = json.Unmarshal(canonical, &req)
					for _, tool := range req.Tools {
						if tool.Function.Name == "submit_stage_repair" {
							corrections.Add(1)
							if mode == "policy_rejected" {
								w.Header().Set("Content-Type", "text/event-stream")
								err := `{"type":"invalid_request_error","code":"invalid_prompt","message":"Invalid prompt: your prompt was flagged as potentially violating our usage policy."}`
								if protocol == modelprofile.ProtocolOpenAIResponses {
									fmt.Fprintf(w, "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":%s}}\n\n", err)
								} else {
									fmt.Fprintf(w, "data: {\"type\":\"error\",\"error\":%s}\n\n", err)
								}
								return
							}
							var props map[string]json.RawMessage
							_ = json.Unmarshal(tool.Function.Parameters["properties"], &props)
							var hash struct {
								Enum []string `json:"enum"`
							}
							_ = json.Unmarshal(props["baseSHA256"], &hash)
							if len(hash.Enum) != 1 {
								t.Error("missing patch base")
								return
							}
							h := hash.Enum[0]
							if mode == "exhausted" {
								h = strings.Repeat("a", 64)
							}
							patch := map[string]any{"baseSHA256": h, "candidateIdentityPatch": []map[string]string{{"path": "/candidateAssessments/0/candidateId", "candidateId": actualID}, {"path": "/evidenceNotes/0/candidateId", "candidateId": actualID}}}
							emitQuestionHandoffFixture(t, w, protocol, n, "", "submit_stage_repair", string(mustBootstrapJSON(t, patch)))
							return
						}
					}
					input := workflowAITestStageInput(prompt)
					info, _ := input["_literature"].(map[string]any)
					phase, _ := info["phase"].(string)
					out := map[string]any{"phase": phase, "summary": "Preserved source relevance, no effect conclusion"}
					ids := []string{}
					if phase == "batch" {
						cs := input["candidates"].([]any)
						key := cs[0].(map[string]any)["id"].(string)
						if firstBatch == "" {
							firstBatch = key
						}
						if key == firstBatch {
							firstBatchReads.Add(1)
						} else if brokenBatch == "" {
							brokenBatch = key
						}
						assessments, notes := []any{}, []any{}
						for i, v := range cs {
							c := v.(map[string]any)
							id := c["id"].(string)
							if i == 0 && key == brokenBatch && !resumed.Load() {
								actualID = id
								id = "misspelled-candidate"
							}
							assessments = append(assessments, map[string]any{"candidateId": id, "decision": "support", "relevance": "medium", "reason": "Relevant source, verify details", "importAction": "verify", "purpose": "Verify the original study"})
							notes = append(notes, map[string]any{"candidateId": id, "quotes": []any{map[string]any{"field": "abstract", "segmentId": "a0001"}}})
						}
						out["candidateAssessments"], out["evidenceNotes"] = assessments, notes
					} else {
						if records, ok := input["evidenceRecords"].([]any); ok {
							for _, v := range records {
								ids = append(ids, v.(map[string]any)["candidateId"].(string))
							}
						}
						if children, ok := input["childSummaries"].([]any); ok {
							for _, v := range children {
								for _, id := range v.(map[string]any)["candidateIds"].([]any) {
									ids = append(ids, id.(string))
								}
							}
						}
						findings := []any{}
						for i := 0; i < len(ids); i += 20 {
							end := i + 20
							if end > len(ids) {
								end = len(ids)
							}
							findings = append(findings, map[string]any{"text": "Relevant materials for user selection; not validated effectiveness", "candidateIds": ids[i:end]})
						}
						out["findings"], out["uncertainties"] = findings, []any{}
						if phase == "coverage" {
							out["coverage"] = map[string]any{"strength": "limited", "sufficientForClaimedScope": false, "abstractAvailable": len(ids), "metadataOnly": 0, "gaps": []string{"Full text not yet verified"}}
							out["supplementalQueries"] = []string{}
							out["recommendation"] = "use_recommendation"
							out["recommendedCandidateIds"] = ids
							out["recheckCandidateIds"] = []string{}
							out["rechecks"] = []any{}
						}
					}
					emitQuestionHandoffFixture(t, w, protocol, n, "```json\n"+string(mustBootstrapJSON(t, out))+"\n```", "", "")
				}))
				defer server.Close()
				options := Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}, ResearchConnectors: []research.Connector{literatureConnectorFixture{name: "pubmed"}, literatureConnectorFixture{name: "openalex"}, literatureConnectorFixture{name: "crossref"}}}
				app, e := New(options)
				if e != nil {
					t.Fatal(e)
				}
				defer func() { _ = app.Close() }()
				app.Startup(context.Background())
				profile, e := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "identity fixture", BaseURL: server.URL + "/v1", APIProtocol: protocol, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true, ContextWindowTokens: 200000}}, TimeoutSeconds: 10, Enabled: true, IsDefault: true})
				if e != nil {
					t.Fatal(e)
				}
				project, e := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "literature identity"})
				if e != nil {
					t.Fatal(e)
				}
				schema := json.RawMessage(`{"type":"object","required":["phase","summary"],"properties":{"phase":{"type":"string"},"summary":{"type":"string"},"candidateAssessments":{"type":"array","items":{"type":"object"}},"evidenceNotes":{"type":"array","items":{"type":"object","properties":{}}},"coverage":{"type":"object"},"supplementalQueries":{"type":"array","items":{"type":"string"}},"recommendation":{"type":"string"},"recommendedCandidateIds":{"type":"array","items":{"type":"string"}},"recheckCandidateIds":{"type":"array","items":{"type":"string"}},"rechecks":{"type":"array","items":{"type":"object"}},"findings":{"type":"array","items":{"type":"object"}},"uncertainties":{"type":"array","items":{"type":"object"}}}}`)
				saved, e := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: project.ID, Definition: workflow.Definition{SchemaVersion: 1, Name: "screening", Inputs: []workflow.Port{{Name: "query", Type: workflow.TypeString, Required: true}}, Nodes: []workflow.Node{
					{ID: "literature_discovery", Name: "Search", Kind: workflow.NodeTool, ToolName: "builtin.research.workflow.search", Arguments: json.RawMessage(`{"limit":20,"referencesOnly":true}`)},
					{ID: "candidate_screening", Name: "Screen", Kind: workflow.NodeAIAnalysis, PromptVersion: "dynamic-candidate-screening-v11", Prompt: "Screen frozen sources", Arguments: json.RawMessage(`{}`), ReviewPolicy: workflow.AIReviewAuto, OutputSchema: schema},
					{ID: "candidate_review", Name: "Confirm", Kind: workflow.NodeCandidateSelection, Arguments: json.RawMessage(`{}`), Prompt: "Select materials"},
				}, Edges: []workflow.Edge{{FromNode: "$input", FromPort: "query", ToNode: "literature_discovery", ToPort: "query"}, {FromNode: "$input", FromPort: "query", ToNode: "candidate_review", ToPort: "query"}, {FromNode: "literature_discovery", FromPort: "structured.candidates", ToNode: "candidate_screening", ToPort: "candidates"}, {FromNode: "literature_discovery", FromPort: "structured.candidates", ToNode: "candidate_review", ToPort: "candidates"}, {FromNode: "candidate_screening", FromPort: "analysis", ToNode: "candidate_review", ToPort: "screening"}}}})
				if e != nil {
					t.Fatal(e)
				}
				started, e := app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: project.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"query":"initial scope"}`), ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: "full_access"})
				if e != nil {
					t.Fatal(e)
				}
				if mode != "patch" {
					failed := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunFailed, 20*time.Second)
					want := "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED"
					if mode == "policy_rejected" {
						want = "MODEL_PROMPT_REJECTED"
					}
					if failed.Run.ErrorCode != want {
						t.Fatal(failed.Run.ErrorCode, failed.Run.ErrorMessage)
					}
					if firstBatchReads.Load() != 1 {
						t.Fatal("prior batch repeated")
					}
					before := requests.Load()
					if e = app.Close(); e != nil {
						t.Fatal(e)
					}
					app, e = New(options)
					if e != nil {
						t.Fatal(e)
					}
					app.Startup(context.Background())
					if requests.Load() != before {
						t.Fatal("restart invoked model")
					}
					resumed.Store(true)
					if _, e = app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: started.Run.ID, StepID: failed.Steps[1].ID}); e != nil {
						t.Fatal(e)
					}
				}
				waiting := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation, 30*time.Second)
				if firstBatchReads.Load() != 1 || waiting.Steps[0].Attempt != 1 {
					t.Fatal("recovery repeated search or accepted batch")
				}
				want := int32(1)
				if mode == "exhausted" {
					want = 2
				}
				if corrections.Load() != want {
					t.Fatal("unexpected correction/retry count", corrections.Load())
				}
				var diagnostics int
				if e = app.store.DB().QueryRow(`SELECT count(*) FROM run_events e JOIN workflow_ai_chat_runs b ON b.chat_run_id=e.aggregate_id JOIN workflow_ai_executions x ON x.id=b.execution_id WHERE x.workflow_run_id=? AND e.event_type='run.submission_rejected'`, started.Run.ID).Scan(&diagnostics); e != nil || diagnostics == 0 {
					t.Fatal("correction diagnostics not persisted", e, diagnostics)
				}
				t.Logf("HTTP+SQLite %s: requests=%d corrections=%d prior accepted batch reads=1; search preserved, original confirmation reached", mode, requests.Load(), corrections.Load())
			})
		}
	}
}

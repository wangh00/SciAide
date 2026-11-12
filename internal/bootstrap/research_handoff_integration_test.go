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
	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Scripted HTTP, real adapters/SQLite/restart/Retry/gate. The host supplies
// saved facts; verdicts and repair choices here are deliberately model fixtures,
// NOT a measurement of any commercial model's scientific reliability.
func TestResearchHandoffReportRepairRestartReviewAndGate(t *testing.T) {
	testResearchHandoffRepair(t, modelprofile.ProtocolOpenAIChat, false)
}

func TestReviewConvergenceAcrossProtocols(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) { testResearchHandoffRepair(t, protocol, true) })
	}
}

func TestRecordedGroupingHandoffAndReviewContinuityAcrossProtocols(t *testing.T) {
	for _, p := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		t.Run(string(p), func(t *testing.T) { testResearchHandoffRepair(t, p, true, false, true) })
	}
}

func TestLargeGateTechnicalRecoveryThenOriginalRevisionAcrossProtocols(t *testing.T) {
	for _, p := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		t.Run(string(p), func(t *testing.T) { testResearchHandoffRepair(t, p, true, true) })
	}
}

func testResearchHandoffRepair(t *testing.T, protocol modelprofile.APIProtocol, scoped bool, gateRecovery ...bool) {
	largeGate := len(gateRecovery) > 0 && gateRecovery[0]
	recordedGrouping := len(gateRecovery) > 1 && gateRecovery[1]
	wideNavigation := len(gateRecovery) > 2 && gateRecovery[2]
	waitTimeout := 25 * time.Second
	if wideNavigation {
		waitTimeout = 2 * time.Minute
	} // CPU-bound catalog construction, not provider retry/timeout policy.
	for _, upstreamError := range []bool{false, true} {
		t.Run(fmt.Sprintf("upstream_error_%t", upstreamError), func(t *testing.T) {
			var reports, reviews, methods, observed atomic.Int32
			var invalidSent atomic.Bool
			var frozenGrouping string
			target := "report_drafting"
			if upstreamError {
				target = "method_selection"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				prompt := workflowAITestPrompt(contextPipelineCanonicalRequest(t, body))
				input := workflowAITestStageInput(prompt)
				schema, err := workflowAITestSchema(prompt)
				if err != nil {
					t.Error(err)
					return
				}
				output := workflowAISchemaFixture(schema).(map[string]any)
				properties := schema["properties"].(map[string]any)
				if _, method := properties["qualityMethod"]; method {
					methods.Add(1)
					output["qualityMethod"] = "Assess only reported domains; no double-counting or invented low risk."
				} else if _, report := properties["markdown"]; report {
					if recordedGrouping {
						audit := string(mustBootstrapJSON(t, input["researchRecordAudit"]))
						if !strings.Contains(audit, research.RecordGroupingVersion) || !strings.Contains(audit, "match_alias") || !strings.Contains(audit, "reuse_source_record") {
							t.Error("real repository grouping decisions missing from report", audit)
						}
						if frozenGrouping != "" && frozenGrouping != audit {
							t.Error("report refreshed immutable query decisions")
						}
						frozenGrouping = audit
					}
					handoff, ok := input["researchHandoff"].(map[string]any)
					if !ok || !strings.Contains(prompt, "original exclusion rationale") || !strings.Contains(prompt, "research-handoff-v1") || len(handoff["excludedRecords"].([]any)) != 1 {
						t.Error("saved reasons still depend on an extra task read")
					}
					// Exercise the same-run correction of a copied/tampered host
					// appendix before the first genuine report is committed.
					if invalidSent.CompareAndSwap(false, true) {
						output["markdown"] = "# Report\n## 附录：候选筛选记录\nchanged saved reason"
					} else {
						n := reports.Add(1)
						if n == 1 && !strings.Contains(prompt, "Host submission validation failed") {
							t.Error("correction did not remain in the same model context")
						}
						output["markdown"] = "# Report\nA source-bounded result. Missing promised assessment."
						if n > 1 {
							output["markdown"] = "# Corrected report\nLimited assessment based on the current method; unreported domains remain unknown.\n" + strings.Repeat("Longer report, bounded to supplied evidence.\n", 240) + "END COMPLETE BODY"
						}
					}
					output["claimSummary"], output["limitations"] = []string{"Bounded fixture"}, []string{"Overlap unknown; no independent-trial claim."}
				} else {
					n := reviews.Add(1)
					if wideNavigation && (!strings.Contains(prompt, `"navigationEncoding":"prefix-groups-v1"`) || !strings.Contains(prompt, `"version":"review-source-v2"`)) {
						t.Error("wide review did not receive its actual grouped source contract")
					}
					handoff, _ := input["researchHandoff"].(map[string]any)
					report := input["context"].(map[string]any)["markdown"].(string)
					records := string(mustBootstrapJSON(t, input["reportRecordAppendices"]))
					if recordedGrouping {
						if string(mustBootstrapJSON(t, input["researchRecordAudit"])) != frozenGrouping || !strings.Contains(records, "本次检索实际题录归组方法") || !strings.Contains(records, "doi:10.1000/grouping") || !strings.Contains(records, "复用相同来源记录") {
							t.Error("review lost actual method appendix")
						}
					}
					if handoff == nil || strings.Contains(report, "## 附录：候选筛选记录") || !strings.Contains(records, "original exclusion rationale") || strings.Count(records, "## 附录：候选筛选记录") != 1 || input["_reviewCorrections"] != nil {
						t.Error("review did not receive exact host source appendix")
					}
					if !strings.Contains(prompt, "Independent review submission (latest sufficient offered stage)") || !strings.Contains(prompt, "concrete error/missing reusable result") {
						t.Error("new responsibility contract absent")
					}
					roots, _ := handoff["repairResultRoots"].(map[string]any)
					if roots == nil || !strings.Contains(string(mustBootstrapJSON(t, roots["method_selection"])), "/methodContext") || !strings.Contains(string(mustBootstrapJSON(t, roots["report_drafting"])), "/context") {
						t.Error("repair ownership did not follow compiled edges", roots)
					}
					observed.Add(1)
					if n > 1 && !strings.Contains(report, "END COMPLETE BODY") {
						t.Error("growing report truncated")
					}
					if n > 1 {
						history, _ := input["reviewHistory"].(map[string]any)
						checks, _ := history["acceptanceChecks"].([]any)
						if len(checks) == 0 || checks[0].(map[string]any)["status"] != "not_met" || len(history["requiredCorrections"].([]any)) != 1 || len(history["revisionPlan"].([]any)) != 1 || !strings.Contains(prompt, "prior remedy was insufficient") {
							t.Error("previous acceptance/remedy not handed off")
						}
					}
					output["approved"], output["reviewedInputSha256"] = n > 1, workflowAITestStageInputSHA256(prompt)
					output["requiredCorrections"] = []string{}
					output["acceptanceChecks"] = []any{map[string]any{"criterionId": "criterion-1", "status": "met", "basis": "Bounded assessment exists; original limitations retained"}}
					if n == 1 {
						issue := "Missing promised report assessment"
						if upstreamError {
							issue = "Reusable method falsely declares unknown domains low risk"
						}
						output["requiredCorrections"], output["methodIssues"] = []string{issue}, []string{issue}
						output["acceptanceChecks"] = []any{map[string]any{"criterionId": "criterion-1", "status": "not_met", "basis": issue}}
					}
					if scoped {
						if input["evidenceAcceptancePolicy"] != "claim-scoped-evidence-v1" {
							t.Error("scoped policy missing")
						}
						checks := []any{}
						for index, c := range input["acceptanceCriteria"].([]any) {
							status := "met"
							if n == 1 && index == 0 {
								status = "not_met"
							}
							checks = append(checks, map[string]any{"criterionId": c.(map[string]any)["id"], "status": status, "basis": "Assess current promised work; uncertain effect is not a positive empirical claim."})
						}
						output["acceptanceChecks"] = checks
					}
					applyTrackedReviewFixture(output, prompt)
					if n == 1 {
						plan := output["revisionPlan"].([]any)[0].(map[string]any)
						plan["nodeId"], plan["reason"], plan["whyNotLater"] = target, "Report omits agreed assessment; current method is correct.", "The report is the last producer of this missing presentation."
						if upstreamError {
							plan["reason"], plan["whyNotLater"] = "Current reusable method explicitly misclassifies unknown domains.", "Report-only editing leaves /methodContext/qualityMethod incorrect."
							output["reviewFindings"].([]any)[0].(map[string]any)["basis"] = []any{map[string]any{"pointer": "/methodContext/qualityMethod", "quote": "declare all unknown domains low risk"}}
						}
					}
					for _, key := range []string{"unsupportedClaims", "citationIssues", "numericIssues", "methodIssues"} {
						delete(output, key)
					}
				}
				delete(output, "reviewedInputSha256") // host binds identity across all three provider protocols
				answer := "```json\n" + string(mustBootstrapJSON(t, output)) + "\n```"
				// Normal fixtures must obey the current required native review
				// interface; the scientific rejection/repair assertions stay intact.
				emitted := emitRequiredPlannerResultFixture(t, w, protocol, body, string(mustBootstrapJSON(t, output)))
				if scoped && properties["reviewedInputSha256"] != nil && (!emitted || !strings.Contains(prompt, "<review_submission_sources>") || !strings.Contains(string(plannerSubmissionSchemaOnWire(protocol, body)), `"sourceSegmentIds"`)) {
					t.Error("current native source-selection review was not offered on wire")
				}
				if !emitted {
					emitQuestionHandoffFixture(t, w, protocol, 1, answer, "", "")
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
			window, automatic := 272000, 244800
			if wideNavigation {
				window, automatic = 400000, 360000
			} // Same 380000 effective input budget as the captured incident.
			profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "handoff scoped fixture", BaseURL: server.URL + "/v1", APIProtocol: protocol, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true, ContextWindowTokens: window, AutoCompactTokenLimit: automatic}}, TimeoutSeconds: 10, Enabled: true, IsDefault: true})
			if err != nil {
				t.Fatal(err)
			}
			project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Frozen research handoff isolated"})
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
			reviewVersion := "handoff-fixture-v1"

			if scoped {
				reviewVersion = "dynamic-independent-review-v9"

			}
			definition := workflow.Definition{SchemaVersion: 1, Name: "Handoff repair ownership fixture",
				Inputs: []workflow.Port{{Name: "contract", Type: workflow.TypeObject, Required: true}, {Name: "screening", Type: workflow.TypeObject, Required: true}},
				Nodes: []workflow.Node{
					{ID: "candidate_screening", Name: "Saved candidate audit", Kind: workflow.NodeAIAnalysis, Arguments: json.RawMessage(`{}`), PromptVersion: "fixture-v1", Prompt: "Saved audit fixture", ReviewPolicy: workflow.AIReviewAuto, OutputSchema: json.RawMessage(`{"type":"object"}`)},
					{ID: "method_selection", Name: "Reusable method", Kind: workflow.NodeAIAnalysis, Arguments: json.RawMessage(`{}`), PromptVersion: "fixture-v1", Prompt: "Correct the recorded method defect only", ReviewPolicy: workflow.AIReviewAuto, OutputSchema: json.RawMessage(`{"type":"object","required":["qualityMethod"],"properties":{"qualityMethod":{"type":"string"}},"additionalProperties":false}`)},
					{ID: "report_drafting", Name: "Report", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: "dynamic-report-v6", Prompt: "Present a bounded report and actual agreed assessment", ReviewPolicy: workflow.AIReviewAuto, AllowedTools: []string{workflow.ResearchTaskReadTool}, OutputSchema: reportSchema},
					{ID: "independent_review", Name: "Independent review", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: reviewVersion, Prompt: "Check actual report, sources and method", ReviewPolicy: workflow.AIReviewAuto, AllowedTools: []string{workflow.ResearchTaskReadTool}, OutputSchema: reviewSchema},
					{ID: "delivery_gate", Name: "Delivery gate", Kind: workflow.NodeTool, ToolName: "builtin.research.workflow.review.gate", Arguments: json.RawMessage(`{}`)},
				},
				Edges: []workflow.Edge{
					{FromNode: "$input", FromPort: "screening", ToNode: "independent_review", ToPort: "evidenceScreening"},
					{FromNode: "candidate_screening", FromPort: "analysis", ToNode: "method_selection", ToPort: "evidenceScreening"},
					{FromNode: "$input", FromPort: "contract", ToNode: "report_drafting", ToPort: "researchContract"}, {FromNode: "$input", FromPort: "contract", ToNode: "independent_review", ToPort: "researchContract"},
					{FromNode: "method_selection", FromPort: "analysis", ToNode: "report_drafting", ToPort: "methodContext"}, {FromNode: "method_selection", FromPort: "analysis", ToNode: "independent_review", ToPort: "methodContext"},
					{FromNode: "report_drafting", FromPort: "analysis", ToNode: "independent_review", ToPort: "context"}, {FromNode: "report_drafting", FromPort: "analysis", ToNode: "delivery_gate", ToPort: "subject"}, {FromNode: "independent_review", FromPort: "analysis", ToNode: "delivery_gate", ToPort: "review"},
				},
				Outputs: []workflow.Output{{Name: "report_draft", Type: workflow.TypeObject, FromNode: "report_drafting", FromPort: "analysis", Required: true}, {Name: "delivery_gate", Type: workflow.TypeObject, FromNode: "delivery_gate", FromPort: "structured", Required: true}},
			}
			if wideNavigation {
				definition.Inputs = append(definition.Inputs, workflow.Port{Name: "wide_sources", Type: workflow.TypeObject, Required: true})
				definition.Edges = append(definition.Edges, workflow.Edge{FromNode: "$input", FromPort: "wide_sources", ToNode: "independent_review", ToPort: "dataPreflight"})
			}
			var queryID string
			if recordedGrouping {
				repo := sqlite.NewResearchRepository(app.store.DB())
				work := research.Work{SourceID: "crossref", SourceRecordID: "grouping-a", Title: "Recorded grouping fixture title", Year: 2026, Identifiers: research.Identifiers{DOI: "10.1000/grouping"}}
				q, e := repo.SaveSearch(context.Background(), project.ID, strings.Repeat("a", 64), research.SearchCommand{Query: "grouping fixture", Limit: 10}, research.SearchResult{Query: "grouping fixture", Works: []research.Work{work, {SourceID: "openalex", SourceRecordID: "grouping-b", Title: work.Title, Year: 2026, Identifiers: research.Identifiers{DOI: "10.1000/grouping", OpenAlex: "W1"}}}}, time.Now().UTC())
				if e != nil {
					t.Fatal(e)
				}
				q2, e := repo.SaveSearch(context.Background(), project.ID, strings.Repeat("b", 64), research.SearchCommand{Query: "grouping reuse", Limit: 10}, research.SearchResult{Query: "grouping reuse", Works: []research.Work{work}}, time.Now().UTC())
				if e != nil {
					t.Fatal(e)
				}
				queryID = q.ID + "," + q2.ID
				definition.Nodes = append([]workflow.Node{{ID: "literature_discovery", Name: "Recorded discovery", Kind: workflow.NodeAIAnalysis, Arguments: json.RawMessage(`{}`), PromptVersion: "fixture-v1", Prompt: "Saved discovery", ReviewPolicy: workflow.AIReviewAuto, OutputSchema: json.RawMessage(`{"type":"object"}`)}}, definition.Nodes...)
				definition.Edges = append(definition.Edges, workflow.Edge{FromNode: "literature_discovery", FromPort: "analysis", ToNode: "candidate_screening", ToPort: "context"})
			}
			saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: project.ID, Definition: definition})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			runID, _ := id.New()
			convID, _ := id.New()
			eventID, _ := id.New()
			inputs := json.RawMessage(`{"contract":{"successCriteria":["Present a supported limited assessment; preserve existing source audit"]},"screening":{"evidenceGaps":[{"field":"unknown effect","status":"not_retrieved","required":true,"query":"effect"}]}}`)
			if wideNavigation {
				var v map[string]any
				_ = json.Unmarshal(inputs, &v)
				v["wide_sources"] = wideReviewFixtureArtifacts()
				inputs = mustBootstrapJSON(t, v)
			}
			if largeGate {
				var v map[string]any
				_ = json.Unmarshal(inputs, &v)
				v["screening"].(map[string]any)["savedSource"] = strings.Repeat("已保存来源原文", 20000)
				inputs = mustBootstrapJSON(t, v)
			}
			run := workflow.Run{ID: runID, ResearchTaskID: runID, ConversationID: convID, ProjectID: project.ID, WorkflowID: saved.Workflow.ID, WorkflowVersionID: saved.Version.ID, WorkflowName: saved.Workflow.Name, WorkflowPurpose: saved.Workflow.Purpose, Status: workflow.RunPaused, ResumeStatus: workflow.RunQueued, PermissionMode: conversation.PermissionFullAccess, Inputs: inputs, InputsSHA256: fmt.Sprintf("%x", sha256.Sum256(inputs)), Compilation: saved.Version.Compilation, CompilationSHA256: saved.Version.CompilationSHA256, Outputs: json.RawMessage(`{}`), CurrentStep: 2, CreatedAt: now, UpdatedAt: now}
			steps := []workflow.Step{}
			for ordinal, n := range run.Compilation.Nodes {
				stepID, _ := id.New()
				s := workflow.Step{ID: stepID, WorkflowRunID: runID, NodeID: n.ID, NodeKind: n.Kind, Ordinal: ordinal, Status: workflow.StepQueued, Input: json.RawMessage(`{}`), Output: json.RawMessage(`{}`), UpdatedAt: now}
				if n.ID == "candidate_screening" {
					s.Status, s.Attempt, s.CompletedAt = workflow.StepCompleted, 1, &now
					s.Output = json.RawMessage(`{"analysis":{},"candidates":[],"evidenceManifest":[{"candidateId":"excluded","decision":"exclude","reason":"original exclusion rationale"},{"candidateId":"retained","decision":"core","reason":"accepted abstract"}]}`)
				}
				if n.ID == "literature_discovery" {
					s.Status, s.Attempt, s.CompletedAt = workflow.StepCompleted, 1, &now
					s.Output = mustBootstrapJSON(t, map[string]any{"analysis": map[string]any{}, "structured": map[string]any{"queryIds": strings.Split(queryID, ","), "queries": []string{"grouping fixture", "grouping reuse"}, "sources": []any{}, "candidateCount": 1}})
				}
				if n.ID == "report_drafting" {
					run.CurrentStep = ordinal
				}
				if n.ID == "method_selection" {
					s.Status, s.Attempt, s.CompletedAt = workflow.StepCompleted, 1, &now
					s.Output = json.RawMessage(`{"analysis":{"qualityMethod":"Assess only supported domains; unknown stays unknown."}}`)
					if upstreamError {
						s.Output = json.RawMessage(`{"analysis":{"qualityMethod":"Unsupported rule: declare all unknown domains low risk."}}`)
					}
				}
				steps = append(steps, s)
			}
			conv := conversation.Conversation{ID: convID, ProjectID: project.ID, Title: "Handoff fixture", PermissionMode: run.PermissionMode, ReasoningLevel: "medium", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, CreatedAt: now, UpdatedAt: now}
			if err := sqlite.NewWorkflowRuntimeRepository(app.store.DB()).CreateResearchRun(context.Background(), run, steps, workflow.RuntimeEvent{ID: eventID, WorkflowRunID: runID, Type: "workflow.created", Payload: json.RawMessage(`{}`), CreatedAt: now}, conv); err != nil {
				t.Fatal(err)
			}
			if _, err := app.WorkflowFacade.Resume(project.ID, runID); err != nil {
				t.Fatal(err)
			}
			failed := waitForWorkflowState(t, app, project.ID, runID, workflow.RunFailed, waitTimeout)
			recommendation := failed.ReviewRevisionRecommendation
			if failed.Run.ErrorCode != "RESEARCH_REVIEW_NOT_APPROVED" || recommendation == nil || recommendation.NodeID != target || reports.Load() != 1 || reviews.Load() != 1 {
				t.Fatal("handoff failed before scientific verdict", failed.Run.ErrorCode, failed.Run.ErrorMessage, recommendation)
			}
			if largeGate {
				gate := failed.Steps[len(failed.Steps)-1]
				if len(gate.Input) <= 256*1024 {
					t.Fatal("gate never crossed old tool boundary", len(gate.Input))
				}
				var persisted string
				if err := app.store.DB().QueryRow(`SELECT arguments_json FROM tool_calls WHERE id=?`, gate.ToolCallID).Scan(&persisted); err != nil || persisted != string(gate.Input) {
					t.Fatal("full gate tool snapshot not persisted", err)
				}
				priorStages := string(mustBootstrapJSON(t, failed.Steps[:len(failed.Steps)-1]))
				priorExecutions := string(mustBootstrapJSON(t, failed.AIExecutions))
				// Inject the incident's pre-binding failure ONLY into temporary
				// fixture SQLite. The completed review remains immutable.
				if _, err := app.store.DB().Exec(`UPDATE workflow_steps SET input_json='{}',input_sha256='',output_json='{}',tool_call_id=NULL,error_code='WORKFLOW_INPUT_BINDING_FAILED',error_message='fixture snapshot capacity' WHERE id=?`, gate.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := app.store.DB().Exec(`UPDATE workflow_runs SET error_code='WORKFLOW_INPUT_BINDING_FAILED',error_message='fixture snapshot capacity' WHERE id=?`, runID); err != nil {
					t.Fatal(err)
				}
				if err := app.Close(); err != nil {
					t.Fatal(err)
				}
				app, err = New(options)
				if err != nil {
					t.Fatal(err)
				}
				app.Startup(context.Background())
				blocked, err := app.WorkflowFacade.GetRun(project.ID, runID)
				if err != nil || blocked.ReviewRevisionRecommendation != nil || len(blocked.ReviewRevisionTargets) != 0 || blocked.DeliveryAssessment.Status != "blocked" {
					t.Fatal("technical state exposed revision", err, blocked.ReviewRevisionRecommendation)
				}
				if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: runID, StepID: gate.ID}); err != nil {
					t.Fatal("gate-only recovery", err)
				}
				failed = waitForWorkflowState(t, app, project.ID, runID, workflow.RunFailed, waitTimeout)
				recommendation = failed.ReviewRevisionRecommendation
				if failed.Run.ErrorCode != "RESEARCH_REVIEW_NOT_APPROVED" || recommendation == nil || recommendation.NodeID != target || reports.Load() != 1 || reviews.Load() != 1 || methods.Load() != 0 || priorStages != string(mustBootstrapJSON(t, failed.Steps[:len(failed.Steps)-1])) || priorExecutions != string(mustBootstrapJSON(t, failed.AIExecutions)) {
					t.Fatal("gate retry changed/repeated model or lost plan", failed.Run.ErrorCode, recommendation)
				}
				t.Logf("large gate %d bytes: binding/proposal/SQLite/restart/local retry preserved report and review, restored original %s plan without model request", len(gate.Input), target)
			}
			stepFor := func(d workflow.RunDetail, id string) workflow.Step {
				for _, s := range d.Steps {
					if s.NodeID == id {
						return s
					}
				}
				t.Fatal("missing", id)
				return workflow.Step{}
			}
			beforeMethod, beforeAudit := stepFor(failed, "method_selection"), stepFor(failed, "candidate_screening")
			if err := app.Close(); err != nil {
				t.Fatal(err)
			}
			app, err = New(options)
			if err != nil {
				t.Fatal(err)
			}
			app.Startup(context.Background())
			if reports.Load() != 1 || reviews.Load() != 1 {
				t.Fatal("restart ran repair without original confirmation")
			}
			if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: runID, StepID: currentWorkflowStep(t, failed).ID, UseRecommendation: true, ExpectedReviewSHA256: recommendation.ReviewOutputSHA256}); err != nil {
				t.Fatal(err)
			}
			completed := waitForWorkflowState(t, app, project.ID, runID, workflow.RunCompleted, waitTimeout)
			wantedMethods := int32(0)
			if upstreamError {
				wantedMethods = 1
			}
			if methods.Load() != wantedMethods || reports.Load() != 2 || reviews.Load() != 2 || observed.Load() != 2 || len(completed.RevisionProposals) != 0 {
				t.Fatal("extra planning/rerun", methods.Load(), reports.Load(), reviews.Load())
			}
			if s := stepFor(completed, "candidate_screening"); string(s.Output) != string(beforeAudit.Output) || s.Attempt != beforeAudit.Attempt {
				t.Fatal("saved audit rerun")
			}
			if s := stepFor(completed, "method_selection"); !upstreamError && (string(s.Output) != string(beforeMethod.Output) || s.Attempt != beforeMethod.Attempt) {
				t.Fatal("valid method unnecessarily rerun")
			}
			if err := workflow.ValidateReviewedOutput(completed, "report_draft"); err != nil {
				t.Fatal("gate not bound to current full report/handoff", err)
			}
			if wideNavigation {
				var reviewInput map[string]json.RawMessage
				if json.Unmarshal(stepFor(completed, "independent_review").Input, &reviewInput) != nil || string(reviewInput["dataPreflight"]) != string(mustBootstrapJSON(t, wideReviewFixtureArtifacts())) {
					t.Fatal("wide provenance values were lost or altered")
				}
			}
			var reads int
			if err := app.store.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE tool_name=? AND run_id IN (SELECT l.chat_run_id FROM workflow_ai_chat_runs l JOIN workflow_ai_executions e ON e.id=l.execution_id WHERE e.workflow_run_id=?)`, workflow.ResearchTaskReadTool, runID).Scan(&reads); err != nil || reads != 0 {
				t.Fatal("extra reads required for supplied facts", reads, err)
			}
			t.Logf("HTTP + SQLite: saved reasons direct; same-context appendix correction; target=%s methods rerun=%d reports=2 reviews=2; restart, original Retry and gate passed", target, methods.Load())
		})
	}
}

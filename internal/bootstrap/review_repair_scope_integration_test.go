package bootstrap

import (
	"context"
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

	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Actual HTTP/SQLite/restart/confirmation/gate, scripted model decisions. No
// scientific reliability claim: the model, not the host, selects each repair.
func TestReviewRepairScopeReportOmissionKeepsMethodAndRealMethodErrorReopensIt(t *testing.T) {
	for _, upstreamError := range []bool{false, true} {
		t.Run(fmt.Sprintf("upstream_error_%t", upstreamError), func(t *testing.T) {
			var methods, reports, reviews atomic.Int32
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
					n := methods.Add(1)
					output["qualityMethod"] = "Apply the adequate method only to supported domains; leave unknown domains unjudged."
					if upstreamError && n == 1 {
						output["qualityMethod"] = "Unsupported rule: declare all unknown domains low risk."
					}
				} else if _, report := properties["markdown"]; report {
					n := reports.Add(1)
					if input["reportAssembly"] != "host-report-assembly-v1" {
						t.Error("new report attempt missing explicit host assembly version")
					}
					if !strings.Contains(prompt, "evidence-navigation-v1") || !strings.Contains(prompt, "不为补报告重复运行相同方法") {
						t.Error("report missing new guidance/index")
					}
					output["markdown"] = "# Synthetic report\nReviews are background only, not pooled or counted as additional trials. Overlap remains unknown."
					if n > 1 {
						output["markdown"] = output["markdown"].(string) + "\n## Limited assessment\nSupported domain: randomized design as recorded in this fixed fixture. Remaining domains cannot be judged from the available excerpt."
						if input["_reviewRevision"] == nil && input["_reviewCorrections"] == nil || !strings.Contains(prompt, "prior review is an opinion to verify") {
							t.Error("confirmed repair lost its history/reassessment guidance")
						}
					}
					output["claimSummary"] = []string{"Synthetic fixture, not a scientific research finding"}
					output["limitations"] = []string{"Evidence limitation retained; original promise still requires actual assessment content."}
				} else {
					n := reviews.Add(1)
					capabilities, _ := input["revisionTargetCapabilities"].(map[string]any)
					if capabilities["report_drafting"] == nil || strings.Contains(prompt, "A citation_selection stage only adopts existing references") || strings.Contains(prompt, "Their presence is expected") {
						t.Error("review missing actual repair capability/ownership guidance")
					}
					if !strings.Contains(prompt, "review-scope-v4") || !strings.Contains(prompt, "latest sufficient offered stage") || !strings.Contains(prompt, "不能单凭此值判重复计数") {
						t.Error("independent review lost responsibility guidance")
					}
					audit, _ := input["evidenceSelectionAudit"].(map[string]any)
					if audit["independentStudyCountVerified"] != false {
						t.Error("fixture host metadata was silently upgraded")
					}
					output["reviewedInputSha256"] = workflowAITestStageInputSHA256(prompt)
					output["approved"] = n > 1
					output["requiredCorrections"] = []string{}
					output["acceptanceChecks"] = []any{map[string]any{"criterionId": "criterion-1", "status": "met", "basis": "Limited assessment exists; no double-counting claimed."}}
					if n == 1 {
						issue := "Report lacks the promised limited assessment."
						if upstreamError {
							issue = "Reusable method falsely turns unknown into low risk."
						}
						output["methodIssues"] = []string{issue}
						output["requiredCorrections"] = []string{issue}
						output["acceptanceChecks"] = []any{map[string]any{"criterionId": "criterion-1", "status": "not_met", "basis": issue}}
					}
					applyTrackedReviewFixture(output, prompt)
					if n == 1 {
						plan := output["revisionPlan"].([]any)[0].(map[string]any)
						plan["nodeId"], plan["reason"], plan["whyNotLater"] = target, "Missing assessment belongs in the report; the adequate method is reusable.", "Report is the final producer of the assessment."
						if upstreamError {
							plan["reason"], plan["whyNotLater"] = "Current method declares unknown domains low risk.", "Editing report leaves the reusable wrong qualityMethod in place."
							finding := output["reviewFindings"].([]any)[0].(map[string]any)
							finding["basis"] = []any{map[string]any{"pointer": "/methodContext/qualityMethod", "quote": "declare all unknown domains low risk"}}
						}
					}
					for _, category := range []string{"unsupportedClaims", "citationIssues", "numericIssues", "methodIssues"} {
						delete(output, category)
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				answer := "```json\n" + string(mustBootstrapJSON(t, output)) + "\n```"
				chunk := mustBootstrapJSON(t, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": answer}, "finish_reason": "stop"}}})
				fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			}))
			defer server.Close()
			options := Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}}
			app, err := New(options)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = app.Close() }()
			app.Startup(context.Background())
			profile := saveWorkflowAITestProfile(t, app, server.URL)
			project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Review repair scope isolated fixture"})
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
			saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: project.ID, Definition: workflow.Definition{
				SchemaVersion: 1, Name: "Report omissions versus real method errors",
				Inputs: []workflow.Port{{Name: "research_contract", Type: workflow.TypeObject, Required: true}, {Name: "selection_audit", Type: workflow.TypeObject, Required: true}},
				Nodes: []workflow.Node{
					{ID: "method_selection", Name: "Existing quality method", Kind: workflow.NodeAIAnalysis, Arguments: json.RawMessage(`{}`), PromptVersion: "scope-fixture-method-v1", Prompt: "Describe the quality-assessment method.", ReviewPolicy: workflow.AIReviewAuto, OutputSchema: json.RawMessage(`{"type":"object","required":["qualityMethod"],"properties":{"qualityMethod":{"type":"string"}},"additionalProperties":false}`)},
					{ID: "report_drafting", Name: "Report", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: "dynamic-report-v6", Prompt: "Present the agreed limited assessment.", ReviewPolicy: workflow.AIReviewAuto, AllowedTools: []string{workflow.ResearchTaskReadTool}, OutputSchema: reportSchema},
					{ID: "independent_review", Name: "Independent review", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: "scope-fixture-review-v1", Prompt: "Independently check the current report.", ReviewPolicy: workflow.AIReviewAuto, AllowedTools: []string{workflow.ResearchTaskReadTool}, OutputSchema: reviewSchema},
					{ID: "delivery_gate", Name: "Delivery gate", Kind: workflow.NodeTool, ToolName: "builtin.research.workflow.review.gate", Arguments: json.RawMessage(`{}`)},
				},
				Edges: []workflow.Edge{
					{FromNode: "$input", FromPort: "research_contract", ToNode: "report_drafting", ToPort: "researchContract"},
					{FromNode: "$input", FromPort: "research_contract", ToNode: "independent_review", ToPort: "researchContract"},
					{FromNode: "$input", FromPort: "selection_audit", ToNode: "report_drafting", ToPort: "evidenceSelectionAudit"},
					{FromNode: "$input", FromPort: "selection_audit", ToNode: "independent_review", ToPort: "evidenceSelectionAudit"},
					{FromNode: "method_selection", FromPort: "analysis", ToNode: "report_drafting", ToPort: "methodContext"},
					{FromNode: "method_selection", FromPort: "analysis", ToNode: "independent_review", ToPort: "methodContext"},
					{FromNode: "report_drafting", FromPort: "analysis", ToNode: "independent_review", ToPort: "context"},
					{FromNode: "report_drafting", FromPort: "analysis", ToNode: "delivery_gate", ToPort: "subject"},
					{FromNode: "independent_review", FromPort: "analysis", ToNode: "delivery_gate", ToPort: "review"},
				},
				Outputs: []workflow.Output{{Name: "report_draft", Type: workflow.TypeObject, FromNode: "report_drafting", FromPort: "analysis", Required: true}, {Name: "delivery_gate", Type: workflow.TypeObject, FromNode: "delivery_gate", FromPort: "structured", Required: true}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			started, err := app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: project.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"research_contract":{"successCriteria":["Present a supported limited quality assessment; do not double-count trials"]},"selection_audit":{"independentStudyCountVerified":false,"distinctDocumentCount":7,"limitedEvidenceAccepted":true}}`), PermissionMode: "full_access", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
			if err != nil {
				t.Fatal(err)
			}
			failed := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunFailed, 20*time.Second)
			recommendation := failed.ReviewRevisionRecommendation
			wantedStages := 3
			if upstreamError {
				wantedStages = 4
			}
			if failed.Run.ErrorCode != "RESEARCH_REVIEW_NOT_APPROVED" || recommendation == nil || recommendation.NodeID != target || len(recommendation.AffectedStages) != wantedStages || len(failed.RevisionProposals) != 0 || reports.Load() != 1 || methods.Load() != 1 || reviews.Load() != 1 {
				t.Fatal("rejection/recommendation did not persist directly", failed.Run.ErrorCode, failed.Run.ErrorMessage, recommendation)
			}
			var priorMethod workflow.Step
			for _, s := range failed.Steps {
				if s.NodeID == "method_selection" {
					priorMethod = s
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
			restored, err := app.WorkflowFacade.GetRun(project.ID, failed.Run.ID)
			if err != nil || restored.ReviewRevisionRecommendation == nil || restored.ReviewRevisionRecommendation.ReviewOutputSHA256 != recommendation.ReviewOutputSHA256 || reports.Load() != 1 {
				t.Fatal("restart changed/replayed the rejected plan", err)
			}
			if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: failed.Run.ID, StepID: currentWorkflowStep(t, failed).ID, UseRecommendation: true, ExpectedReviewSHA256: recommendation.ReviewOutputSHA256}); err != nil {
				t.Fatal(err)
			}
			completed := waitForWorkflowState(t, app, project.ID, failed.Run.ID, workflow.RunCompleted, 20*time.Second)
			wantedMethods := int32(1)
			if upstreamError {
				wantedMethods = 2
			}
			if methods.Load() != wantedMethods || reports.Load() != 2 || reviews.Load() != 2 || len(completed.RevisionProposals) != 0 {
				t.Fatal("extra planning or unnecessary rerun", methods.Load(), reports.Load(), reviews.Load())
			}
			if !upstreamError {
				for _, s := range completed.Steps {
					if s.NodeID == priorMethod.NodeID && (s.Attempt != priorMethod.Attempt || string(s.Output) != string(priorMethod.Output)) {
						t.Fatal("report-only repair changed valid method")
					}
				}
			}
			if err := workflow.ValidateReviewedOutput(completed, "report_draft"); err != nil {
				t.Fatal("unchanged conservative metadata caused an endless rejection", err)
			}
			t.Logf("controlled HTTP: target=%s stages=%d methods=%d reports=%d reviews=%d; explicit confirmation only, no extra proposal", target, wantedStages, methods.Load(), reports.Load(), reviews.Load())
		})
	}
}

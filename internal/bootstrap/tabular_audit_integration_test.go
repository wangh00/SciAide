package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/platform/pythonruntime"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Real HTTP adapter, temporary SQLite and actual isolated Python. Controlled
// model replies test transport/repair boundaries, not scientific reasoning.
func TestTabularAuditPreservesDirectRepairAndScientificConfirmation(t *testing.T) {
	for _, target := range []string{"report_drafting", "method_implementation"} {
		t.Run(target, func(t *testing.T) {
			const csvBody = "row_label,partition,measure,score\n001,train,2,5\n002,train,2,5\n003,test,4,6\n004,test,4,6\n"
			const incorrect = "# Report\nFour independent groups were verified from unique row labels; all p-values were adjusted."
			const corrected = "# Report\nRow labels do not verify sample identity. Repeated measurement combinations are observed, not proven independent samples. The supplemental p=0.04 is raw and descriptive, not adjusted significance evidence."
			var implementations, reports, reviews atomic.Int32
			base := newResearchStarterOutputTestServer(t, func(out map[string]any, prompt string) {
				input := workflowAITestStageInput(prompt)
				if _, implementation := out["analysisInput"]; implementation {
					n := implementations.Add(1)
					if !strings.Contains(prompt, "Computational observation and inference audit") {
						t.Error("implementation missing computational audit guidance")
					}
					audit := tabularAuditFixtureValue(t, input, "dataContext")["tabularEqualityAudit"].(map[string]any)
					if audit["fullRows"].(map[string]any)["repeatedGroupCount"] != float64(0) || audit["withoutFullyUniqueColumns"].(map[string]any)["repeatedGroupCount"] != float64(2) || audit["identityVerified"] != false {
						t.Error("actual preflight masked repetitions or fabricated identity", audit)
					}
					key := "row_label"
					out["researchChanges"] = []any{}
					if target == "method_implementation" && n == 2 {
						key = "measure"
						out["researchChanges"] = []any{map[string]any{
							"before": "resample unique row labels as records", "after": "resample equal measurement groups as an explicit sensitivity assumption",
							"reason": "row-label uniqueness did not establish independence", "impact": "uncertainty and grouping assumption, no deletion or repartition",
						}}
					}
					out["_fixturePythonCode"] = strings.ReplaceAll(tabularAuditFixtureCode, "__GROUP_KEY__", key)
				}
				if _, report := out["markdown"]; report {
					n := reports.Add(1)
					if !strings.Contains(prompt, "raw from adjusted p-values") {
						t.Error("report missing inference distinctions")
					}
					out["markdown"], out["claimSummary"] = incorrect, []any{incorrect}
					if n > 1 {
						out["markdown"], out["claimSummary"] = corrected, []any{corrected}
					}
				}
			}, mutateStarterToEmpiricalReportRoute)
			var observedSkillLoads sync.Map // actual current review binding
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
				if err != nil {
					t.Error(err)
					http.Error(w, "read request", 400)
					return
				}
				prompt := workflowAITestPrompt(body)
				schema, err := workflowAITestSchema(prompt)
				properties, _ := schema["properties"].(map[string]any)
				_, review := properties["reviewedInputSha256"]
				binding := ""
				if start := strings.LastIndex(prompt, "<review_submission_sources>"); start >= 0 {
					raw := prompt[start+len("<review_submission_sources>"):]
					end := strings.Index(raw, "</review_submission_sources>")
					var menu struct{ BindingSHA256 string }
					if end >= 0 && json.Unmarshal([]byte(raw[:end]), &menu) == nil {
						binding = menu.BindingSHA256
					}
				}
				if review && binding != "" && resourceFixtureSkillLoaded(body, "scientific-writing") {
					observedSkillLoads.Store(binding, true)
				}
				_, loaded := observedSkillLoads.Load(binding)
				if err != nil || !review || !loaded {
					r.Body = io.NopCloser(bytes.NewReader(body))
					base.Config.Handler.ServeHTTP(w, r)
					return
				}
				n := reviews.Add(1)
				if idx := strings.LastIndex(prompt, "Host submission validation failed"); idx >= 0 {
					end := min(len(prompt), idx+1800)
					t.Log("review correction feedback", prompt[idx:end])
				}
				if !strings.Contains(prompt, "not just the first reporting issue") || !strings.Contains(prompt, "latest offered repair stage") {
					t.Error("review missing comprehensive/minimal-repair guidance")
				}
				input := workflowAITestStageInput(prompt)
				preflight := tabularAuditFixtureValue(t, input, "dataPreflight")
				if preflight["rowCount"] != float64(4) || preflight["tabularEqualityAudit"] == nil {
					t.Error("review lost actual tabular observations", preflight)
				}
				computed := tabularAuditFixtureValue(t, input, "computedResults")
				if computed["rowCount"] != float64(4) || computed["measurementGroupCount"] != float64(2) || computed["rawSupplementalP"] != 0.04 {
					t.Error("review lost executed values", computed)
				}
				out := workflowAISchemaFixture(schema).(map[string]any)
				out["reviewedInputSha256"], out["approved"] = workflowAITestStageInputSHA256(prompt), true
				applyResearchAcceptanceFixture(out, prompt)
				if n == 1 {
					out["approved"] = false
					out["methodIssues"] = []any{"The report mislabels an unused supplemental raw p-value as adjusted."}
					out["requiredCorrections"] = []any{"Describe supplemental raw p-values accurately without changing their use or correct calculated values."}
					if target == "method_implementation" {
						out["methodIssues"] = append(out["methodIssues"].([]any), "Unique row labels were used as supposedly independent resampling groups.")
						out["requiredCorrections"] = append(out["requiredCorrections"].([]any), "Correct the grouping assumption and reusable uncertainty output; preserve records and partition.")
					}
				}
				applyTrackedReviewFixture(out, prompt)
				for index, rawPlan := range out["revisionPlan"].([]any) {
					plan := rawPlan.(map[string]any)
					owner := "report_drafting" // the first defect is raw-p wording
					if target == "method_implementation" && index == 1 {
						owner = target
					}
					plan["nodeId"] = owner
					plan["reason"] = "Correct the actual responsible output at the latest sufficient offered stage."
					plan["whyNotLater"] = "The selected output owns this defect; a later-only edit leaves it unchanged."
					contract, _ := input["revisionWriteContract"].(map[string]any)
					objects, _ := contract["objects"].([]any)
					found := false
					for _, raw := range objects {
						object, _ := raw.(map[string]any)
						if object["ownerNodeId"] == owner && (object["kind"] == "stage_result" || object["kind"] == "report_authored_body") {
							plan["editTargets"] = []any{object["id"]}
							found = true
							break
						}
					}
					if !found {
						t.Error("responsible write object missing", owner)
					}
				}
				for _, key := range []string{"unsupportedClaims", "citationIssues", "numericIssues", "methodIssues"} {
					delete(out, key)
				}
				encoded, _ := json.Marshal(out)
				if !emitRequiredPlannerResultFixture(t, w, modelprofile.ProtocolOpenAIChat, body, string(encoded)) {
					t.Error("current native review source-selection interface missing")
				}
			}))
			defer server.Close()
			root := t.TempDir()
			app, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = app.Close() }()
			app.Startup(context.Background())
			profile := saveWorkflowAITestProfile(t, app, server.URL)
			project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "tabular observation and repair"})
			if err != nil {
				t.Fatal(err)
			}
			discovery, err := app.PythonFacade.DetectInterpreters()
			if err != nil || len(discovery.Interpreters) == 0 {
				t.Fatal("actual Python required for this integration fixture", err)
			}
			env := filepath.Join(t.TempDir(), "python")
			if err := pythonruntime.New(app.pythonexec).CreateEnvironment(context.Background(), discovery.Interpreters[0].ExecutablePath, env); err != nil {
				t.Fatal(err)
			}
			if _, err := app.PythonFacade.BindProjectEnvironment(project.ID, filepath.Join(env, "Scripts", "python.exe")); err != nil {
				t.Fatal(err)
			}
			inputPath := filepath.Join(project.WorkspacePath, "input.csv")
			if err := os.WriteFile(inputPath, []byte(csvBody), 0600); err != nil {
				t.Fatal(err)
			}
			started, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "Inspect repeated measurement combinations and uncertainty; preserve all records and split. Supplemental p-values are descriptive.", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
			if err != nil {
				t.Fatal(err)
			}
			planned := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunCompleted, 20*time.Second)
			adopted, err := app.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: project.ID, RunID: planned.Run.ID, RouteID: "empirical_report"})
			if err != nil {
				t.Fatal(err)
			}
			var inputs map[string]any
			if json.Unmarshal(adopted.InitialInputs, &inputs) != nil {
				t.Fatal("invalid adopted inputs")
			}
			inputs["input_paths"] = []string{"input.csv"}
			formal, err := app.WorkflowFacade.StartAdoptedResearchRoute(workflow.StartAdoptedResearchRouteCommand{ProjectID: project.ID, StarterRunID: planned.Run.ID, RouteID: adopted.RouteID, WorkflowID: adopted.Workflow.Workflow.ID, WorkflowVersionID: adopted.Workflow.Version.ID, Inputs: mustBootstrapJSON(t, inputs), ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
			if err != nil {
				t.Fatal(err)
			}
			failed := waitForWorkflowState(t, app, project.ID, formal.Run.ID, workflow.RunFailed, 40*time.Second)
			recommendation := failed.ReviewRevisionRecommendation
			if failed.Run.ErrorCode != "RESEARCH_REVIEW_NOT_APPROVED" || recommendation == nil || recommendation.Status != "recommended" || recommendation.NodeID != target || len(failed.HumanDecisions) != 0 {
				t.Fatal("review was coerced into approval, a program error or extra input gate", failed.Run.ErrorCode, failed.Run.ErrorMessage, recommendation)
			}
			firstReview := tabularAuditFixtureStep(t, failed, "independent_review")
			firstKernel := tabularAuditFixtureStep(t, failed, "python_analysis")
			gate := tabularAuditFixtureStep(t, failed, "delivery_gate")
			repair := tabularAuditFixtureStep(t, failed, target)
			if err := app.Close(); err != nil {
				t.Fatal(err)
			}
			app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
			if err != nil {
				t.Fatal(err)
			}
			app.Startup(context.Background())
			reopened, err := app.WorkflowFacade.GetRun(project.ID, formal.Run.ID)
			if err != nil || !bytes.Equal(tabularAuditFixtureStep(t, reopened, "independent_review").Output, firstReview.Output) || reviews.Load() != 1 || implementations.Load() != 1 {
				t.Fatal("restart rewrote/replayed historical review", err)
			}
			if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: formal.Run.ID, StepID: gate.ID, UseRecommendation: true, ExpectedReviewSHA256: recommendation.ReviewOutputSHA256, ConfirmSideEffect: target == "method_implementation"}); err != nil {
				t.Fatal(err)
			}
			if target == "method_implementation" {
				waiting := waitForWorkflowState(t, app, project.ID, formal.Run.ID, workflow.RunWaitingHumanConfirmation, 25*time.Second)
				implementation := tabularAuditFixtureStep(t, waiting, target)
				var executed int
				if err := app.store.DB().QueryRow(`SELECT count(*) FROM python_kernel_executions WHERE workflow_run_id=? AND status='success'`, formal.Run.ID).Scan(&executed); err != nil || executed != 2 || reviews.Load() != 1 || reports.Load() != 1 || implementation.Status != workflow.StepWaitingHumanConfirmation {
					t.Fatal("scientific change executed before existing confirmation", executed, err)
				}
				if _, err := app.WorkflowFacade.Decide(workflow.HumanDecisionCommand{ProjectID: project.ID, RunID: formal.Run.ID, StepID: implementation.ID, Approved: true}); err != nil {
					t.Fatal(err)
				}
			}
			final := waitForWorkflowState(t, app, project.ID, formal.Run.ID, workflow.RunCompleted, 40*time.Second)
			if reviews.Load() != 2 || reports.Load() != 2 || !bytes.Equal(failed.Run.Inputs, final.Run.Inputs) || !bytes.Equal(mustBootstrapJSON(t, failed.Run.Compilation), mustBootstrapJSON(t, final.Run.Compilation)) {
				t.Fatal("repair added extra model pass or rewrote frozen contract")
			}
			for _, prior := range failed.Steps {
				current := tabularAuditFixtureStep(t, final, prior.NodeID)
				if prior.Ordinal < repair.Ordinal && !bytes.Equal(mustBootstrapJSON(t, prior), mustBootstrapJSON(t, current)) {
					t.Fatal("unaffected predecessor replayed or mutated", prior.NodeID)
				}
			}
			if actual, err := os.ReadFile(inputPath); err != nil || string(actual) != csvBody {
				t.Fatal("repair changed original data", err)
			}
			var savedReview string
			if err := app.store.DB().QueryRow(`SELECT output_json FROM workflow_ai_executions WHERE workflow_step_id=? AND attempt=?`, firstReview.ID, firstReview.Attempt).Scan(&savedReview); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(savedReview, "mislabels") || strings.Contains(savedReview, `"approved":true`) {
				t.Fatal("old review became retroactively approved", savedReview)
			}
			lastReview := map[string]any{}
			_ = json.Unmarshal(tabularAuditFixtureStep(t, final, "independent_review").Output, &lastReview)
			for _, finding := range lastReview["analysis"].(map[string]any)["reviewFindings"].([]any) {
				if finding.(map[string]any)["status"] != "resolved" {
					t.Fatal("second controlled review lost stable resolution", finding)
				}
			}
			lastKernel := tabularAuditFixtureStep(t, final, "python_analysis")
			if target == "report_drafting" {
				if implementations.Load() != 1 || !bytes.Equal(firstKernel.Output, lastKernel.Output) || firstKernel.Attempt != lastKernel.Attempt || len(final.HumanDecisions) != 0 {
					t.Fatal("documentation-only repair recalculated or required scientific consent", implementations.Load(), final.HumanDecisions)
				}
			} else {
				if implementations.Load() != 2 || lastKernel.Attempt != firstKernel.Attempt+1 || bytes.Equal(firstKernel.Output, lastKernel.Output) {
					t.Fatal("grouping repair did not recompute after confirmation")
				}
				var previous, current map[string]any
				_ = json.Unmarshal(firstKernel.Output, &previous)
				_ = json.Unmarshal(lastKernel.Output, &current)
				oldValue := tabularAuditFixtureValue(t, previous, "structured")
				newValue := tabularAuditFixtureValue(t, current, "structured")
				if oldValue["resamplingGroupCount"] != float64(4) || newValue["resamplingGroupCount"] != float64(2) || oldValue["bootstrapVariance"] == newValue["bootstrapVariance"] {
					t.Fatal("actual resampling values did not change", oldValue, newValue)
				}
			}
			for _, execution := range final.AIExecutions {
				node := tabularAuditFixtureStep(t, final, tabularAuditFixtureNodeID(final, execution.WorkflowStepID)).NodeID
				if node == "method_selection" || node == "method_implementation" || node == "result_interpretation" || node == "report_drafting" || node == "independent_review" {
					version := execution.PromptVersion
					hasAudit := strings.HasSuffix(version, "-data-audit-v2") || strings.Contains(version, "-data-audit-v2-")
					if strings.HasPrefix(version, "p-") {
						hasAudit = hasAudit || strings.HasSuffix(version, "-da2") || strings.Contains(version, "-da2-")
					}
					if !hasAudit || len(execution.PromptSHA256) != 64 {
						t.Fatal("executed guidance lacks persistent identity", node, execution.PromptVersion)
					}
				}
			}
			t.Log("actual preflight/CSV/Kernel -> controlled rejection -> restart -> original direct repair -> correct suffix; raw p alone is not a host veto, grouping changes wait for existing consent")
		})
	}
}

func tabularAuditFixtureValue(t *testing.T, object map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := object[key].(map[string]any)
	if !ok {
		t.Fatalf("missing fixture field %s: %v", key, object)
	}
	if inner, ok := value["value"].(map[string]any); ok {
		return inner
	}
	return value
}

func tabularAuditFixtureStep(t *testing.T, detail workflow.RunDetail, nodeID string) workflow.Step {
	t.Helper()
	for _, step := range detail.Steps {
		if step.NodeID == nodeID {
			return step
		}
	}
	t.Fatal("missing fixture step", nodeID)
	return workflow.Step{}
}

func tabularAuditFixtureNodeID(detail workflow.RunDetail, stepID string) string {
	for _, step := range detail.Steps {
		if step.ID == stepID {
			return step.NodeID
		}
	}
	return ""
}

const tabularAuditFixtureCode = `import csv
import json
import random
from pathlib import Path
with Path(SCIAIDE_INPUTS[0]).open("r", encoding="utf-8-sig", newline="") as handle:
    rows = list(csv.DictReader(handle))
groups = {}
for row in rows:
    groups.setdefault(row["__GROUP_KEY__"], []).append(float(row["measure"]))
cluster_values = list(groups.values())
rng = random.Random(101)
means = []
for _ in range(200):
    values = []
    for _ in cluster_values:
        values.extend(rng.choice(cluster_values))
    means.append(sum(values) / len(values))
center = sum(means) / len(means)
result = {"rowCount": len(rows), "measurementGroupCount": len(set(row["measure"] for row in rows)), "resamplingGroupCount": len(groups), "bootstrapVariance": sum((value - center)**2 for value in means) / len(means), "rawSupplementalP": 0.04, "identityVerified": False}
Path(SCIAIDE_OUTPUTS[0]).write_text(json.dumps(result), encoding="utf-8")
Path(SCIAIDE_OUTPUTS[1]).write_text("# Methods\nResampling key: __GROUP_KEY__. Equal measurements do not prove sample identity. Raw supplemental p is descriptive. Fixture values are not wine research results.", encoding="utf-8")
result`

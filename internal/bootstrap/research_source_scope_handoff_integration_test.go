package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/research/connectors"
	wails "github.com/wangh00/SciAide/internal/transport/wails"
)

// TestResearchSourceScopeHandoffFromNaturalLanguage exercises one complete
// controlled path: a natural-language source restriction is frozen in the
// starter request, the planner selects a literature route, the retrieval
// planner emits structured source IDs, and the discovery tool actually calls
// only those sources. The model endpoint is scripted; this proves host
// handoff/binding, not semantic understanding by a real model.
func TestResearchSourceScopeHandoffFromNaturalLanguage(t *testing.T) {
	testResearchSourceScopeHandoff(t, false)
}

func TestResearchSourceScopeHandoffWithSinceYearToPresent(t *testing.T) {
	testResearchSourceScopeHandoff(t, true)
}

func testResearchSourceScopeHandoff(t *testing.T, withYears bool) {
	const sourceConstraint = "ERIC、OpenAlex、Crossref"
	var retrievalPromptSawScope atomic.Bool
	var anchoredYear atomic.Int32
	model := newResearchStarterOutputTestServer(t, func(output map[string]any, prompt string) {
		if _, starter := output["normalizedQuestion"]; starter {
			for _, rawRoute := range sourceScopeAnySlice(output["routes"]) {
				route, _ := rawRoute.(map[string]any)
				stages := sourceScopeAnySlice(route["stagePlans"])
				if sourceScopeHasStage(stages, "literature_discovery") {
					continue
				}
				// The fixture's first route is already host-valid. Insert the
				// evidence stage after question refinement so materialization
				// compiles the public-literature path.
				insertAt := 1
				if insertAt > len(stages) {
					insertAt = len(stages)
				}
				evidence := map[string]any{"stageId": "literature_discovery", "objective": "按用户限定的三个数据库发现公共文献", "methods": []any{}, "skillNames": []any{}}
				stages = append(stages, nil)
				copy(stages[insertAt+1:], stages[insertAt:])
				stages[insertAt] = evidence
				route["stagePlans"] = stages
			}
		}
		if _, retrieval := output["retrievalProfile"]; retrieval {
			if strings.Contains(prompt, "ERIC") && strings.Contains(prompt, "OpenAlex") && strings.Contains(prompt, "Crossref") {
				retrievalPromptSawScope.Store(true)
			}
			output["retrievalProfile"] = "education"
			output["sourceRationale"] = "用户明确限定教育主题检索只使用 ERIC、OpenAlex、Crossref。"
			output["sourceIds"] = []any{"eric", "openalex", "crossref"}
			output["queries"] = []any{"writing feedback higher education"}
			output["rationale"] = []any{"覆盖高等教育中的写作反馈主题"}
			output["providerQueries"] = map[string]any{
				"eric":     []any{"writing feedback higher education"},
				"openalex": []any{"writing feedback higher education"},
				"crossref": []any{"writing feedback higher education"},
			}
			output["publicationYears"] = map[string]any{"from": 0, "to": 0}
			output["yearEvidence"] = ""
			if withYears {
				input := workflowAITestStageInput(prompt)
				clock, _ := input["researchTimeContext"].(map[string]any)
				date, _ := clock["date"].(string)
				anchor, err := time.Parse("2006-01-02", date)
				if err != nil {
					t.Error("missing original frozen date", err)
					return
				}
				anchoredYear.Store(int32(anchor.Year()))
				output["publicationYears"] = map[string]any{"from": 2015, "to": anchor.Year()}
				output["yearEvidence"] = "2015年至今"
			}
		}
	}, nil)
	defer model.Close()

	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if withYears {
			// The scholarly keywords contain no years. Verify the actual routed
			// source requests carry both filter endpoints, not just planner text.
			query := r.URL.Query().Encode()
			if !strings.Contains(query, "2015") || !strings.Contains(query, fmt.Sprint(anchoredYear.Load())) {
				t.Errorf("source HTTP lost frozen year filter: %s", r.Header.Get("X-Fixture-Source"))
			}
		}
		switch r.Header.Get("X-Fixture-Source") {
		case "api.ies.ed.gov":
			if r.URL.Path != "/eric/" {
				http.Error(w, "unexpected ERIC path", http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `{"response":{"numFound":1,"docs":[{"id":"EJ1234567","title":"Controlled writing feedback study","description":"Controlled abstract.","publicationdateyear":2025,"e_fulltextauth":0}]}}`)
		case "api.openalex.org":
			if r.URL.Path != "/works" {
				http.Error(w, "unexpected OpenAlex path", http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `{"meta":{"count":1},"results":[{"id":"https://openalex.org/W123","title":"Controlled higher education feedback","publication_year":2025}]}`)
		case "api.crossref.org":
			fmt.Fprint(w, `{"status":"ok","message":{"items":[]}}`)
		default:
			http.Error(w, "source escaped explicit scope", http.StatusBadRequest)
		}
	}))
	defer sourceServer.Close()
	endpoint, err := url.Parse(sourceServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &routingFixtureTransport{endpoint: endpoint}

	app, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}, ResearchConnectors: connectors.DefaultWithTransport(transport)})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, app, model.URL)
	project, err := app.ProjectFacade.CreateProject(wails.CreateProjectRequest{Name: "自然语言选源交接"})
	if err != nil {
		t.Fatal(err)
	}
	idea := "研究高等教育写作反馈；文献检索只使用 " + sourceConstraint + " 三个数据库，不使用其他数据库；先形成文献综述，不进行招募。"
	if withYears {
		idea += "发表时间限定2015年至今。"
	}
	started, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: idea, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	planned := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunCompleted, 20*time.Second)
	plan, err := app.WorkflowFacade.GetResearchStarterPlan(project.ID, planned.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Routes) == 0 || plan.RecommendedRouteID == "" {
		t.Fatalf("planner did not return a route: %#v", plan)
	}
	selected := plan.Routes[0]
	for _, route := range plan.Routes {
		if route.RouteID == plan.RecommendedRouteID {
			selected = route
			break
		}
	}
	if !sourceScopeContains(selected.StageIDs, "literature_discovery") {
		t.Fatalf("natural-language source request did not reach a literature route: stages=%v route=%#v", selected.StageIDs, selected)
	}

	adopted, err := app.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: project.ID, RunID: planned.Run.ID, RouteID: plan.RecommendedRouteID})
	if err != nil || adopted.Run == nil {
		t.Fatalf("adopt route: %#v, %v", adopted, err)
	}
	detail := waitForSourceScopeLiteratureDiscovery(t, app, project.ID, adopted.Run.Run.ID, 30*time.Second)
	queryPlan := sourceScopeCompletedStep(t, detail, "literature_query_expansion")
	if !retrievalPromptSawScope.Load() {
		preview := string(queryPlan.Input)
		if len(preview) > 3000 {
			preview = preview[:3000]
		}
		t.Fatalf("literature retrieval planner did not receive the original source restriction; input=%s", preview)
	}
	var queryPlanEnvelope struct {
		Analysis struct {
			SourceIDs []string `json:"sourceIds"`
		} `json:"analysis"`
	}
	if err := json.Unmarshal(queryPlan.Output, &queryPlanEnvelope); err != nil {
		t.Fatalf("decode query plan: %v; output=%s", err, queryPlan.Output)
	}
	got := append([]string(nil), queryPlanEnvelope.Analysis.SourceIDs...)
	sort.Strings(got)
	if strings.Join(got, ",") != "crossref,eric,openalex" {
		t.Fatalf("structured source plan changed: %v; output=%s", queryPlanEnvelope.Analysis.SourceIDs, queryPlan.Output)
	}
	if withYears {
		for _, execution := range detail.AIExecutions {
			if execution.WorkflowStepID == queryPlan.ID && (execution.Attempt != 1 || execution.ModelTurns != 1) {
				t.Fatal("a valid anchored first submission required retries", execution.Attempt, execution.ModelTurns)
			}
		}
	}
	discovery := sourceScopeCompletedStep(t, detail, "literature_discovery")
	var discoveryInput struct {
		SourceIDs []string `json:"sourceIds"`
	}
	if err := json.Unmarshal(discovery.Input, &discoveryInput); err != nil {
		t.Fatalf("decode discovery input: %v; input=%s", err, discovery.Input)
	}
	got = append([]string(nil), discoveryInput.SourceIDs...)
	sort.Strings(got)
	if strings.Join(got, ",") != "crossref,eric,openalex" {
		t.Fatalf("discovery input lost frozen source IDs: %v; input=%s", discoveryInput.SourceIDs, discovery.Input)
	}
	if withYears {
		var recorded struct {
			Years struct{ From, To int } `json:"publicationYears"`
		}
		if err := json.Unmarshal(discovery.Input, &recorded); err != nil || recorded.Years.From != 2015 || recorded.Years.To != int(anchoredYear.Load()) {
			t.Fatal("discovery did not freeze the parsed range", err, recorded.Years)
		}
	}
	if gotCalls := transport.calls.Load(); gotCalls != 3 {
		t.Fatalf("explicit three-source scope made %d upstream calls, want 3", gotCalls)
	}
}

func sourceScopeAnySlice(value any) []any {
	values, _ := value.([]any)
	return values
}

func sourceScopeHasStage(stages []any, id string) bool {
	for _, raw := range stages {
		stage, _ := raw.(map[string]any)
		if stage["stageId"] == id {
			return true
		}
	}
	return false
}

func sourceScopeContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sourceScopeCompletedStep(t *testing.T, detail workflow.RunDetail, nodeID string) workflow.Step {
	t.Helper()
	for _, step := range detail.Steps {
		if step.NodeID == nodeID && step.Status == workflow.StepCompleted {
			return step
		}
	}
	t.Fatalf("completed step %q not found: %#v", nodeID, detail.Steps)
	return workflow.Step{}
}

func waitForSourceScopeLiteratureDiscovery(t *testing.T, app *Application, projectID, runID string, timeout time.Duration) workflow.RunDetail {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		detail, err := app.WorkflowFacade.GetRun(projectID, runID)
		if err != nil {
			t.Fatal(err)
		}
		for _, step := range detail.Steps {
			if step.NodeID == "literature_discovery" {
				if step.Status == workflow.StepCompleted {
					return detail
				}
				if step.Status == workflow.StepFailed {
					t.Fatalf("literature discovery failed: %s", step.ErrorMessage)
				}
			}
		}
		if detail.Run.Status == workflow.RunFailed {
			var states []string
			for _, failedStep := range detail.Steps {
				output := string(failedStep.Output)
				if len(output) > 600 {
					output = output[:600]
				}
				states = append(states, fmt.Sprintf("%s=%s(%s output=%s)", failedStep.NodeID, failedStep.Status, failedStep.ErrorMessage, output))
			}
			var attempts []string
			for _, execution := range detail.AIExecutions {
				if execution.WorkflowStepID == "" || execution.Status == "completed" {
					continue
				}
				text := execution.OutputText
				if len(text) > 800 {
					text = text[:800]
				}
				attempts = append(attempts, fmt.Sprintf("%s=%s(%s output=%s text=%s)", execution.WorkflowStepID, execution.Status, execution.ErrorMessage, string(execution.Output), text))
			}
			t.Fatalf("formal route failed before literature discovery: %s; steps=%s; attempts=%s", detail.Run.ErrorMessage, strings.Join(states, "; "), strings.Join(attempts, "; "))
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("timed out waiting for literature discovery")
	return workflow.RunDetail{}
}

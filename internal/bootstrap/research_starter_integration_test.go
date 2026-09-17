package bootstrap

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/platform/pythonruntime"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestResearchStarterAutomaticallyCorrectsInvalidStructuredOutput(t *testing.T) {
	var mu sync.Mutex
	completedResponses := 0
	server := newResearchStarterSkillTestServer(t, func(plan map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		completedResponses++
		if completedResponses != 1 {
			return
		}
		routes, _ := plan["routes"].([]any)
		if len(routes) == 0 {
			return
		}
		route, _ := routes[0].(map[string]any)
		stagePlans, _ := route["stagePlans"].([]any)
		if len(stagePlans) == 0 {
			return
		}
		stage, _ := stagePlans[0].(map[string]any)
		if stage != nil {
			delete(stage, "objective")
		}
	})
	application, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	application.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, application, server.URL)
	projectValue, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "结构化输出自动纠正"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.WorkflowFacade.StartResearch(workflow.StartResearchCommand{
		ProjectID: projectValue.ID, ResearchIdea: "研究夜间使用手机与睡眠的关系", ModelProfileID: profile.ID, ModelID: workflowAITestModelID,
	})
	if err != nil {
		t.Fatal(err)
	}
	completed := waitForWorkflowState(t, application, projectValue.ID, started.Run.ID, workflow.RunCompleted, 20*time.Second)
	if completed.Run.ErrorCode != "" {
		t.Fatalf("automatically corrected starter retained an error: %#v", completed.Run)
	}
	mu.Lock()
	responseCount := completedResponses
	mu.Unlock()
	if responseCount != 2 {
		t.Fatalf("completed model responses = %d, want invalid + corrected", responseCount)
	}
	rows, err := application.store.DB().Query(`SELECT status,error_code FROM workflow_ai_executions WHERE workflow_run_id=? ORDER BY attempt`, started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var states [][2]string
	for rows.Next() {
		var state [2]string
		if err := rows.Scan(&state[0], &state[1]); err != nil {
			t.Fatal(err)
		}
		states = append(states, state)
	}
	if len(states) != 1 || states[0][0] != "completed" {
		t.Fatalf("AI execution attempts = %#v", states)
	}
	var repairEvents int
	if err := application.store.DB().QueryRow(`SELECT COUNT(*) FROM workflow_events WHERE workflow_run_id=? AND event_type='workflow.ai_output_repair_queued'`, started.Run.ID).Scan(&repairEvents); err != nil || repairEvents != 0 {
		t.Fatalf("automatic repair events = %d, %v", repairEvents, err)
	}
}

func TestResearchStarterNormalizesNoClarificationWithoutAnotherModelAttempt(t *testing.T) {
	server := newResearchStarterSkillTestServer(t, func(plan map[string]any) {
		plan["clarification"] = map[string]any{"needsUserInput": false}
	})
	application, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	application.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, application, server.URL)
	projectValue, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "无数据先做设计"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.WorkflowFacade.StartResearch(workflow.StartResearchCommand{
		ProjectID: projectValue.ID, ResearchIdea: "我想研究夜间使用手机是否影响大学生睡眠，但目前还没有数据和文献。",
		ModelProfileID: profile.ID, ModelID: workflowAITestModelID,
	})
	if err != nil {
		t.Fatal(err)
	}
	completed := waitForWorkflowState(t, application, projectValue.ID, started.Run.ID, workflow.RunCompleted, 20*time.Second)
	if len(completed.AIExecutions) != 1 {
		t.Fatalf("unnecessary model retries: %d", len(completed.AIExecutions))
	}
	execution := completed.AIExecutions[0]
	var output struct {
		Clarification struct {
			Questions []json.RawMessage `json:"questions"`
		} `json:"clarification"`
	}
	if err := json.Unmarshal(execution.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.Clarification.Questions == nil || len(output.Clarification.Questions) != 0 {
		t.Fatalf("missing canonical empty questions: %s", execution.Output)
	}
	var original struct {
		Clarification map[string]json.RawMessage `json:"clarification"`
	}
	// The normalization changes only the accepted structured projection, not
	// the provider response retained for auditing and replay.
	_, text, found := strings.Cut(execution.OutputText, "```json\n")
	if !found {
		t.Fatal("fixture response lost its original JSON fence")
	}
	text, _, found = strings.Cut(text, "\n```")
	if !found {
		t.Fatal("fixture response lost its closing JSON fence")
	}
	if err := json.Unmarshal([]byte(text), &original); err != nil {
		t.Fatal(err)
	}
	if _, present := original.Clarification["questions"]; present {
		t.Fatal("raw provider response was rewritten")
	}
	for _, event := range completed.Events {
		if event.Type == "workflow.ai_output_repair_queued" {
			t.Fatal("empty clarification should not trigger a model repair")
		}
	}
}

func TestResearchStarterAdoptedDataRouteKeepsOneTaskIdentity(t *testing.T) {
	application, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	application.Startup(context.Background())
	server := newWorkflowAITestServer(t)
	profile := saveWorkflowAITestProfile(t, application, server.URL)
	projectValue, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "数据路线任务一致性"})
	if err != nil {
		t.Fatal(err)
	}
	starter, err := application.WorkflowFacade.StartResearch(workflow.StartResearchCommand{
		ProjectID: projectValue.ID, ResearchIdea: "我已有一份尚未上传的睡眠时长实验数据，需要比较组间差异并生成图表报告。", ModelProfileID: profile.ID, ModelID: workflowAITestModelID,
	})
	if err != nil {
		t.Fatal(err)
	}
	completed := waitForWorkflowState(t, application, projectValue.ID, starter.Run.ID, workflow.RunCompleted, 10*time.Second)
	plan, err := application.WorkflowFacade.GetResearchStarterPlan(projectValue.ID, completed.Run.ID)
	if err != nil || plan.Clarification != nil && plan.Clarification.NeedsUserInput {
		t.Fatalf("complete research intent must reach route selection without upload questions: %#v, %v", plan, err)
	}
	adopted, err := application.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{
		ProjectID: projectValue.ID, RunID: completed.Run.ID, RouteID: "tabular_analysis_when_ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	if adopted.AvailableNow || adopted.Run != nil || adopted.StarterRunID != completed.Run.ID || adopted.RouteID != "tabular_analysis_when_ready" {
		t.Fatalf("data route adoption = %#v", adopted)
	}
	for i := 0; i < 2; i++ {
		recovered, err := application.WorkflowFacade.GetPendingResearchAdoption(projectValue.ID, completed.Run.ID)
		if err != nil || recovered == nil || recovered.Workflow.Workflow.ID != adopted.Workflow.Workflow.ID || recovered.ResearchTaskID != adopted.ResearchTaskID || string(recovered.InitialInputs) != string(adopted.InitialInputs) {
			t.Fatalf("upload checkpoint restore changed route: %+v %v", recovered, err)
		}
	}
	page, err := application.WorkflowFacade.ResearchTimeline(workflow.ResearchTimelineQuery{ProjectID: projectValue.ID, TaskID: completed.Run.ResearchTaskID})
	if err != nil {
		t.Fatal(err)
	}
	adoptionEntries := 0
	for _, entry := range page.Entries {
		if entry.EventType == "research.route_adopted" {
			adoptionEntries++
			if !entry.Active {
				t.Fatal("upload checkpoint not active")
			}
		}
		if entry.EventType == "workflow.completed" && entry.Active {
			t.Fatal("adopted planning still active")
		}
	}
	if adoptionEntries != 1 {
		t.Fatal("restore duplicated route adoption", adoptionEntries)
	}
	if _, err := application.WorkflowFacade.StartAdoptedResearchRoute(workflow.StartAdoptedResearchRouteCommand{
		ProjectID: projectValue.ID, StarterRunID: adopted.StarterRunID, RouteID: adopted.RouteID,
		WorkflowID: adopted.Workflow.Workflow.ID, WorkflowVersionID: adopted.Workflow.Version.ID,
		Inputs: adopted.InitialInputs, ModelProfileID: profile.ID, ModelID: workflowAITestModelID,
	}); err == nil {
		t.Fatal("adoption without file selection must not start execution")
	}
	// Wails may receive null from a restored/empty form. Reject it without
	// panicking or changing the task, then allow a valid file selection below.
	if _, err := application.WorkflowFacade.StartAdoptedResearchRoute(workflow.StartAdoptedResearchRouteCommand{
		ProjectID: projectValue.ID, StarterRunID: adopted.StarterRunID, RouteID: adopted.RouteID,
		WorkflowID: adopted.Workflow.Workflow.ID, WorkflowVersionID: adopted.Workflow.Version.ID,
		Inputs: json.RawMessage(`null`), ModelProfileID: profile.ID, ModelID: workflowAITestModelID,
	}); err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("null route inputs must be recoverable: %v", err)
	}

	inputName := "sleep-study.csv"
	inputPath := filepath.Join(projectValue.WorkspacePath, inputName)
	if err := os.WriteFile(inputPath, []byte("duration,score\n7,4\n8,5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var inputs map[string]any
	if err := json.Unmarshal(adopted.InitialInputs, &inputs); err != nil {
		t.Fatal(err)
	}
	// A resumed browser may still have a stale editable form value. The
	// server-owned planner question and route context must be canonicalized
	// while the selected file remains the only user-provided route input.
	inputs["research_goal"] = "旧表单中的研究问题"
	inputs["route_context"] = map[string]any{"routeId": "stale-route-context"}
	secondName := "sleep-groups.csv"
	if err := os.WriteFile(filepath.Join(projectValue.WorkspacePath, secondName), []byte("duration,group\n7,a\n8,b\n"), 0600); err != nil {
		t.Fatal(err)
	}
	inputs["input_paths"] = []string{inputName, secondName}
	boundInputs, err := json.Marshal(inputs)
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.WorkflowFacade.StartAdoptedResearchRoute(workflow.StartAdoptedResearchRouteCommand{
		ProjectID: projectValue.ID, StarterRunID: adopted.StarterRunID, RouteID: adopted.RouteID,
		WorkflowID: adopted.Workflow.Workflow.ID, WorkflowVersionID: adopted.Workflow.Version.ID,
		Inputs: boundInputs, ModelProfileID: profile.ID, ModelID: workflowAITestModelID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if started.Run.ResearchTaskID != completed.Run.ID || started.Run.ResearchStarterID != completed.Run.ID {
		t.Fatalf("adopted data route lost task identity: %#v", started.Run)
	}
	if pending, err := application.WorkflowFacade.GetPendingResearchAdoption(projectValue.ID, completed.Run.ID); err != nil || pending != nil {
		t.Fatal("execution revived upload checkpoint", err)
	}
	retried, err := application.WorkflowFacade.StartAdoptedResearchRoute(workflow.StartAdoptedResearchRouteCommand{
		ProjectID: projectValue.ID, StarterRunID: adopted.StarterRunID, RouteID: adopted.RouteID,
		WorkflowID: adopted.Workflow.Workflow.ID, WorkflowVersionID: adopted.Workflow.Version.ID,
		Inputs: boundInputs, ModelProfileID: profile.ID, ModelID: workflowAITestModelID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if retried.Run.ID != started.Run.ID {
		t.Fatalf("repeated adopted route launch created another Run: first=%s retry=%s", started.Run.ID, retried.Run.ID)
	}
	updatedStarter, err := application.WorkflowFacade.GetRun(projectValue.ID, completed.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var formalRunID string
	for _, event := range updatedStarter.Events {
		if event.Type != "research.route_adopted" {
			continue
		}
		var payload struct {
			RouteID     string `json:"routeId"`
			FormalRunID string `json:"formalRunId"`
		}
		if json.Unmarshal(event.Payload, &payload) == nil && payload.RouteID == adopted.RouteID {
			formalRunID = payload.FormalRunID
		}
	}
	if formalRunID != started.Run.ID {
		t.Fatalf("data route adoption did not record formal Run: %q", formalRunID)
	}
	tasks, err := application.WorkflowFacade.ListProjectRuns(projectValue.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != started.Run.ID {
		t.Fatalf("planning and data execution were exposed as separate tasks: %#v", tasks)
	}
	// Do not close the application while the background Workflow driver still
	// owns SQLite handles. The fixture may complete or fail depending on the
	// host Python installation; either terminal outcome is sufficient here.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		current, getErr := application.WorkflowFacade.GetRun(projectValue.ID, started.Run.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if current.Run.Status.Terminal() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDynamicEmpiricalResearchRouteCompletesAndRegistersReviewedReport(t *testing.T) {
	var reviewMu sync.Mutex
	reviewCount := 0
	server := newResearchStarterOutputTestServer(t, func(output map[string]any, _ string) {
		checks, ok := output["acceptanceChecks"].([]any)
		if !ok {
			return
		}
		reviewMu.Lock()
		defer reviewMu.Unlock()
		reviewCount++
		if reviewCount == 1 {
			output["approved"] = false
			output["requiredCorrections"] = []string{"修订分析实现的方法说明后重新计算并核对结果"}
			if len(checks) > 0 {
				checks[0].(map[string]any)["status"] = "not_met"
				checks[0].(map[string]any)["basis"] = "计算方法说明尚不完整"
			}
		}
	}, mutateStarterToEmpiricalReportRoute)
	application, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	application.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, application, server.URL)
	projectValue, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "动态实证闭环"})
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := application.PythonFacade.DetectInterpreters()
	if err != nil || len(discovery.Interpreters) == 0 {
		t.Skipf("no Python interpreter available for empirical Workflow integration test: %v", err)
	}
	testEnvironment := filepath.Join(t.TempDir(), "workflow-python")
	if err := pythonruntime.New(application.pythonexec).CreateEnvironment(context.Background(), discovery.Interpreters[0].ExecutablePath, testEnvironment); err != nil {
		t.Skipf("cannot create isolated Python fixture environment: %v", err)
	}
	fixturePython := filepath.Join(testEnvironment, "Scripts", "python.exe")
	if _, err := application.PythonFacade.BindProjectEnvironment(projectValue.ID, fixturePython); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectValue.WorkspacePath, "sleep.csv"), []byte("exercise_minutes,sleep_hours\n30,7.5\n45,8.0\n20,6.5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	starter, err := application.WorkflowFacade.StartResearch(workflow.StartResearchCommand{
		ProjectID: projectValue.ID, ResearchIdea: "运动时长与睡眠时长是否相关", ModelProfileID: profile.ID, ModelID: workflowAITestModelID,
	})
	if err != nil {
		t.Fatal(err)
	}
	planned := waitForWorkflowState(t, application, projectValue.ID, starter.Run.ID, workflow.RunCompleted, 15*time.Second)
	adopted, err := application.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: projectValue.ID, RunID: planned.Run.ID, RouteID: "empirical_report"})
	if err != nil {
		t.Fatalf("adopt empirical route = %#v, %v", adopted, err)
	}
	var routeInputs map[string]any
	if json.Unmarshal(adopted.InitialInputs, &routeInputs) != nil {
		t.Fatalf("decode empirical route inputs: %s", adopted.InitialInputs)
	}
	routeInputs["input_paths"] = []string{"sleep.csv"}
	boundInputs, _ := json.Marshal(routeInputs)
	started, err := application.WorkflowFacade.StartAdoptedResearchRoute(workflow.StartAdoptedResearchRouteCommand{
		ProjectID: projectValue.ID, StarterRunID: adopted.StarterRunID, RouteID: adopted.RouteID,
		WorkflowID: adopted.Workflow.Workflow.ID, WorkflowVersionID: adopted.Workflow.Version.ID, Inputs: boundInputs,
		ModelProfileID: profile.ID, ModelID: workflowAITestModelID,
	})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(60 * time.Second)
	var completed workflow.RunDetail
	revised := false
	var priorPythonOutput json.RawMessage
	for time.Now().Before(deadline) {
		current, getErr := application.WorkflowFacade.GetRun(projectValue.ID, started.Run.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if current.Run.Status == workflow.RunWaitingHumanConfirmation {
			for _, step := range current.Steps {
				if step.Status != workflow.StepWaitingHumanConfirmation {
					continue
				}
				if _, err := application.WorkflowFacade.Decide(workflow.HumanDecisionCommand{ProjectID: projectValue.ID, RunID: current.Run.ID, StepID: step.ID, Approved: true, Context: json.RawMessage(`{}`)}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if current.Run.Status == workflow.RunCompleted {
			completed = current
			break
		}

		if current.Run.Status == workflow.RunFailed && !revised && current.DeliveryAssessment != nil && current.DeliveryAssessment.Status == "revision_required" {
			var gateID string
			for _, step := range current.Steps {
				if step.NodeID == "delivery_gate" {
					gateID = step.ID
				}
				if step.NodeID == "python_analysis" {
					priorPythonOutput = append(json.RawMessage(nil), step.Output...)
				}
			}
			targetFound := false
			for _, target := range current.ReviewRevisionTargets {
				if target.NodeID == "method_implementation" && target.RepeatsSideEffects {
					targetFound = true
				}
			}
			if !targetFound {
				t.Fatal("method revision did not disclose repeated Python execution")
			}
			retry := workflow.RetryCommand{ProjectID: projectValue.ID, RunID: current.Run.ID, StepID: gateID, RevisionNodeID: "method_implementation"}
			if _, err := application.WorkflowFacade.Retry(retry); err == nil {
				t.Fatal("method revision repeated side effects without confirmation")
			}
			retry.ConfirmSideEffect = true
			if _, err := application.WorkflowFacade.Retry(retry); err != nil {
				t.Fatal(err)
			}
			revised = true
			continue
		}
		if current.Run.Status == workflow.RunFailed || current.Run.Status == workflow.RunCancelled || current.Run.Status == workflow.RunInterrupted {
			t.Fatalf("empirical route ended as %s: %s %s; steps=%#v", current.Run.Status, current.Run.ErrorCode, current.Run.ErrorMessage, current.Steps)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if completed.Run.ID == "" {
		t.Fatal("empirical route did not complete before timeout")
	}

	if !revised || len(priorPythonOutput) == 0 {
		t.Fatal("empirical revision path was not exercised")
	}
	if completed.DeliveryAssessment == nil || completed.DeliveryAssessment.Status != "reviewed" || completed.DeliveryAssessment.Kind != "analysis_result" {
		t.Fatalf("empirical delivery assessment=%+v", completed.DeliveryAssessment)
	}
	for _, step := range completed.Steps {
		if step.NodeID == "python_analysis" && step.Attempt != 2 {
			t.Fatalf("Python attempt=%d", step.Attempt)
		}
		if step.NodeID == "question_refinement" && step.Attempt != 1 {
			t.Fatal("empirical revision reran the frozen research agreement")
		}
	}
	var outputs map[string]json.RawMessage
	if json.Unmarshal(completed.Run.Outputs, &outputs) != nil || len(outputs["analysis"]) == 0 || len(outputs["interpretation"]) == 0 || len(outputs["report_draft"]) == 0 || len(outputs["delivery_gate"]) == 0 {
		t.Fatalf("empirical route outputs are incomplete: %s", completed.Run.Outputs)
	}
	if _, err := application.WorkflowFacade.SaveDeliverable(wailstransport.SaveWorkflowDeliverableRequest{ProjectID: projectValue.ID, RunID: completed.Run.ID, OutputName: "report_draft"}); err != nil {
		t.Fatal(err)
	}
	refreshed, err := application.WorkflowFacade.GetRun(projectValue.ID, completed.Run.ID)
	if err != nil || refreshed.ArtifactCount == 0 {
		t.Fatalf("reviewed report was not registered as a research artifact: count=%d, err=%v", refreshed.ArtifactCount, err)
	}
}

func mutateStarterToEmpiricalReportRoute(plan map[string]any) {
	stage := func(stageID, objective string, skill bool) map[string]any {
		skills := []any{}
		if skill {
			skills = []any{"scientific-writing"}
		}
		return map[string]any{"stageId": stageID, "objective": objective, "methods": []any{}, "skillNames": skills}
	}
	plan["routes"] = []any{map[string]any{
		"routeId": "empirical_report", "title": "动态实证研究报告", "reason": "对当前表格执行设计约束下的可复现分析并形成报告", "availableNow": true,
		"requiredResources": []any{"课题相关 CSV"}, "deliverables": []any{"实证研究报告"}, "blockers": []any{}, "stagePlans": []any{stage("question_refinement", "冻结问题", false), stage("method_selection", "形成方法蓝图", true), stage("research_design", "形成研究设计", true), stage("data_preflight", "预检数据", false), stage("method_implementation", "生成分析实现", false), stage("dependency_preparation", "准备依赖", false), stage("python_analysis", "执行计算", false), stage("result_interpretation", "解释结果", false), stage("report_drafting", "形成报告", false), stage("independent_review", "独立审查", true), stage("delivery_gate", "核验交付", false)},
	}}
	plan["recommendedRouteId"] = "empirical_report"
	plan["recommendationReason"] = "当前已有课题相关表格，可完成实证闭环。"
	plan["availableResources"] = []any{"sleep.csv"}
	plan["missingInformation"] = []any{}
}

func TestResearchStarterFreezesAndReloadsRouteSkillsInFormalStages(t *testing.T) {
	application, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	application.Startup(context.Background())
	server := newResearchStarterSkillTestServer(t)
	profile := saveWorkflowAITestProfile(t, application, server.URL)
	projectValue, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "动态 Skill 科研路线"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.WorkflowFacade.StartResearch(workflow.StartResearchCommand{
		ProjectID: projectValue.ID, ResearchIdea: "设计并审查一项大学生睡眠研究", ModelProfileID: profile.ID, ModelID: workflowAITestModelID,
	})
	if err != nil {
		t.Fatal(err)
	}
	planner := waitForWorkflowState(t, application, projectValue.ID, started.Run.ID, workflow.RunCompleted, 10*time.Second)
	if len(planner.AIExecutions) != 1 || planner.AIExecutions[0].ChatRunID == "" {
		t.Fatalf("planner AI execution = %#v", planner.AIExecutions)
	}
	var plannerContentHash, plannerPackageHash string
	if err := application.store.DB().QueryRow(`SELECT content_hash,package_hash FROM run_dynamic_skills WHERE run_id=? AND skill_name='scientific-writing'`, planner.AIExecutions[0].ChatRunID).Scan(&plannerContentHash, &plannerPackageHash); err != nil {
		t.Fatalf("planner Skill snapshot: %v", err)
	}
	if len(plannerContentHash) != 64 || len(plannerPackageHash) != 64 {
		t.Fatalf("planner Skill hashes = %q %q", plannerContentHash, plannerPackageHash)
	}
	adopted, err := application.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: projectValue.ID, RunID: planner.Run.ID, RouteID: "design_first"})
	if err != nil || adopted.Run == nil {
		t.Fatalf("adopt Skill route = %#v, %v", adopted, err)
	}
	var initial struct {
		RouteContext struct {
			SelectedSkills []struct {
				Name        string `json:"name"`
				ContentHash string `json:"contentHash"`
				PackageHash string `json:"packageHash"`
			} `json:"selectedSkills"`
		} `json:"route_context"`
	}
	if json.Unmarshal(adopted.InitialInputs, &initial) != nil || len(initial.RouteContext.SelectedSkills) != 1 || initial.RouteContext.SelectedSkills[0].ContentHash != plannerContentHash || initial.RouteContext.SelectedSkills[0].PackageHash != plannerPackageHash {
		t.Fatalf("formal route did not freeze planner Skill hashes: %s", adopted.InitialInputs)
	}
	formal := waitForWorkflowState(t, application, projectValue.ID, adopted.Run.Run.ID, workflow.RunCompleted, 10*time.Second)
	steps := map[string]workflow.Step{}
	for _, step := range formal.Steps {
		steps[step.ID] = step
	}
	loadedStages := map[string]bool{}
	for _, execution := range formal.AIExecutions {
		step := steps[execution.WorkflowStepID]
		if step.NodeID != "method_selection" && step.NodeID != "research_design" && step.NodeID != "independent_review" {
			continue
		}
		var contentHash, packageHash string
		err := application.store.DB().QueryRow(`SELECT content_hash,package_hash FROM run_dynamic_skills WHERE run_id=? AND skill_name='scientific-writing'`, execution.ChatRunID).Scan(&contentHash, &packageHash)
		if err != nil || contentHash != plannerContentHash || packageHash != plannerPackageHash {
			t.Fatalf("stage %s Skill snapshot = %q %q, %v", step.NodeID, contentHash, packageHash, err)
		}
		loadedStages[step.NodeID] = true
	}
	for _, stageID := range []string{"method_selection", "research_design", "independent_review"} {
		if !loadedStages[stageID] {
			t.Fatalf("formal route did not reload scientific-writing in %s: %#v", stageID, formal.AIExecutions)
		}
	}
	if !strings.Contains(string(formal.Run.Inputs), "scientific-writing") {
		t.Fatalf("formal Run lost frozen Skill route: %s", formal.Run.Inputs)
	}
}

func TestResearchStarterPersistsHiddenSystemRunAndAdoptsOnlyFrozenRoutes(t *testing.T) {
	application, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	application.Startup(context.Background())
	server := newWorkflowAITestServer(t)
	profile := saveWorkflowAITestProfile(t, application, server.URL)
	projectValue, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "一句话研究启动"})
	if err != nil {
		t.Fatal(err)
	}
	idea := "我想研究夜间使用手机是否影响大学生睡眠，但目前没有数据和文献。"
	started, err := application.WorkflowFacade.StartResearch(workflow.StartResearchCommand{
		ProjectID: projectValue.ID, ResearchIdea: idea, ModelProfileID: profile.ID, ModelID: workflowAITestModelID,
	})
	if err != nil {
		t.Fatal(err)
	}
	completed := waitForWorkflowState(t, application, projectValue.ID, started.Run.ID, workflow.RunCompleted, 10*time.Second)
	if completed.Run.WorkflowPurpose != workflow.PurposeResearchStarter || completed.Run.WorkflowName != "AI 研究启动" || completed.Run.ConversationID == "" {
		t.Fatalf("research starter Run snapshot = %#v", completed.Run)
	}
	if len(completed.AIExecutions) != 1 || completed.AIExecutions[0].ChatRunID == "" {
		t.Fatalf("research starter AI execution binding = %#v", completed.AIExecutions)
	}
	scopeRepository := sqlite.NewWorkflowRuntimeRepository(application.store.DB())
	chatRunID := completed.AIExecutions[0].ChatRunID
	taskID, err := scopeRepository.ResearchTaskIDForSubject(context.Background(), tool.SubjectChatRun, chatRunID)
	if err != nil || taskID != completed.Run.ResearchTaskID {
		t.Fatalf("Workflow AI Chat Run task scope = %q, %v; want %q", taskID, err, completed.Run.ResearchTaskID)
	}
	workspaceRoot, err := scopeRepository.WorkspaceRootForSubject(context.Background(), tool.SubjectChatRun, chatRunID)
	wantWorkspaceRoot, pathErr := project.ResearchTaskWorkspacePath(projectValue, completed.Run.ResearchTaskID)
	if err != nil || pathErr != nil || filepath.Clean(workspaceRoot) != filepath.Clean(wantWorkspaceRoot) {
		t.Fatalf("Workflow AI Chat Run workspace = %q, %v; want %q, %v", workspaceRoot, err, wantWorkspaceRoot, pathErr)
	}
	messages, err := application.ConversationFacade.ListMessages(completed.Run.ConversationID)
	if err != nil || len(messages) != 2 || !messages[0].Internal || !messages[1].Internal {
		t.Fatalf("research starter internal messages = %#v, %v", messages, err)
	}
	plans, err := application.WorkflowFacade.List(projectValue.ID)
	if err != nil || len(plans) != 0 {
		t.Fatalf("system starter leaked into saved plans: %#v, %v", plans, err)
	}
	runs, err := application.WorkflowFacade.ListProjectRuns(projectValue.ID, 20)
	if err != nil || len(runs) != 1 || runs[0].ID != completed.Run.ID || runs[0].WorkflowPurpose != workflow.PurposeResearchStarter {
		t.Fatalf("research starter is not recoverable from project tasks: %#v, %v", runs, err)
	}
	if _, err := application.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: projectValue.ID, RunID: completed.Run.ID, RouteID: "invented_workflow"}); err == nil {
		t.Fatal("client-invented route was accepted")
	}
	data, err := application.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: projectValue.ID, RunID: completed.Run.ID, RouteID: "tabular_analysis_when_ready"})
	if err != nil {
		t.Fatal(err)
	}
	if data.TemplateID != "dynamic-research-tabular_analysis_when_ready" || data.AvailableNow {
		t.Fatalf("data route without data = %#v", data)
	}
	var dataInputs map[string]json.RawMessage
	var inputPaths []string
	if json.Unmarshal(data.InitialInputs, &dataInputs) != nil || json.Unmarshal(dataInputs["input_paths"], &inputPaths) != nil || len(inputPaths) != 0 || len(dataInputs["route_context"]) == 0 {
		t.Fatalf("data route initial inputs = %s", data.InitialInputs)
	}
	design, err := application.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: projectValue.ID, RunID: completed.Run.ID, RouteID: "design_first"})
	if err != nil {
		t.Fatal(err)
	}
	if design.TemplateID != "dynamic-research-design_first" || !design.AvailableNow || design.ResearchIdea != idea {
		t.Fatalf("research design route = %#v", design)
	}
	if design.Run == nil || design.Run.Run.WorkflowID != design.Workflow.Workflow.ID || design.Run.Run.WorkflowPurpose != workflow.PurposeUserPlan || design.Run.Run.ConversationID == "" {
		t.Fatalf("adopted research design did not start a formal Run: %#v", design.Run)
	}
	retriedDesign, err := application.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: projectValue.ID, RunID: completed.Run.ID, RouteID: "design_first"})
	if err != nil {
		t.Fatal(err)
	}
	if retriedDesign.Run == nil || retriedDesign.Run.Run.ID != design.Run.Run.ID || retriedDesign.Workflow.Workflow.ID != design.Workflow.Workflow.ID {
		t.Fatalf("route adoption retry created another formal task: first=%#v retry=%#v", design.Run, retriedDesign.Run)
	}
	if _, err := application.WorkflowFacade.ReplanResearchStarter(workflow.ReplanResearchStarterCommand{ProjectID: projectValue.ID, StarterRunID: completed.Run.ID}); err == nil {
		t.Fatal("adopted historical starter created a hidden replanning branch")
	}
	if _, err := application.WorkflowFacade.StartAdoptedResearchRoute(workflow.StartAdoptedResearchRouteCommand{
		ProjectID: projectValue.ID, StarterRunID: completed.Run.ID, RouteID: data.RouteID,
		WorkflowID: data.Workflow.Workflow.ID, WorkflowVersionID: data.Workflow.Version.ID, Inputs: json.RawMessage(`{"input_paths":["not-selected.csv"]}`),
		ModelProfileID: profile.ID, ModelID: workflowAITestModelID,
	}); err == nil || !strings.Contains(err.Error(), "后续任务记录") {
		t.Fatalf("pending alternate route bypassed current-task guard: %v", err)
	}
	formal := waitForWorkflowState(t, application, projectValue.ID, design.Run.Run.ID, workflow.RunCompleted, 10*time.Second)
	if formal.Run.ResearchTaskID != completed.Run.ID || formal.Run.ResearchStarterID != completed.Run.ID {
		t.Fatalf("adopted route did not retain unified research task identity: %#v", formal.Run)
	}
	projectTasks, err := application.WorkflowFacade.ListProjectRuns(projectValue.ID, 20)
	if err != nil || len(projectTasks) != 1 || projectTasks[0].ID != formal.Run.ID {
		t.Fatalf("planning and execution were exposed as separate tasks: %#v, %v", projectTasks, err)
	}
	if len(formal.Steps) != 5 || formal.Steps[4].Status != workflow.StepCompleted || len(formal.Run.Outputs) == 0 || string(formal.Run.Outputs) == "{}" || formal.ArtifactCount != 0 {
		t.Fatalf("formal research design did not complete end-to-end: %#v", formal)
	}
	deliverable, err := application.WorkflowFacade.SaveDeliverable(wailstransport.SaveWorkflowDeliverableRequest{
		ProjectID: projectValue.ID, RunID: formal.Run.ID, OutputName: "research_design",
	})
	if err != nil || !deliverable.Created || deliverable.Version.Provenance.WorkflowRunID != formal.Run.ID || deliverable.Version.Provenance.Extra["workflowDeliverable"] != "research_design" {
		t.Fatalf("formal research design deliverable = %#v, %v", deliverable, err)
	}
	if len(deliverable.Version.Lineage) != 1 || deliverable.Version.Lineage[0].SourceWorkflowRunID != formal.Run.ID {
		t.Fatalf("formal research design lineage = %#v", deliverable.Version.Lineage)
	}
	replayed, err := application.WorkflowFacade.SaveDeliverable(wailstransport.SaveWorkflowDeliverableRequest{
		ProjectID: projectValue.ID, RunID: formal.Run.ID, OutputName: "research_design",
	})
	if err != nil || replayed.Created || replayed.Version.ID != deliverable.Version.ID {
		t.Fatalf("formal research design deliverable replay = %#v, %v", replayed, err)
	}
	registered, err := application.WorkflowFacade.GetRun(projectValue.ID, formal.Run.ID)
	if err != nil || registered.ArtifactCount != 1 {
		t.Fatalf("formal research design Artifact count = %d, %v", registered.ArtifactCount, err)
	}
	formalMessages, err := application.ConversationFacade.ListMessages(formal.Run.ConversationID)
	if err != nil || len(formalMessages) != 8 {
		t.Fatalf("formal research conversation = %#v, %v", formalMessages, err)
	}
	for _, message := range formalMessages {
		if !message.Internal {
			t.Fatalf("formal research message leaked as a user conversation message: %#v", message)
		}
	}
	var designInputs struct {
		ResearchGoal string `json:"research_goal"`
	}
	if json.Unmarshal(design.InitialInputs, &designInputs) != nil || designInputs.ResearchGoal != idea {
		t.Fatalf("research idea was not preserved: %s", design.InitialInputs)
	}
	visible, err := application.WorkflowFacade.List(projectValue.ID)
	if err != nil || len(visible) != 2 {
		t.Fatalf("adopted saved plans = %#v, %v", visible, err)
	}
	for _, value := range visible {
		if value.Purpose != workflow.PurposeUserPlan || value.Name == "AI 研究启动" {
			t.Fatalf("invalid visible Workflow purpose: %#v", value)
		}
	}
	var adoptedEvents int
	for _, event := range completed.Events {
		if event.Type == "research.route_adopted" {
			adoptedEvents++
		}
	}
	updated, err := application.WorkflowFacade.GetRun(projectValue.ID, completed.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range updated.Events {
		if event.Type == "research.route_adopted" {
			adoptedEvents++
		}
	}
	if adoptedEvents != 2 {
		t.Fatalf("route adoption audit events = %d, want 2", adoptedEvents)
	}
	if err := application.WorkflowFacade.DeleteRun(projectValue.ID, formal.Run.ID); err != nil {
		t.Fatal(err)
	}
	projectTasks, err = application.WorkflowFacade.ListProjectRuns(projectValue.ID, 20)
	if err != nil || len(projectTasks) != 0 {
		t.Fatalf("deleting a unified task exposed its planning record again: %#v, %v", projectTasks, err)
	}
	if _, err := application.WorkflowFacade.GetRun(projectValue.ID, completed.Run.ID); err == nil {
		t.Fatal("unified task deletion retained the research starter audit Run")
	}
}

func TestResearchStarterAdoptsValidAlternativeAfterInvalidDuplicate(t *testing.T) {
	server := newResearchStarterSkillTestServer(t, mutateInvalidDuplicateStarterRoute)
	application, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	application.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, application, server.URL)
	projectValue, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "损坏路线备选采纳"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.WorkflowFacade.StartResearch(workflow.StartResearchCommand{
		ProjectID: projectValue.ID, ResearchIdea: "验证损坏推荐路线不会阻塞合法备选路线", ModelProfileID: profile.ID, ModelID: workflowAITestModelID,
	})
	if err != nil {
		t.Fatal(err)
	}
	completed := waitForWorkflowState(t, application, projectValue.ID, started.Run.ID, workflow.RunCompleted, 10*time.Second)
	plan, err := application.WorkflowFacade.GetResearchStarterPlan(projectValue.ID, completed.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Routes) != 2 || plan.Routes[0].Validation != "invalid" || plan.Routes[1].Validation != "ready" {
		t.Fatalf("starter route projection = %#v", plan.Routes)
	}
	if plan.RecommendedRouteID != "valid_alternative" {
		t.Fatalf("recommended alternative route = %q", plan.RecommendedRouteID)
	}
	adopted, err := application.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{
		ProjectID: projectValue.ID, RunID: completed.Run.ID, RouteID: "valid_alternative",
	})
	if err != nil {
		t.Fatal(err)
	}
	if adopted.Run == nil || adopted.TemplateID != "dynamic-research-valid_alternative" {
		t.Fatalf("valid alternative was not adopted and started: %#v", adopted)
	}
	formal := waitForWorkflowState(t, application, projectValue.ID, adopted.Run.Run.ID, workflow.RunCompleted, 10*time.Second)
	if formal.Run.Status != workflow.RunCompleted {
		t.Fatalf("adopted alternative did not complete: %#v", formal.Run)
	}
}

func mutateInvalidDuplicateStarterRoute(plan map[string]any) {
	routes, ok := plan["routes"].([]any)
	if !ok || len(routes) < 2 {
		return
	}
	broken, brokenOK := routes[0].(map[string]any)
	alternative, alternativeOK := routes[1].(map[string]any)
	if !brokenOK || !alternativeOK {
		return
	}
	// Keep a valid alternative while making the first candidate structurally
	// incomplete. Unloaded Skill references are now rejected before planning
	// completes, so they can no longer serve as a completed-plan fixture.
	alternative["stagePlans"] = cloneJSONValue(broken["stagePlans"])
	alternative["routeId"] = "valid_alternative"
	alternative["title"] = "合法备选路线"
	alternative["reason"] = "验证结构校验后仍可采纳"
	alternative["availableNow"] = true
	alternative["blockers"] = []any{}
	broken["routeId"] = "broken_duplicate"
	broken["title"] = "损坏推荐路线"
	broken["stagePlans"] = []any{map[string]any{"stageId": "question_refinement", "objective": "仅明确问题，没有实质交付阶段", "skillNames": []any{"scientific-writing"}}}
	plan["recommendedRouteId"] = "valid_alternative"
}

func cloneJSONValue(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var cloned any
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return value
	}
	return cloned
}

func TestResearchRouteAdoptionRejectsOrdinaryAndIncompleteRuns(t *testing.T) {
	application, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	application.Startup(context.Background())
	server := newWorkflowAITestServer(t)
	profile := saveWorkflowAITestProfile(t, application, server.URL)
	projectValue, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "研究启动验证"})
	if err != nil {
		t.Fatal(err)
	}
	starter, err := application.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: projectValue.ID, ResearchIdea: "验证未完成状态", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: projectValue.ID, RunID: starter.Run.ID, RouteID: "design_first"}); err == nil {
		t.Fatal("incomplete research starter route was accepted")
	}
	completed := waitForWorkflowState(t, application, projectValue.ID, starter.Run.ID, workflow.RunCompleted, 10*time.Second)
	design, err := application.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: projectValue.ID, RunID: completed.Run.ID, RouteID: "design_first"})
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := application.WorkflowFacade.Start(workflow.StartCommand{
		ProjectID: projectValue.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: design.Workflow.Workflow.ID, WorkflowVersionID: design.Workflow.Version.ID,
		Inputs: design.InitialInputs, ModelProfileID: profile.ID, ModelID: workflowAITestModelID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if design.Run == nil {
		t.Fatal("adopted route did not return its automatically started formal Run")
	}
	if _, err := application.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: projectValue.ID, RunID: ordinary.Run.ID, RouteID: "design_first"}); err == nil {
		t.Fatal("ordinary Workflow Run was accepted as a research starter")
	}
	for _, runID := range []string{ordinary.Run.ID, design.Run.Run.ID} {
		if _, err := application.WorkflowFacade.Cancel(projectValue.ID, runID); err != nil {
			t.Fatal(err)
		}
		waitForWorkflowState(t, application, projectValue.ID, runID, workflow.RunCancelled, 10*time.Second)
	}
}

func TestResearchStarterNormalizesLimitationsInOneSubmission(t *testing.T) {
	var mu sync.Mutex
	submissions := 0
	const original = "需要原始数据；不拆分这一段限制说明"
	server := newResearchStarterSkillTestServer(t, func(plan map[string]any) {
		mu.Lock()
		submissions++
		mu.Unlock()
		selected, _ := plan["selectedSkills"].([]any)
		for _, value := range selected {
			if item, ok := value.(map[string]any); ok {
				item["limitations"] = original
			}
		}
	})
	application, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	application.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, application, server.URL)
	projectValue, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "兼容限制说明列表"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: projectValue.ID, ResearchIdea: "研究手机与睡眠的关系", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	completed := waitForWorkflowState(t, application, projectValue.ID, started.Run.ID, workflow.RunCompleted, 20*time.Second)
	mu.Lock()
	count := submissions
	mu.Unlock()
	if count != 1 || len(completed.AIExecutions) != 1 {
		t.Fatalf("unnecessary model/stage repeat: submissions=%d executions=%d", count, len(completed.AIExecutions))
	}
	var value struct {
		SelectedSkills []struct {
			Limitations []string `json:"limitations"`
		} `json:"selectedSkills"`
	}
	if err := json.Unmarshal(completed.AIExecutions[0].Output, &value); err != nil {
		t.Fatal(err)
	}
	if len(value.SelectedSkills) == 0 {
		t.Fatal("missing skills")
	}
	for _, skill := range value.SelectedSkills {
		if len(skill.Limitations) != 1 || skill.Limitations[0] != original {
			t.Fatalf("changed meaning: %#v", skill.Limitations)
		}
	}
	var normalizedEvents int
	if err := application.store.DB().QueryRow(`SELECT COUNT(*) FROM workflow_events WHERE workflow_run_id=? AND event_type='workflow.ai_output_normalized'`, started.Run.ID).Scan(&normalizedEvents); err != nil || normalizedEvents != 1 {
		t.Fatalf("normalization audit count=%d err=%v", normalizedEvents, err)
	}
}

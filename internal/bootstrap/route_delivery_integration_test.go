package bootstrap

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Controlled HTTP responses exercise the production planner, SQLite, stage
// binding, submission and gate. This is not a scientific/model quality eval.
func TestRouteDeliveryPreservesMaterialsThroughRealRuntime(t *testing.T) {
	goal := "为大学生睡眠与学习状态生成方案、问卷初稿及分析计划；没有数据。"
	body := "## 自编问卷初稿（未验证）\n过去一周，您在课堂感到困倦的频率？\n从不（0）/偶尔（1）/经常（2）。\n变量 classroom_sleepiness；不作临床诊断。"
	server := newResearchStarterOutputTestServer(t, func(output map[string]any, prompt string) {
		input := workflowAITestStageInput(prompt)
		if _, planner := output["normalizedQuestion"]; planner {
			return
		}
		if input["originalRequest"] != goal {
			t.Errorf("lost original request: %v", input["originalRequest"])
		}
		if _, question := output["successCriteria"]; question {
			output["successCriteria"] = []any{"交付研究方案、具体问卷初稿与分析计划，标注未验证且不虚构数据"}
		}
		if _, design := output["status"]; design {
			output["deliverables"] = []any{map[string]any{"title": "自编问卷初稿", "markdown": body}}
		}
		if _, report := output["markdown"]; report {
			upstream, _ := input["context"].(map[string]any)
			encoded, _ := json.Marshal(upstream["deliverables"])
			if !strings.Contains(string(encoded), "classroom_sleepiness") {
				t.Error("report did not receive questionnaire")
			}
			output["markdown"] = "# 研究设计\n\n## 分析计划\n按有序变量分析关联，不推断因果。"
			output["claimSummary"] = []any{"无数据的设计草稿"}
			output["methodSummary"] = "横断面关联分析设计"
			output["limitations"] = []any{"自编题项未验证，尚未采集数据"}
		}
		if _, review := output["reviewedInputSha256"]; review {
			report, _ := input["context"].(map[string]any)
			// New routes separate host-preserved materials from authored
			// prose; the review must see the exact complete material once.
			materials, _ := input["reportRecordAppendices"].([]any)
			matches := 0
			for _, value := range materials {
				if text, ok := value.(string); ok && strings.Contains(text, body) {
					matches++
				}
			}
			if matches != 1 || strings.Contains(report["markdown"].(string), body) {
				t.Error("review lost exact material or mixed it into authored narrative", matches)
			}
			design, _ := input["designContext"].(map[string]any)
			if design["deliverables"] == nil {
				t.Error("review lost source design materials")
			}
			route, _ := input["routeContext"].(map[string]any)
			if route["deliverables"] == nil {
				t.Error("review lost adopted deliverables")
			}
			if strings.Contains(prompt, "Frozen upstream evidenceScreening") {
				t.Error("design review received evidence workflow instructions")
			}
		}
		delete(output, "reviewedInputSha256") // no model hash transcription for design delivery

	}, func(plan map[string]any) {
		route := plan["routes"].([]any)[0].(map[string]any)
		route["deliverables"] = []any{"方案", "问卷初稿", "分析计划"}
		stages := route["stagePlans"].([]any)
		route["stagePlans"] = append(append([]any{}, stages[:3]...), append([]any{map[string]any{"stageId": "report_drafting", "objective": "组织完整正文", "methods": []any{}, "skillNames": []any{}}}, stages[3:]...)...)
	})
	application, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	application.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, application, server.URL)
	project, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "交付衔接夹具"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: goal, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	plan := waitForWorkflowState(t, application, project.ID, started.Run.ID, workflow.RunCompleted, 20*time.Second)
	adopted, err := application.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: project.ID, RunID: plan.Run.ID, RouteID: "design_first"})
	if err != nil || adopted.Run == nil {
		t.Fatalf("adopt: %v", err)
	}
	formal := waitForWorkflowState(t, application, project.ID, adopted.Run.Run.ID, workflow.RunCompleted, 30*time.Second)
	if err := workflow.ValidateReviewedOutput(formal, "report_draft"); err != nil {
		t.Fatal(err)
	}
	var outputs map[string]json.RawMessage
	_ = json.Unmarshal(formal.Run.Outputs, &outputs)
	if !strings.Contains(string(outputs["research_design"]), "classroom_sleepiness") || !strings.Contains(string(outputs["report_draft"]), "classroom_sleepiness") {
		t.Fatal("persisted output lost actual material")
	}
	for _, execution := range formal.AIExecutions {
		if execution.Attempt != 1 || execution.Status != "completed" {
			t.Fatalf("unexpected retry: %+v", execution)
		}
	}
}

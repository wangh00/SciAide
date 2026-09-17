package bootstrap

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Exercise the real facade, durable task scope, local model and immutable
// replanning runs together; no external model or Python runtime is used.
func TestResearchClarificationAndReplanPreserveTaskResources(t *testing.T) {
	server := newResearchStarterSkillTestServer(t, func(plan map[string]any) {
		plan["clarification"] = map[string]any{
			"needsUserInput": true,
			"intro":          "确认报告需要的内容。",
			"questions": []any{map[string]any{
				"id": "outputs", "kind": "research_direction", "impact": "决定本轮交付范围", "text": "报告需要包含哪些内容？", "required": true, "selectionMode": "multiple",
				"options": []any{
					map[string]any{"id": "tables", "label": "数据表"},
					map[string]any{"id": "plots", "label": "图表"},
				},
			}},
		}
	})
	application, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	application.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, application, server.URL)
	project, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "澄清多选与资料刷新"})
	if err != nil {
		t.Fatal(err)
	}
	start := func(idea string) workflow.RunDetail {
		t.Helper()
		run, err := application.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: idea, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
		if err != nil {
			t.Fatal(err)
		}
		return waitForWorkflowState(t, application, project.ID, run.Run.ID, workflow.RunCompleted, 20*time.Second)
	}
	decode := func(run workflow.RunDetail) workflow.ResearchStarterContext {
		t.Helper()
		var input struct {
			Starter workflow.ResearchStarterContext `json:"starter_context"`
		}
		if err := json.Unmarshal(run.Run.Inputs, &input); err != nil {
			t.Fatal(err)
		}
		return input.Starter
	}
	importCSV := func(taskID, name string) {
		t.Helper()
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte("light_hours,height_cm,source\n8,12,"+name+"\n16,21,"+name+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		batch, err := application.AttachmentFacade.ImportTaskDocumentPaths(project.ID, taskID, []string{path})
		if err != nil || len(batch.Errors) != 0 || len(batch.Attachments) != 1 {
			t.Fatalf("import task CSV: %#v, %v", batch, err)
		}
	}
	other := start("另一个任务的私有资料不能影响当前课题")
	importCSV(other.Run.ResearchTaskID, "other-task.csv")
	initial := start("我想分析光照时间对幼苗生长的影响，并整理报告。")
	if got := decode(initial).ResourceSnapshot.AttachmentCount; got != 0 {
		t.Fatalf("new task inherited private attachments: %d", got)
	}
	plan, err := application.WorkflowFacade.GetResearchStarterPlan(project.ID, initial.Run.ID)
	if err != nil || plan.Clarification == nil || plan.Clarification.Questions[0].SelectionMode != "multiple" {
		t.Fatalf("multiple-choice projection: %#v, %v", plan.Clarification, err)
	}
	if _, err := application.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: project.ID, RunID: initial.Run.ID, RouteID: plan.Routes[0].RouteID}); err == nil {
		t.Fatal("an unresolved research direction must still block adoption")
	}
	importCSV(initial.Run.ResearchTaskID, "seedlings.csv")
	answers := map[string][]string{"outputs": {"tables", "plots"}}
	command := workflow.AnswerResearchClarificationCommand{ProjectID: project.ID, StarterRunID: initial.Run.ID, Answers: answers}
	answered, err := application.WorkflowFacade.AnswerResearchClarification(command)
	if err != nil {
		t.Fatal(err)
	}
	answered = waitForWorkflowState(t, application, project.ID, answered.Run.ID, workflow.RunCompleted, 20*time.Second)
	input := decode(answered)
	if answered.Run.ID == initial.Run.ID || answered.Run.ResearchTaskID != initial.Run.ResearchTaskID || input.PriorStarterRunID != initial.Run.ID {
		t.Fatalf("clarification lost task lineage: %#v, %#v", answered.Run, input)
	}
	if input.ResourceSnapshot.AttachmentCount != 1 || !reflect.DeepEqual(input.ResourceSnapshot.TabularFiles, []string{"seedlings.csv"}) || input.ResourceSnapshot.ResourceFingerprint == "" {
		t.Fatalf("current task resources not frozen: %#v", input.ResourceSnapshot)
	}
	if !reflect.DeepEqual(input.ClarificationAnswers, answers) || !strings.Contains(input.ResearchIdea, "数据表") || !strings.Contains(input.ResearchIdea, "图表") {
		t.Fatalf("multiple answers not preserved: %#v", input)
	}
	assertV5 := func(run workflow.RunDetail) {
		t.Helper()
		if len(run.Run.Compilation.Nodes) != 1 || run.Run.Compilation.Nodes[0].PromptVersion != "research-starter-semantic-v6" {
			t.Fatalf("replan did not freeze latest planner: %#v", run.Run.Compilation.Nodes)
		}
	}
	assertV5(answered)
	replay, err := application.WorkflowFacade.AnswerResearchClarification(command)
	if err != nil || replay.Run.ID != answered.Run.ID {
		t.Fatalf("same answers/resources did not replay: %s, %v", replay.Run.ID, err)
	}
	command.Answers = map[string][]string{"outputs": {"tables"}}
	if _, err := application.WorkflowFacade.AnswerResearchClarification(command); err == nil {
		t.Fatal("old starter accepted a different clarification branch")
	}
	if _, err := application.WorkflowFacade.ReplanResearchStarter(workflow.ReplanResearchStarterCommand{ProjectID: project.ID, StarterRunID: initial.Run.ID}); err == nil {
		t.Fatal("old starter accepted a refresh branch")
	}
	importCSV(initial.Run.ResearchTaskID, "additional.csv")
	command.Answers = answers
	if _, err := application.WorkflowFacade.AnswerResearchClarification(command); err == nil {
		t.Fatal("changed resources on an old starter silently returned the stale answer run")
	}
	refresh := workflow.ReplanResearchStarterCommand{ProjectID: project.ID, StarterRunID: answered.Run.ID}
	replanned, err := application.WorkflowFacade.ReplanResearchStarter(refresh)
	if err != nil {
		t.Fatal(err)
	}
	replanned = waitForWorkflowState(t, application, project.ID, replanned.Run.ID, workflow.RunCompleted, 20*time.Second)
	refreshed := decode(replanned)
	if replanned.Run.ID == answered.Run.ID || replanned.Run.ResearchTaskID != initial.Run.ResearchTaskID || refreshed.PriorStarterRunID != answered.Run.ID {
		t.Fatalf("refresh lost task lineage: %#v", replanned.Run)
	}
	if refreshed.ResourceSnapshot.AttachmentCount != 2 || strings.Contains(strings.Join(refreshed.ResourceSnapshot.AttachmentNames, ","), "other-task") || refreshed.ResourceSnapshot.ResourceFingerprint == input.ResourceSnapshot.ResourceFingerprint {
		t.Fatalf("refresh missed new resources or leaked another task: %#v", refreshed.ResourceSnapshot)
	}
	if refreshed.ResearchIdea != input.ResearchIdea || len(refreshed.ClarificationAnswers) != 0 {
		t.Fatalf("refresh fabricated or dropped user choices: %#v", refreshed)
	}
	assertV5(replanned)
	replay, err = application.WorkflowFacade.ReplanResearchStarter(refresh)
	if err != nil || replay.Run.ID != replanned.Run.ID {
		t.Fatalf("refresh replay created another run: %s, %v", replay.Run.ID, err)
	}
	original, err := application.WorkflowFacade.GetRun(project.ID, initial.Run.ID)
	if err != nil || string(original.Run.Inputs) != string(initial.Run.Inputs) {
		t.Fatal("replanning changed original immutable input")
	}
}

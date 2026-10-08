package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

type stageMemoryFixture struct {
	RuntimeRepository
	ResearchDiscussionRepository
	binding ResearchStageReadBinding
	detail  RunDetail
	saved   json.RawMessage
}

func (*stageMemoryFixture) ResolveResearchDiscussion(context.Context, string) (ResearchDiscussionBinding, error) {
	return ResearchDiscussionBinding{}, fmt.Errorf("not a user discussion")
}

func (f *stageMemoryFixture) ResolveResearchStageRead(_ context.Context, id string) (ResearchStageReadBinding, error) {
	if id != "current-chat" {
		return ResearchStageReadBinding{}, fmt.Errorf("unknown stage binding")
	}
	return f.binding, nil
}

func (f *stageMemoryFixture) GetRun(_ context.Context, projectID, runID string) (RunDetail, error) {
	if projectID != f.detail.Run.ProjectID || runID != f.detail.Run.ID {
		return RunDetail{}, fmt.Errorf("foreign task")
	}
	return f.detail, nil
}

func (f *stageMemoryFixture) ReadResearchRevisionSnapshot(_ context.Context, runID, revisionID string) (json.RawMessage, error) {
	if runID != f.detail.Run.ID || revisionID != "confirmed-revision" {
		return nil, fmt.Errorf("foreign or unconfirmed revision")
	}
	return f.saved, nil
}

func stageMemoryService() (*RuntimeService, *stageMemoryFixture) {
	f := &stageMemoryFixture{
		binding: ResearchStageReadBinding{ProjectID: "p", WorkflowRunID: "run", WorkflowStepID: "step", Attempt: 2},
		detail: RunDetail{Run: Run{ID: "run", ProjectID: "p", Status: RunRunning, CurrentStep: 3,
			Compilation: Compilation{Nodes: []CompiledNode{{ID: "implement", Kind: NodeAgentStage, AllowedTools: []ToolSnapshot{{QualifiedName: ResearchTaskReadTool}}}}}},
			Steps: []Step{{ID: "step", NodeID: "implement", Ordinal: 3, Attempt: 2, Status: StepRunning, Input: raw(`{"current":true}`)}},
		},
		saved: rawObject(map[string]any{"steps": []Step{{NodeID: "implement", Status: StepCompleted, Attempt: 1, Output: rawObject(map[string]any{"code": strings.Repeat("# 中文旧代码\n", 2000)})}}}),
	}
	return &RuntimeService{repository: f}, f
}

func TestStageReadsFullHistoricalCodeThroughSameTaskTool(t *testing.T) {
	s, f := stageMemoryService()
	command := ResearchTaskReadCommand{Section: "revision_stage_output", RevisionID: "confirmed-revision", NodeID: "implement"}
	var content strings.Builder
	pages := 0
	for {
		value, err := s.ReadResearchTask(context.Background(), "current-chat", command)
		if err != nil {
			t.Fatal(err)
		}
		if model := tool.BuildModelContextSnapshot("call", "", tool.Result{Status: tool.ResultSuccess, Structured: value}); strings.Contains(model, "tool result truncated") {
			t.Fatal("paged historical code exceeds the model result envelope")
		}
		var page struct {
			Content string `json:"content"`
			Next    int    `json:"nextOffset"`
			More    bool   `json:"hasMore"`
		}
		if err := json.Unmarshal(value, &page); err != nil {
			t.Fatal(err)
		}
		content.WriteString(page.Content)
		pages++
		if !page.More {
			break
		}
		if page.Next <= command.Offset {
			t.Fatal("paging stalled")
		}
		command.Offset = page.Next
	}
	expected, err := researchRevisionStageRecord(f.saved, command)
	if err != nil || content.String() != string(expected) || pages < 2 {
		t.Fatal("historical code was lost or not paged", err)
	}
	command.Offset = -1
	if _, err := s.ReadResearchTask(context.Background(), "current-chat", command); err == nil {
		t.Fatal("invalid page accepted")
	}
}

func TestStageTaskReadPreservesScopeAndDiscussionBoundary(t *testing.T) {
	for _, section := range []string{"supplemental_materials", "supplemental_content", "messages", "attempts", "tool_calls", "revisions"} {
		s, _ := stageMemoryService()
		if _, err := s.ReadResearchTask(context.Background(), "current-chat", ResearchTaskReadCommand{Section: section}); err == nil {
			t.Fatal("stage read private discussion section", section)
		}
	}
	for _, mutate := range []func(*stageMemoryFixture){
		func(f *stageMemoryFixture) { f.binding.Attempt = 1 },
		func(f *stageMemoryFixture) { f.binding.WorkflowStepID = "previous" },
		func(f *stageMemoryFixture) { f.binding.ProjectID = "foreign" },
		func(f *stageMemoryFixture) { f.detail.Steps[0].Status = StepCompleted },
		func(f *stageMemoryFixture) { f.detail.Run.CurrentStep = 9 },
		func(f *stageMemoryFixture) { f.detail.Run.Compilation.Nodes[0].AllowedTools = nil },
	} {
		s, f := stageMemoryService()
		mutate(f)
		if _, err := s.ReadResearchTask(context.Background(), "current-chat", ResearchTaskReadCommand{Section: "overview"}); err == nil {
			t.Fatal("invalid attempt or unfrozen permission accepted")
		}
	}
	s, _ := stageMemoryService()
	if _, err := s.ReadResearchTask(context.Background(), "foreign-chat", ResearchTaskReadCommand{Section: "overview"}); err == nil {
		t.Fatal("unbound chat accepted")
	}
	if _, err := s.ReadResearchTask(context.Background(), "current-chat", ResearchTaskReadCommand{Section: "revision_stage_output", RevisionID: "foreign", NodeID: "implement"}); err == nil {
		t.Fatal("foreign revision accepted")
	}
	if _, err := s.ProposeResearchRevision(context.Background(), "current-chat", "call", ProposeResearchRevisionCommand{}); err == nil {
		t.Fatal("execution stage proposed a user revision")
	}
}

func TestHistoricalInputsAndArtifactsKeepActualVersion(t *testing.T) {
	snapshot := rawObject(map[string]any{"run": Run{Inputs: raw(`{"input_paths":["original.csv"]}`)}, "effectiveInputs": raw(`{"input_paths":["updated.csv"]}`),
		"steps": []Step{{NodeID: "python_analysis", Status: StepCompleted, Attempt: 3, Output: raw(`{"artifacts":[{"workspacePath":"analysis-output/old.py","sha256":"source-hash"}]}`)}}})
	for section, want := range map[string]string{"revision_inputs": "updated.csv", "revision_artifacts": "source-hash"} {
		value, err := researchRevisionRecord(snapshot, ResearchTaskReadCommand{Section: section, RevisionID: "revision"})
		if err != nil || !strings.Contains(string(value), want) || !strings.Contains(string(value), `"historical":true`) {
			t.Fatal(section, string(value), err)
		}
		if section == "revision_inputs" && strings.Contains(string(value), "original.csv") {
			t.Fatal("historical inputs reverted to the first version")
		}
	}
	legacy, err := researchRevisionRecord(raw(`{"run":{"inputs":{"input_paths":["legacy.csv"]}}}`), ResearchTaskReadCommand{Section: "revision_inputs"})
	if err != nil || !strings.Contains(string(legacy), "legacy.csv") {
		t.Fatal("legacy snapshot unreadable", err)
	}
	for _, tools := range [][]ToolSnapshot{nil, {{QualifiedName: ResearchTaskReadTool}}} {
		prompt := buildAIStagePrompt(RunDetail{}, Step{Input: raw(`{}`)}, CompiledNode{AllowedTools: tools})
		if strings.Contains(prompt, "Exact current-task research memory is available") != (len(tools) > 0) {
			t.Fatal("history prompt advertised an unfrozen capability")
		}
	}
}

package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/tool"
)

type openingRecordFixture struct {
	RuntimeRepository
	ResearchDiscussionRepository
	details map[string]RunDetail
	history []conversation.Message
}

func (f *openingRecordFixture) ResearchTaskDiscussionMessages(context.Context, string) ([]conversation.Message, error) {
	return f.history, nil
}

func (f *openingRecordFixture) GetRun(_ context.Context, projectID, runID string) (RunDetail, error) {
	d, ok := f.details[runID]
	if !ok || d.Run.ProjectID != projectID {
		return RunDetail{}, fmt.Errorf("missing run")
	}
	return d, nil
}
func (f *openingRecordFixture) GetRunByConversation(_ context.Context, _ string) (RunDetail, bool, error) {
	return f.details["planner"], true, nil
}

func TestLongPlanningDiscussionIsPreservedAndPagedWithoutStageInputOverflow(t *testing.T) {
	history := make([]conversation.Message, 60)
	ids := []string{}
	for i := range history {
		history[i] = conversation.Message{ID: fmt.Sprint(i), Status: conversation.MessageComplete, Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: strings.Repeat("约定", 20000)}}}
		ids = append(ids, history[i].ID)
	}
	planner := RunDetail{Run: Run{ID: "planner", ProjectID: "p", ResearchTaskID: "task", WorkflowPurpose: PurposeResearchStarter, Inputs: rawObject(map[string]any{"starter_context": ResearchStarterContext{ResearchIdea: "goal", DiscussionRecord: &ResearchPlanningDiscussion{MessageCount: len(history)}}}), Compilation: Compilation{Edges: ResearchStarterTemplate().Definition.Edges}}, Events: []RuntimeEvent{{Type: "workflow.created", Payload: rawObject(map[string]any{"planningDiscussion": map[string]any{"discussionMessageIds": ids}})}}}
	if _, _, err := validateRuntimeInputs(ResearchStarterTemplate().Definition.Inputs, planner.Run.Inputs); err != nil {
		t.Fatal(err)
	}
	bound, err := bindNodeInput(planner, CompiledNode{ID: "explore", Kind: NodeAgentStage, Arguments: raw(`{}`)})
	if err != nil || len(bound) > 32_000 {
		t.Fatal("long history overflowed planner", err)
	}
	formal := RunDetail{Run: Run{ID: "formal", ProjectID: "p", ResearchTaskID: "task", Inputs: rawObject(map[string]any{"route_context": map[string]any{"discussionRecord": ResearchPlanningDiscussion{SourcePlannerRunID: "planner", MessageCount: len(history)}}})}}
	repo := &openingRecordFixture{details: map[string]RunDetail{"planner": planner, "formal": formal}, history: history}
	// Later discussion is not part of this adopted requirements record.
	repo.history = append(append([]conversation.Message(nil), history...), conversation.Message{ID: "later", Status: conversation.MessageComplete, Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: "not adopted"}}})
	s := &RuntimeService{repository: repo}
	expected := rawObject(map[string]any{"discussionHistory": history})
	for _, d := range []RunDetail{planner, formal} {
		var content strings.Builder
		command := ResearchTaskReadCommand{Section: "planning_discussion"}
		for {
			page, err := s.readResearchTaskDetail(context.Background(), d, command)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(tool.BuildModelContextSnapshot("call", "", tool.Result{Status: tool.ResultSuccess, Structured: page}), "tool result truncated") {
				t.Fatal("page lost content")
			}
			var v struct {
				Content string `json:"content"`
				Next    int    `json:"nextOffset"`
				More    bool   `json:"hasMore"`
			}
			if json.Unmarshal(page, &v) != nil {
				t.Fatal(string(page))
			}
			content.WriteString(v.Content)
			if !v.More {
				break
			}
			if v.Next <= command.Offset {
				t.Fatal("stalled paging")
			}
			command.Offset = v.Next
		}
		if content.String() != string(expected) {
			t.Fatal("frozen discussion changed")
		}
	}
	foreign := planner
	foreign.Run.ResearchTaskID = "other-task"
	repo.details["planner"] = foreign
	if _, err := s.planningDiscussionRecord(context.Background(), formal); err == nil {
		t.Fatal("cross-task discussion accepted")
	}
}

func TestLargeCandidateGuidanceUsesPreviewAndFullTaskReader(t *testing.T) {
	planner := RunDetail{Run: Run{ID: "planner", Status: RunCompleted, WorkflowPurpose: PurposeResearchStarter, Inputs: rawObject(map[string]any{"starter_context": ResearchStarterContext{ResearchIdea: strings.Repeat("goal", 2000)}}), Outputs: rawObject(map[string]any{"plan": map[string]any{"reason": strings.Repeat("大计划", 20000)}})}}
	s := RuntimeService{repository: &openingRecordFixture{details: map[string]RunDetail{"planner": planner}}}
	g, bound, err := s.GuidanceForConversation(context.Background(), "c")
	if err != nil || !bound || len(g.SystemContext) > 12_000 || !strings.Contains(g.SystemContext, "nextOffset") || !strings.Contains(g.SystemContext, `"truncated":true`) {
		t.Fatal("candidate pinned full plan in system", err, len(g.SystemContext))
	}
	page, err := s.readResearchTaskDetail(context.Background(), planner, ResearchTaskReadCommand{Section: "outputs"})
	if err != nil || !strings.Contains(string(page), `"hasMore":true`) {
		t.Fatal("full candidate unreadable", err)
	}
}

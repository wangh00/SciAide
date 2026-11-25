package workflow

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestResearchDiscussionOffersWorkingStateWithoutRequiringRoute(t *testing.T) {
	detail := RunDetail{Run: Run{
		ID: "discussion", ProjectID: "p", ResearchTaskID: "task",
		WorkflowPurpose: PurposeResearchStarter, Status: RunCompleted,
		Inputs: raw(`{"discussion_only":true,"starter_context":{"researchIdea":"先比较现有证据，不要开始完整路线"}}`),
	}}
	repository := &openingRecordFixture{details: map[string]RunDetail{"planner": detail}}
	service := &RuntimeService{repository: repository}
	guidance, found, err := service.GuidanceForConversation(context.Background(), "discussion-chat")
	if err != nil || !found {
		t.Fatal("research discussion unavailable", err)
	}
	if !slices.Contains(guidance.AllowedToolNames, "builtin.research.state.update") {
		t.Fatal("research discussion cannot keep working state without preparing a route")
	}
	for _, name := range []string{"builtin.research.catalog", ResearchDiscoverySearchTool, ResearchPlanPrepareTool} {
		if !slices.Contains(guidance.AllowedToolNames, name) {
			t.Fatalf("existing optional capability was removed: %s", name)
		}
	}
	for _, name := range []string{"builtin.workflow.start", "builtin.python.execute", "builtin.workflow.research.import"} {
		if slices.Contains(guidance.AllowedToolNames, name) {
			t.Fatalf("working notes broadened execution authority: %s", name)
		}
	}
}

func TestResearchCandidateGuidancePinsOnlyMetadata(t *testing.T) {
	marker := "candidate-content-is-not-a-system-instruction"
	detail := RunDetail{Run: Run{
		ID: "candidate", ProjectID: "p", ResearchTaskID: "task",
		WorkflowPurpose: PurposeResearchStarter, Status: RunCompleted,
		Inputs:  raw(`{"starter_context":{"researchIdea":"compare evidence"}}`),
		Outputs: rawObject(map[string]any{"content": strings.Repeat(marker, 200)}),
	}}
	service := &RuntimeService{repository: &openingRecordFixture{details: map[string]RunDetail{"planner": detail}}}
	guidance, found, err := service.GuidanceForConversation(context.Background(), "candidate-chat")
	if err != nil || !found {
		t.Fatal("candidate discussion unavailable", err)
	}
	if strings.Contains(guidance.SystemContext, marker) || !strings.Contains(guidance.SystemContext, `"section":"outputs"`) || !strings.Contains(guidance.SystemContext, `"contentOmitted":true`) {
		t.Fatal("candidate body was pinned or its exact reader was lost")
	}
}

func TestResearchDiscussionUsesPersistentTaskDiscovery(t *testing.T) {
	detail := RunDetail{Run: Run{ID: "discussion", ProjectID: "p", ResearchTaskID: "task", WorkflowPurpose: PurposeResearchStarter, Status: RunCompleted, Inputs: raw(`{"discussion_only":true}`)}}
	service := &RuntimeService{repository: &openingRecordFixture{details: map[string]RunDetail{"planner": detail}}}
	guidance, _, err := service.GuidanceForConversation(context.Background(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"builtin.research.discovery.search", "builtin.research.discovery.read"} {
		if !slices.Contains(guidance.AllowedToolNames, name) {
			t.Fatalf("research discussion cannot retain and reread task discovery: %s", name)
		}
	}
	if slices.Contains(guidance.AllowedToolNames, "builtin.research.search") {
		t.Fatal("discussion still advertises the transient search instead of task discovery")
	}
}

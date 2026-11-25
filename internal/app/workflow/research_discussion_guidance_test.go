package workflow

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

type scopedDiscussionFixture struct {
	*openingRecordFixture
	discussion ResearchDiscussionContext
}

func (f *scopedDiscussionFixture) ResearchDiscussionForConversation(context.Context, string) (ResearchDiscussionContext, bool, error) {
	return f.discussion, true, nil
}
func (f *scopedDiscussionFixture) ResearchDiscussionForChat(_ context.Context, chatID string) (ResearchDiscussionContext, bool, error) {
	return f.discussion, chatID == "human", nil
}
func (f *scopedDiscussionFixture) GetLatestTaskRun(context.Context, string, string) (Run, error) {
	return f.details["current"].Run, nil
}
func (f *scopedDiscussionFixture) FindLatestTaskRun(context.Context, string, string) (Run, bool, error) {
	value, found := f.details["current"]
	return value.Run, found, nil
}
func (f *scopedDiscussionFixture) ResolveResearchDiscussion(context.Context, string) (ResearchDiscussionBinding, error) {
	return ResearchDiscussionBinding{ProjectID: "p", WorkflowRunID: "old", UserMessageID: "user"}, nil
}

func TestResearchDiscussionCurrentInputsAreReadOnDemandNotPinned(t *testing.T) {
	ctx := context.Background()
	old := RunDetail{Run: Run{ID: "old", ProjectID: "p", ResearchTaskID: "task", ConversationID: "human-conv", WorkflowPurpose: PurposeResearchStarter, Status: RunCompleted, Inputs: raw(`{"discussion_only":true,"starter_context":{"researchIdea":"original"}}`)}}
	for _, size := range []int{1, 400, 8000} {
		goal, reference := strings.Repeat("未核验需求", size), strings.Repeat("未核验资料名", size)
		current := RunDetail{Run: Run{ID: "current", ProjectID: "p", ResearchTaskID: "task", ConversationID: "planner-conv", WorkflowPurpose: PurposeResearchStarter, Status: RunRunning,
			Inputs: rawObject(map[string]any{"starter_context": map[string]any{"researchIdea": goal, "selectedReferenceMaterials": []any{map[string]any{"attachmentId": "ref", "originalName": reference}}}})}}
		repo := &scopedDiscussionFixture{openingRecordFixture: &openingRecordFixture{details: map[string]RunDetail{"planner": old, "old": old, "current": current}}, discussion: ResearchDiscussionContext{ProjectID: "p", ResearchTaskID: "task", ConversationID: "human-conv", PrimaryConversationID: "human-conv", SourceWorkflowRunID: "old", CanExplore: true}}
		s := &RuntimeService{repository: repo}
		guidance, found, err := s.GuidanceForChat(ctx, "human", "human-conv")
		if err != nil || !found || guidance.WorkflowRunID != "current" || !slices.Contains(guidance.AllowedToolNames, ResearchDiscoveryReadTool) {
			t.Fatal("human role did not follow task identity/current candidate", guidance, found, err)
		}
		if strings.Contains(guidance.SystemContext, "未核验需求") || strings.Contains(guidance.SystemContext, "未核验资料名") || !strings.Contains(guidance.SystemContext, `"section":"inputs"`) {
			t.Fatal("candidate requirements/reference bodies were pinned or their exact reader was removed")
		}
		var content strings.Builder
		command := ResearchTaskReadCommand{Section: "inputs"}
		for {
			raw, err := s.ReadResearchTask(ctx, "human", command)
			var page struct {
				Content string `json:"content"`
				More    bool   `json:"hasMore"`
				Next    int    `json:"nextOffset"`
			}
			if err != nil || json.Unmarshal(raw, &page) != nil {
				t.Fatal("current candidate inputs unavailable", err)
			}
			content.WriteString(page.Content)
			if !page.More {
				break
			}
			if page.Next <= command.Offset {
				t.Fatal("input paging did not advance")
			}
			command.Offset = page.Next
		}
		if content.String() != string(current.Run.Inputs) {
			t.Fatal("moving task data out of the system prompt lost the exact current candidate")
		}
		for _, chatID := range []string{"internal-ai", "unbound"} {
			g, _, err := s.GuidanceForChat(ctx, chatID, "human-conv")
			if err != nil || slices.Contains(g.AllowedToolNames, ResearchStateUpdateTool) || slices.Contains(g.AllowedToolNames, ResearchDiscoveryReadTool) {
				t.Fatal("non-human role acquired exploration tools", chatID, err)
			}
		}
	}
}

package sqlite

import (
	"encoding/json"
	"testing"
)

func TestArchiveRemapsResearchDiscussionIdentities(t *testing.T) {
	maps := newArchiveIDMaps()
	for table, pairs := range map[string]map[string]string{"messages": {"human": "restored-human"}, "conversations": {"conv": "restored-conv"}, "workflow_runs": {"prior": "restored-prior"}, "runs": {"chat": "restored-chat"}, "attachments": {"file": "restored-file"}} {
		maps.values[table] = pairs
	}
	raw := []byte(`{"starter_context":{"priorStarterRunId":"prior","discussionUserMessageId":"human","discussionConversationId":"conv","discussionHistory":[{"id":"human","conversationId":"conv","runId":"chat","parts":[{"payload":{"attachmentId":"file"}}]}]}}`)
	result, err := rewriteWorkflowPayload(raw, maps, "project", "restored-project")
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Starter struct {
			Prior        string `json:"priorStarterRunId"`
			User         string `json:"discussionUserMessageId"`
			Conversation string `json:"discussionConversationId"`
			History      []struct {
				ID           string `json:"id"`
				Conversation string `json:"conversationId"`
				Run          string `json:"runId"`
			} `json:"discussionHistory"`
		} `json:"starter_context"`
	}
	if err := json.Unmarshal(result, &value); err != nil {
		t.Fatal(err)
	}
	v := value.Starter
	if v.Prior != "restored-prior" || v.User != "restored-human" || v.Conversation != "restored-conv" || len(v.History) != 1 || v.History[0].ID != "restored-human" || v.History[0].Conversation != "restored-conv" || v.History[0].Run != "restored-chat" {
		t.Fatalf("wrong restored identity: %s", result)
	}
}

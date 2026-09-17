package chat

import (
	"context"
	"testing"

	"github.com/wangh00/SciAide/internal/app/conversation"
)

func TestStartRetainsValidatedClientMessageIdentity(t *testing.T) {
	const messageID = "0d814b09-7687-4434-a453-24cbdbd2a911"
	for _, value := range []string{messageID, "", "not-an-id", "../other", "0d814b09-7687-1434-a453-24cbdbd2a911"} {
		t.Run(value, func(t *testing.T) {
			repo := &memoryRepo{conversationMode: conversation.PermissionPlan}
			service := NewService(repo, repo, repo, nil)
			if err := service.SetRunner(&blockingStartRunner{started: make(chan struct{})}); err != nil {
				t.Fatal(err)
			}
			defer service.Close()
			run, err := service.Start(context.Background(), StartCommand{ConversationID: "conversation", ModelProfileID: "profile", ModelID: "model", Text: "question", ClientMessageID: value})
			valid := value == "" || value == messageID
			if (err == nil) != valid {
				t.Fatalf("identity=%q error=%v", value, err)
			}
			if valid {
				if run.UserMessageID == "" || (value != "" && run.UserMessageID != value) {
					t.Fatal("client identity lost", run.UserMessageID)
				}
			} else if len(repo.messages) != 0 {
				t.Fatal("invalid identity persisted")
			}
		})
	}
}

package chat

import (
	"context"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"testing"
)

func TestStartFreezesWebOptIn(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		repo := &memoryRepo{conversationMode: conversation.PermissionPlan}
		service := NewService(repo, repo, repo, nil)
		if err := service.SetRunner(&blockingStartRunner{started: make(chan struct{})}); err != nil {
			t.Fatal(err)
		}
		run, err := service.Start(context.Background(), StartCommand{ConversationID: "conversation", ModelProfileID: "profile", ModelID: "model", Text: "question", WebSearchEnabled: enabled})
		service.Close()
		if err != nil || run.WebSearchDisabled == enabled {
			t.Fatalf("enabled=%v run=%+v err=%v", enabled, run, err)
		}
	}
}

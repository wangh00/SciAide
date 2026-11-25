package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/project"
)

func TestConversationMessageOrderBeforeApplyingLimit(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "sciaide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p, err := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash")).Create(ctx, "chat", "")
	if err != nil {
		t.Fatal(err)
	}
	repo := NewConversationRepository(store.DB())
	c, err := conversation.NewService(repo).Create(ctx, p.ID, "chat")
	if err != nil {
		t.Fatal(err)
	}
	epoch := time.Date(2026, 11, 23, 12, 51, 25, 0, time.UTC)
	sameTime := epoch.Add(40 * time.Millisecond)
	lastUser := epoch.Add(66_520_000 * time.Nanosecond)
	ordered := []struct {
		id   string
		role conversation.Role
		at   time.Time
	}{
		{"whole-second-user", conversation.RoleUser, epoch},
		{"one-nanosecond-reply", conversation.RoleAssistant, epoch.Add(time.Nanosecond)},
		{"z-tied-user", conversation.RoleUser, sameTime},
		{"a-tied-reply", conversation.RoleAssistant, sameTime},
		{"latest-user", conversation.RoleUser, lastUser},
		{"latest-reply", conversation.RoleAssistant, lastUser.Add(time.Nanosecond)},
	}
	for i := len(ordered) - 1; i >= 0; i-- {
		item := ordered[i]
		err := repo.CreateMessage(ctx, conversation.Message{
			ID: item.id, ConversationID: c.ID, Role: item.role, Status: conversation.MessageComplete,
			CreatedAt: item.at, UpdatedAt: item.at,
			Parts: []conversation.MessagePart{{ID: item.id + "-part", Type: "text", Text: item.id, CreatedAt: item.at}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	check := func(rows []conversation.Message, err error, wantStart int) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != len(ordered)-wantStart {
			t.Fatalf("got %d messages, want %d", len(rows), len(ordered)-wantStart)
		}
		for i, got := range rows {
			want := ordered[wantStart+i]
			if got.ID != want.id || !got.CreatedAt.Equal(want.at) || len(got.Parts) != 1 || got.Parts[0].Text != want.id {
				t.Fatalf("message %d: got %s at %s, want %s at %s", i, got.ID, got.CreatedAt, want.id, want.at)
			}
		}
	}
	all, err := repo.ListMessages(ctx, c.ID, -1)
	check(all, err, 0)
	lastThree, err := repo.ListMessages(ctx, c.ID, 3)
	check(lastThree, err, 3)
	latestVisible, err := repo.ListVisibleMessages(ctx, c.ID, 1)
	check(latestVisible, err, 5)
}

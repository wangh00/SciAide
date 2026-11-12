package chat

import (
	"context"
	"testing"

	"github.com/wangh00/SciAide/internal/app/conversation"
)

func TestUISnapshotDoesNotChangeCompleteModelOrStageHistory(t *testing.T) {
	repo := &measuredSnapshotRepo{memoryRepo: &memoryRepo{run: Run{ID: "r", ConversationID: "c"}, messages: []conversation.Message{
		{ID: "prompt", Role: conversation.RoleUser, Internal: true},
		{ID: "result", Role: conversation.RoleAssistant, Internal: true},
		{ID: "human", Role: conversation.RoleUser},
	}}}
	s := NewService(repo, repo, repo, nil)
	ui, err := s.UISnapshot(context.Background(), "r")
	if err != nil || len(ui.Messages) != 2 || ui.Messages[0].ID != "result" || ui.Messages[1].ID != "human" {
		t.Fatal(ui.Messages, err)
	}
	full, err := s.Snapshot(context.Background(), "r")
	if err != nil || len(full.Messages) != 3 {
		t.Fatal("model history changed", err)
	}
	stage, err := s.StageSnapshot(context.Background(), "r")
	if err != nil || len(stage.Messages) != 1 || stage.Messages[0].ID != "prompt" {
		t.Fatal("stage prompt changed", err)
	}
}

func TestResumeRecordsSurviveWaitAndReleaseOnlyTerminalRun(t *testing.T) {
	repo := &memoryRepo{run: Run{ID: "r", Status: RunWaitingUserInput}}
	s := NewService(repo, repo, repo, nil)
	s.lastResumeKey["r"] = "user-input:q"
	s.resumedUserInputs["r:user-input:q"] = true
	s.resumedUserInputs["other:user-input:q"] = true
	s.finishActive("r")
	if len(s.lastResumeKey) != 1 || len(s.resumedUserInputs) != 2 {
		t.Fatal("waiting run lost idempotency")
	}
	repo.run.Status = RunCompleted
	s.finishActive("r")
	if len(s.lastResumeKey) != 0 || len(s.resumedUserInputs) != 1 || !s.resumedUserInputs["other:user-input:q"] {
		t.Fatal("incorrect lifecycle cleanup")
	}
	s.Close()
	if len(s.resumedUserInputs) != 0 {
		t.Fatal("close retained input records")
	}
}

package chat

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/tool"
)

type measuredSnapshotRepo struct {
	*memoryRepo
	historyReads, stageReads, stepReads, toolReads int
	sequence                                       int64
	getError                                       error
}

func (r *measuredSnapshotRepo) Get(ctx context.Context, id string) (Run, error) {
	if r.getError != nil {
		return Run{}, r.getError
	}
	return r.memoryRepo.Get(ctx, id)
}
func (r *measuredSnapshotRepo) LatestEventSequence(context.Context, string) (int64, error) {
	return r.sequence, nil
}

func (r *measuredSnapshotRepo) ListMessages(ctx context.Context, id string, limit int) ([]conversation.Message, error) {
	r.historyReads++
	return r.memoryRepo.ListMessages(ctx, id, limit)
}
func (r *measuredSnapshotRepo) ListMessagesForRun(ctx context.Context, cid, id string) ([]conversation.Message, error) {
	r.stageReads++
	return []conversation.Message{r.messages[0]}, nil
}
func (r *measuredSnapshotRepo) ListRunSteps(context.Context, string) ([]RunStep, error) {
	r.stepReads++
	return []RunStep{{Commentary: "full stage result"}}, nil
}
func (r *measuredSnapshotRepo) ListByRun(context.Context, string) ([]tool.Call, error) {
	r.toolReads++
	return []tool.Call{{ID: "call", ToolName: "read", Status: tool.CallCompleted}}, nil
}

func TestActivitySnapshotAvoidsHistoryAndKeepsRunAndTools(t *testing.T) {
	repo := &measuredSnapshotRepo{memoryRepo: &memoryRepo{run: Run{ID: "run", ConversationID: "c"}, messages: []conversation.Message{{ID: "answer", RunID: "run"}, {ID: "other", RunID: "old"}}}}
	service := NewService(repo, repo, repo, nil)
	service.SetSnapshotToolCalls(repo)
	service.SetSnapshotRunSteps(repo)
	full, err := service.Snapshot(context.Background(), "run")
	if err != nil {
		t.Fatal(err)
	}
	light, err := service.ActivitySnapshot(context.Background(), "run")
	if err != nil {
		t.Fatal(err)
	}
	if repo.historyReads != 1 || repo.stepReads != 1 || len(light.Messages) != 0 || len(light.RunSteps) != 0 || !reflect.DeepEqual(full.Run, light.Run) || !reflect.DeepEqual(full.ToolCalls, light.ToolCalls) {
		t.Fatal("activity queried or changed history", repo, light)
	}
	stage, err := service.StageSnapshot(context.Background(), "run")
	if err != nil {
		t.Fatal(err)
	}
	if repo.historyReads != 1 || repo.stageReads != 1 || len(stage.Messages) != 1 || stage.Messages[0].ID != "answer" || len(stage.RunSteps) != 1 {
		t.Fatal("stage recovery lost result", stage)
	}
}

func TestActivityRevisionTracksTerminalChangesWithoutReadingHistory(t *testing.T) {
	repo := &measuredSnapshotRepo{memoryRepo: &memoryRepo{run: Run{ID: "run", Status: RunRunning}}}
	service := NewService(repo, repo, repo, nil)
	if v, err := service.ActivityRevision(context.Background(), "run"); err != nil || v != "" {
		t.Fatal("running activity cached")
	}
	repo.run.Status = RunCompleted
	first, err := service.ActivityRevision(context.Background(), "run")
	if err != nil || first == "" {
		t.Fatal(err)
	}
	repo.sequence++
	next, _ := service.ActivityRevision(context.Background(), "run")
	if next == first {
		t.Fatal("event change ignored")
	}
	repo.run.InputTokens++
	latest, _ := service.ActivityRevision(context.Background(), "run")
	if latest == next {
		t.Fatal("usage change ignored")
	}
	repo.getError = errors.New("run deleted")
	if _, err := service.ActivityRevision(context.Background(), "run"); err == nil {
		t.Fatal("deleted run reused")
	}
	if repo.historyReads != 0 || repo.toolReads != 0 {
		t.Fatal("version check loaded history")
	}
}

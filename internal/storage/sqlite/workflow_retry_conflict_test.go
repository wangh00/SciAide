package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

type interleavedRetryRepository struct {
	*WorkflowRuntimeRepository
	beforeReset func()
}

// Test-only driver interception exercises the actual GetRun query order without
// a production hook. A separate connection performs the competing transition.
type retryReadHookDriver struct {
	base   driver.Driver
	before func(string)
}

type retryReadHookConn struct {
	driver.Conn
	before func(string)
}

func (d retryReadHookDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &retryReadHookConn{Conn: conn, before: d.before}, nil
}

func (c *retryReadHookConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.before(query)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

var retryReadDriverSequence atomic.Uint64

func (r *interleavedRetryRepository) ResetStepForRetry(ctx context.Context, runID, stepID string, decision *workflow.HumanDecision, at time.Time, event workflow.RuntimeEvent, guard workflow.RetryGuard) error {
	if r.beforeReset != nil {
		hook := r.beforeReset
		r.beforeReset = nil
		hook()
	}
	return r.WorkflowRuntimeRepository.ResetStepForRetry(ctx, runID, stepID, decision, at, event, guard)
}

// A deterministic interleaving at the persistence boundary: no provider,
// timing sleeps or extra model requests are involved.
func TestStaleManualRetryCannotResetNewerAttempt(t *testing.T) {
	for _, newerStatus := range []string{"running", "failed"} {
		t.Run(newerStatus, func(t *testing.T) {
			keep := runtimeFixtureTool{name: "fixture.keep", risk: tool.RiskLow, idempotent: true}
			h := newWorkflowRuntimeHarness(t, keep)
			saved := h.save(t, workflow.Definition{SchemaVersion: 1, Name: "retry CAS", Nodes: []workflow.Node{
				{ID: "keep", Name: "Keep checkpoint", Kind: workflow.NodeTool, ToolName: keep.name, Arguments: json.RawMessage(`{"query":"retained"}`)},
				{ID: "draft", Name: "Failed AI stage", Kind: workflow.NodeAIAnalysis, Prompt: "Return summary", PromptVersion: "fixture-v1", OutputSchema: json.RawMessage(`{"type":"object"}`), Arguments: json.RawMessage(`{}`)},
			}, Edges: []workflow.Edge{{FromNode: "keep", FromPort: "structured", ToNode: "draft", ToPort: "context"}}})
			start, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{}`), ModelProfileID: "profile", ModelID: "model"})
			if err != nil {
				t.Fatal(err)
			}
			failed := waitWorkflowStatus(t, h.runtime, h.project.ID, start.Run.ID, workflow.RunFailed)
			h.runtime.Wait()
			step := failed.Steps[1]
			if failed.Steps[0].Status != workflow.StepCompleted || step.Status != workflow.StepFailed || step.InputSHA256 == "" {
				t.Fatal("fixture did not reach a frozen failed AI stage")
			}
			repo := &interleavedRetryRepository{WorkflowRuntimeRepository: NewWorkflowRuntimeRepository(h.store.DB())}
			repo.beforeReset = func() {
				// Another retry has already advanced this exact step generation.
				if _, err := h.store.DB().Exec(`UPDATE workflow_runs SET status=?,completed_at=NULL,cancel_requested=0 WHERE id=?`, newerStatus, failed.Run.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := h.store.DB().Exec(`UPDATE workflow_steps SET status=?,completed_at=NULL,attempt=attempt+1 WHERE id=?`, newerStatus, step.ID); err != nil {
					t.Fatal(err)
				}
			}
			registry := tool.NewRegistry()
			if err := registry.Register(context.Background(), keep); err != nil {
				t.Fatal(err)
			}
			runtime, err := workflow.NewRuntimeService(repo, NewWorkflowRepository(h.store.DB()), NewProjectRepository(h.store.DB()), registry, tool.NewService(NewToolRepository(h.store.DB()), tool.JSONSchemaValidator{}), permission.NewEngine(NewPermissionRepository(h.store.DB())), h.executor)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			facade := wailstransport.NewWorkflowFacade(wailstransport.NewLifecycleContext(), nil, runtime, nil, nil)
			if _, err := facade.Retry(workflow.RetryCommand{ProjectID: h.project.ID, RunID: failed.Run.ID, StepID: step.ID}); apperr.Public(err).Code != "WORKFLOW_RETRY_CONFLICT" || !strings.Contains(err.Error(), "未重复恢复或覆盖新尝试") {
				t.Fatalf("stale manual retry must report a retry conflict, got %v", err)
			}
			current, err := repo.GetRun(context.Background(), h.project.ID, failed.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if string(current.Steps[1].Status) != newerStatus || current.Steps[1].Attempt != step.Attempt+1 || current.Steps[1].InputSHA256 != step.InputSHA256 || string(current.Steps[1].Input) != string(step.Input) {
				t.Fatal("new generation was cleared or replayed")
			}
			if string(current.Steps[0].Output) != string(failed.Steps[0].Output) || current.Steps[0].ToolCallID != failed.Steps[0].ToolCallID {
				t.Fatal("completed upstream changed")
			}
		})
	}
}

func retryGuardFixture(t *testing.T) (*workflowRuntimeHarness, *WorkflowRuntimeRepository, workflow.RunDetail) {
	t.Helper()
	keep := runtimeFixtureTool{name: "fixture.keep", risk: tool.RiskLow, idempotent: true}
	h := newWorkflowRuntimeHarness(t, keep)
	saved := h.save(t, workflow.Definition{SchemaVersion: 1, Name: "retry guard", Nodes: []workflow.Node{
		{ID: "keep", Name: "Keep checkpoint", Kind: workflow.NodeTool, ToolName: keep.name, Arguments: json.RawMessage(`{"query":"retained"}`)},
		{ID: "draft", Name: "Failed AI stage", Kind: workflow.NodeAIAnalysis, Prompt: "Return summary", PromptVersion: "fixture-v1", OutputSchema: json.RawMessage(`{"type":"object"}`), Arguments: json.RawMessage(`{}`)},
	}, Edges: []workflow.Edge{{FromNode: "keep", FromPort: "structured", ToNode: "draft", ToPort: "context"}}})
	start, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{}`), ModelProfileID: "profile", ModelID: "model"})
	if err != nil {
		t.Fatal(err)
	}
	waitWorkflowStatus(t, h.runtime, h.project.ID, start.Run.ID, workflow.RunFailed)
	h.runtime.Wait()
	repo := NewWorkflowRuntimeRepository(h.store.DB())
	detail, err := repo.GetRun(context.Background(), h.project.ID, start.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return h, repo, detail
}

func retryGuardEvent(detail workflow.RunDetail, id string) workflow.RuntimeEvent {
	return workflow.RuntimeEvent{ID: id, WorkflowRunID: detail.Run.ID, Type: "workflow.step_retry_queued", Payload: json.RawMessage(`{}`), CreatedAt: time.Now().UTC()}
}

func TestRetryGuardRejectsChangedStateBeforeAnyMutation(t *testing.T) {
	for _, change := range []string{"cancel", "event_only", "input", "compilation", "producer", "empty_guard", "wrong_run", "wrong_step", "wrong_producer"} {
		t.Run(change, func(t *testing.T) {
			h, repo, detail := retryGuardFixture(t)
			ctx := context.Background()
			guard := workflow.NewRetryGuard(detail, detail.Steps[1], nil)
			var err error
			switch change {
			case "cancel":
				_, err = h.store.DB().Exec(`UPDATE workflow_runs SET cancel_requested=1 WHERE id=?`, detail.Run.ID)
			case "event_only":
				_, err = h.store.DB().Exec(`INSERT INTO workflow_events(id,workflow_run_id,sequence,event_type,payload_json,created_at) SELECT ?,?,COALESCE(MAX(sequence),0)+1,'workflow.checkpoint','{}',? FROM workflow_events WHERE workflow_run_id=?`, "new-checkpoint", detail.Run.ID, formatTime(time.Now().UTC()), detail.Run.ID)
			case "input":
				_, err = h.store.DB().Exec(`UPDATE workflow_steps SET input_sha256=? WHERE id=?`, strings.Repeat("a", 64), detail.Steps[1].ID)
			case "compilation":
				_, err = h.store.DB().Exec(`UPDATE workflow_runs SET compilation_sha256=? WHERE id=?`, strings.Repeat("b", 64), detail.Run.ID)
			case "producer":
				guard = workflow.NewRetryGuard(detail, detail.Steps[1], &detail.Steps[0])
				_, err = h.store.DB().Exec(`UPDATE workflow_steps SET attempt=attempt+1 WHERE id=?`, detail.Steps[0].ID)
			case "empty_guard":
				guard = workflow.RetryGuard{}
			case "wrong_run":
				guard.RunID = "other-run"
			case "wrong_step":
				guard.StepID = detail.Steps[0].ID
			case "wrong_producer":
				guard.ProducerStepID = detail.Steps[1].ID
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := repo.GetRun(ctx, h.project.ID, detail.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			at := time.Now().UTC()
			decision := &workflow.HumanDecision{ID: "not-committed", WorkflowRunID: detail.Run.ID, WorkflowStepID: detail.Steps[1].ID, Kind: "retry_side_effect", Attempt: detail.Steps[1].Attempt + 1, Approved: true, Context: json.RawMessage(`{}`), CreatedAt: at}
			if change == "producer" || change == "wrong_producer" {
				err = repo.ResetStepsForUpstreamRetry(ctx, detail.Run.ID, detail.Steps[0].ID, detail.Steps[1].ID, decision, at, retryGuardEvent(detail, "not-committed"), guard)
			} else {
				err = repo.ResetStepForRetry(ctx, detail.Run.ID, detail.Steps[1].ID, decision, at, retryGuardEvent(detail, "not-committed"), guard)
			}
			if apperr.Public(err).Code != "WORKFLOW_RETRY_CONFLICT" {
				t.Fatalf("expected retry conflict, got %v", err)
			}
			after, err := repo.GetRun(ctx, h.project.ID, detail.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("conflict changed durable state, history, or decisions")
			}
		})
	}
}

func TestRetryGuardAllowsExactlyOneConcurrentReset(t *testing.T) {
	h, repo, detail := retryGuardFixture(t)
	guard := workflow.NewRetryGuard(detail, detail.Steps[1], nil)
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, id := range []string{"first-retry", "second-retry"} {
		workers.Add(1)
		go func(id string) {
			defer workers.Done()
			<-start
			results <- repo.ResetStepForRetry(context.Background(), detail.Run.ID, detail.Steps[1].ID, nil, time.Now().UTC(), retryGuardEvent(detail, id), guard)
		}(id)
	}
	close(start)
	workers.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if apperr.Public(err).Code == "WORKFLOW_RETRY_CONFLICT" {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
	current, err := repo.GetRun(context.Background(), h.project.ID, detail.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Events) != len(detail.Events)+1 || current.Steps[1].Status != workflow.StepQueued || current.Steps[0].ToolCallID != detail.Steps[0].ToolCallID || !reflect.DeepEqual(current.AIExecutions, detail.AIExecutions) {
		t.Fatal("concurrent reset duplicated events, lost history, or changed the completed checkpoint")
	}
}

func TestRetryGuardFencesMixedReadAfterCancelABA(t *testing.T) {
	h, repo, failed := retryGuardFixture(t)
	ctx := context.Background()
	// Establish a cancelled AI step that has not begun in this generation.
	queueAndCancel := func(detail workflow.RunDetail, id string) workflow.RunDetail {
		t.Helper()
		event := retryGuardEvent(detail, id+"-retry")
		if err := repo.ResetStepForRetry(ctx, detail.Run.ID, detail.Steps[1].ID, nil, time.Now().UTC(), event, workflow.NewRetryGuard(detail, detail.Steps[1], nil)); err != nil {
			t.Fatal(err)
		}
		event.ID, event.Type = id+"-cancel", "workflow.cancelled"
		if err := repo.RequestCancel(ctx, detail.Run.ID, time.Now().UTC(), event); err != nil {
			t.Fatal(err)
		}
		cancelled, err := repo.GetRun(ctx, h.project.ID, detail.Run.ID)
		if err != nil {
			t.Fatal(err)
		}
		return cancelled
	}
	old := queueAndCancel(failed, "initial")
	var latest workflow.RunDetail
	interleaved := false
	name := fmt.Sprintf("retry-read-fence-%d", retryReadDriverSequence.Add(1))
	sql.Register(name, retryReadHookDriver{base: h.store.DB().Driver(), before: func(query string) {
		if !interleaved && strings.HasPrefix(query, "SELECT id,workflow_run_id,sequence,event_type,payload_json,created_at FROM workflow_events") {
			interleaved = true
			latest = queueAndCancel(old, "newer")
		}
	}})
	var sequence int
	var schema, dbPath string
	if err := h.store.DB().QueryRowContext(ctx, "PRAGMA database_list").Scan(&sequence, &schema, &dbPath); err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open(name, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	reader.SetMaxOpenConns(1)
	mixed, err := NewWorkflowRuntimeRepository(reader).GetRun(ctx, h.project.ID, old.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !interleaved || mixed.RetryEventSequence != old.RetryEventSequence || !reflect.DeepEqual(mixed.Steps, old.Steps) || !reflect.DeepEqual(mixed.Events, latest.Events) {
		t.Fatal("GetRun did not preserve the pre-read fence across the real read interleaving")
	}
	if old.Steps[1].Attempt != latest.Steps[1].Attempt || old.Steps[1].InputSHA256 != "" || latest.Steps[1].InputSHA256 != "" || old.Run.Status != latest.Run.Status || old.Steps[1].Status != latest.Steps[1].Status || latest.RetryEventSequence <= old.RetryEventSequence {
		t.Fatal("fixture did not reproduce a same-generation cancel ABA")
	}
	// GetRun returned old run/steps but newer events. Its start fence must NOT
	// be rebuilt from that list, even if the captured start sequence is zero.
	for _, sequence := range []int64{old.RetryEventSequence, 0} {
		mixed.RetryEventSequence = sequence
		guard := workflow.NewRetryGuard(mixed, mixed.Steps[1], nil)
		if guard.EventSequence != sequence {
			t.Fatal("guard acquired a newer sequence from a mixed read")
		}
		if err := repo.ResetStepForRetry(ctx, old.Run.ID, old.Steps[1].ID, nil, time.Now().UTC(), retryGuardEvent(old, "stale-retry"), guard); apperr.Public(err).Code != "WORKFLOW_RETRY_CONFLICT" {
			t.Fatalf("mixed read cleared newer cancellation: %v", err)
		}
	}
	after, err := repo.GetRun(ctx, h.project.ID, old.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(latest, after) {
		t.Fatal("stale retry changed cancelled state or history")
	}
}

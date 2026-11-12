package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
)

type plannerStorageFixture struct {
	*implementationStorageFixture
	base chat.PlannerCheckpoint
}

func newPlannerStorageFixture(t *testing.T) *plannerStorageFixture {
	t.Helper()
	db, path := workflowReasoningHistory(t)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workflow_ai_executions SET status='cancelled'`); err != nil {
		t.Fatal(err)
	}
	f := &plannerStorageFixture{implementationStorageFixture: &implementationStorageFixture{db: db, path: path, repo: NewRunRepository(db)}}
	f.createPlannerAttempt(t, 10)
	return f
}

func (f *plannerStorageFixture) createPlannerAttempt(t *testing.T, attempt int) {
	t.Helper()
	ctx, now := context.Background(), time.Now().UTC()
	var conversationID string
	if err := f.db.QueryRow(`SELECT conversation_id FROM workflow_conversations WHERE workflow_run_id='wr'`).Scan(&conversationID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE workflow_runs SET status='running',current_step_ordinal=0,cancel_requested=0 WHERE id='wr'`); err != nil {
		t.Fatal(err)
	}
	input := strings.Repeat("b", 64)
	if _, err := f.db.Exec(`UPDATE workflow_steps SET node_id='explore',status='running',attempt=?,input_sha256=? WHERE id='step'`, attempt, input); err != nil {
		t.Fatal(err)
	}
	runID := fmt.Sprintf("planner-%d", attempt)
	run := chat.Run{ID: runID, ConversationID: conversationID, UserMessageID: runID + "-u", AssistantMessageID: runID + "-a", ModelProfileID: "profile", ModelID: "fixture", Status: chat.RunRunning, CreatedAt: now, StartedAt: &now, UpdatedAt: now}
	u := conversation.Message{ID: run.UserMessageID, ConversationID: conversationID, RunID: run.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now}
	a := conversation.Message{ID: run.AssistantMessageID, ConversationID: conversationID, RunID: run.ID, Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now, UpdatedAt: now}
	schema := json.RawMessage(`{"type":"object","properties":{"normalizedQuestion":{"type":"string"},"selectedSkills":{"type":"array"},"routes":{"type":"array"}}}`)
	c := chat.WorkflowAIExecution{ID: runID + "-execution", WorkflowRunID: "wr", WorkflowStepID: "step", Attempt: attempt, NodeKind: "agent_stage", NodeID: "explore", PromptVersion: "research-starter-semantic-v6", PromptText: "unchanged planning contract", PromptSHA256: strings.Repeat("a", 64), InputSHA256: input, OutputSchema: schema, OutputSchemaSHA256: fmt.Sprintf("%x", sha256.Sum256(schema)), AllowedTools: json.RawMessage(`[]`), CreatedAt: now}
	if err := f.repo.CreateWorkflowAIWithMessages(ctx, run, u, a, c); err != nil {
		t.Fatal(err)
	}
	f.implementationStorageFixture.base = chat.ImplementationDraft{RunID: run.ID, ExecutionID: c.ID}
	f.base = chat.PlannerCheckpoint{Version: 1, RunID: run.ID, ExecutionID: c.ID, WorkflowRunID: c.WorkflowRunID, WorkflowStepID: c.WorkflowStepID, Attempt: attempt, InputSHA256: c.InputSHA256, PromptSHA256: c.PromptSHA256, SchemaSHA256: c.OutputSchemaSHA256, UpdatedAt: now}
}

func (f *plannerStorageFixture) begin(t *testing.T, v chat.PlannerCheckpoint) chat.PlannerCheckpoint {
	t.Helper()
	ctx, now := context.Background(), time.Now().UTC()
	run, err := f.repo.IncrementModelTurns(ctx, v.RunID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repo.BeginModelTurn(ctx, run.ID, run.ModelTurns, now); err != nil {
		t.Fatal(err)
	}
	v.TurnIndex, v.RequestInFlight, v.UpdatedAt = run.ModelTurns, true, now
	return v
}

func (f *plannerStorageFixture) reject(t *testing.T, v chat.PlannerCheckpoint, raw string) chat.PlannerCheckpoint {
	t.Helper()
	ctx, now := context.Background(), time.Now().UTC()
	if err := f.repo.UpdateModelTurnDraft(ctx, v.RunID, v.TurnIndex, raw, 0, now); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.FinishModelTurn(ctx, v.RunID, v.TurnIndex, chat.ModelTurnCompleted, "stop", 0, now); err != nil {
		t.Fatal(err)
	}
	v.RequestInFlight = false
	v.ContentCorrections++
	v.Correction = "specific format failure"
	v.Candidate = raw
	v.CandidateTurnIndex = v.TurnIndex
	v.CandidateSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
	v.UpdatedAt = now
	if err := f.repo.SavePlannerCheckpoint(ctx, v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestPlannerCheckpointStorageRestartPreservesBaselineAndBudgets(t *testing.T) {
	f := newPlannerStorageFixture(t)
	ctx := context.Background()
	v := f.begin(t, f.base)
	if err := f.repo.SavePlannerCheckpoint(ctx, v); err != nil {
		t.Fatal(err)
	}
	v = f.reject(t, v, "原始候选 } trailing")
	// A new connection/repository reads the exact same private host state.
	store, err := Open(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reopened := NewRunRepository(store.DB())
	got, found, err := reopened.PlannerCheckpoint(ctx, v.RunID)
	if err != nil || !found || !chat.SamePlannerCheckpoint(v, got) {
		t.Fatal("checkpoint changed across reopen", found, err)
	}
	var before int
	if err := f.db.QueryRow(`SELECT count(*) FROM run_events WHERE event_type=?`, plannerCheckpointEvent).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := reopened.SavePlannerCheckpoint(ctx, got); err != nil {
		t.Fatal(err)
	}
	var after int
	_ = f.db.QueryRow(`SELECT count(*) FROM run_events WHERE event_type=?`, plannerCheckpointEvent).Scan(&after)
	if after != before {
		t.Fatal("idempotent save duplicated immutable state")
	}
	request := f.begin(t, got)
	bad := request
	bad.ContentCorrections = 0
	if err := f.repo.SavePlannerCheckpoint(ctx, bad); err == nil {
		t.Fatal("resume refunded spent corrections")
	}
	if err := f.repo.SavePlannerCheckpoint(ctx, request); err != nil {
		t.Fatal(err)
	}
	bad = request
	bad.RequestInFlight = false
	if err := f.repo.SavePlannerCheckpoint(ctx, bad); err == nil {
		t.Fatal("undecided streaming request was called a ready result")
	}
}

func TestPlannerCheckpointStorageRejectsChangedIdentityAndLateWrites(t *testing.T) {
	for _, kind := range []string{"candidate", "hash", "execution", "attempt", "cancelled", "rewound"} {
		t.Run(kind, func(t *testing.T) {
			f := newPlannerStorageFixture(t)
			ctx := context.Background()
			v := f.begin(t, f.base)
			if err := f.repo.SavePlannerCheckpoint(ctx, v); err != nil {
				t.Fatal(err)
			}
			v = f.reject(t, v, "original rejected baseline")
			next := f.begin(t, v)
			switch kind {
			case "candidate":
				next.Candidate = "changed"
				next.CandidateSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(next.Candidate)))
			case "hash":
				next.InputSHA256 = strings.Repeat("d", 64)
			case "execution":
				next.ExecutionID = "other"
			case "attempt":
				next.Attempt++
			case "cancelled":
				if _, err := f.db.Exec(`UPDATE workflow_runs SET cancel_requested=1 WHERE id='wr'`); err != nil {
					t.Fatal(err)
				}
			case "rewound":
				if _, err := f.db.Exec(`UPDATE workflow_steps SET attempt=attempt+1 WHERE id='step'`); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.repo.SavePlannerCheckpoint(ctx, next); err == nil {
				t.Fatal("changed or late checkpoint accepted", kind)
			}
			got, found, err := f.repo.PlannerCheckpoint(ctx, v.RunID)
			if kind != "rewound" && (err != nil || !found || !chat.SamePlannerCheckpoint(v, got)) {
				t.Fatal("failed save replaced original checkpoint", found, err)
			}
		})
	}
}

func TestPlannerCheckpointNewExplicitAttemptDoesNotAdoptOldBudgetOrDraft(t *testing.T) {
	f := newPlannerStorageFixture(t)
	ctx := context.Background()
	v := f.begin(t, f.base)
	if err := f.repo.SavePlannerCheckpoint(ctx, v); err != nil {
		t.Fatal(err)
	}
	f.reject(t, v, "retain old attempt verbatim")
	f.finishAttempt(t, "failed", "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED")
	f.createPlannerAttempt(t, 11)
	if _, found, err := f.repo.PlannerCheckpoint(ctx, f.base.RunID); err != nil || found {
		t.Fatal("new attempt inherited the previous rejected state", found, err)
	}
	v = f.begin(t, f.base)
	if err := f.repo.SavePlannerCheckpoint(ctx, v); err != nil {
		t.Fatal(err)
	}
}

func TestPlannerCheckpointReferencesLargeCandidateWithoutReescapingOrTruncating(t *testing.T) {
	f := newPlannerStorageFixture(t)
	ctx := context.Background()
	v := f.begin(t, f.base)
	if err := f.repo.SavePlannerCheckpoint(ctx, v); err != nil {
		t.Fatal(err)
	}
	raw := `{"normalizedQuestion":"` + strings.Repeat("<", 180000) + `","selectedSkills":[],"routes":[]}`
	v = f.reject(t, v, raw)
	var payload string
	if err := f.db.QueryRow(`SELECT payload_json FROM run_events WHERE aggregate_id=? AND event_type=? ORDER BY sequence DESC LIMIT 1`, v.RunID, plannerCheckpointEvent).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) > 4096 || strings.Contains(payload, `\u003c`) || strings.Contains(payload, `"candidate":`) {
		t.Fatal("checkpoint duplicated original candidate into escaped event", len(payload))
	}
	got, found, err := f.repo.PlannerCheckpoint(ctx, v.RunID)
	if err != nil || !found || got.Candidate != raw || got.CandidateSHA256 != v.CandidateSHA256 {
		t.Fatal("large journal reference lost original candidate", found, err)
	}
}

func TestPlannerCheckpointExecutionAdmissionDoesNotHideRetainedAudit(t *testing.T) {
	for _, mutation := range []string{
		`UPDATE workflow_runs SET status='interrupted' WHERE id='wr'`,
		`UPDATE workflow_runs SET status='failed' WHERE id='wr'`,
		`UPDATE workflow_runs SET cancel_requested=1 WHERE id='wr'`,
		`UPDATE workflow_steps SET status='interrupted' WHERE id='step'`,
		`UPDATE workflow_steps SET attempt=attempt+1 WHERE id='step'`,
		`UPDATE workflow_ai_executions SET status='interrupted' WHERE id=?`,
	} {
		t.Run(fmt.Sprintf("state_%x", sha256.Sum256([]byte(mutation))), func(t *testing.T) {
			f := newPlannerStorageFixture(t)
			ctx := context.Background()
			v := f.begin(t, f.base)
			if err := f.repo.SavePlannerCheckpoint(ctx, v); err != nil {
				t.Fatal(err)
			}
			v = f.reject(t, v, "retained inactive baseline")
			c, found, err := f.repo.WorkflowAIContract(ctx, v.RunID)
			if err != nil || !found {
				t.Fatal(found, err)
			}
			if err := f.repo.CheckPlannerCheckpointActive(ctx, v.RunID, c); err != nil {
				t.Fatal("active planner was rejected", err)
			}
			args := []any{}
			if strings.Contains(mutation, "?") {
				args = append(args, v.ExecutionID)
			}
			if _, err := f.db.Exec(mutation, args...); err != nil {
				t.Fatal(err)
			}
			if err := f.repo.CheckPlannerCheckpointActive(ctx, v.RunID, c); err == nil {
				t.Fatal("inactive binding admitted approved tools")
			}
			got, exists, err := f.repo.PlannerCheckpoint(ctx, v.RunID)
			if err != nil || !exists || !chat.SamePlannerCheckpoint(v, got) {
				t.Fatal("inactive admission erased or hid original audit", exists, err)
			}
		})
	}
}

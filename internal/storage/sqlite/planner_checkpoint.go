package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/events"
	"github.com/wangh00/SciAide/internal/id"
)

const plannerCheckpointEvent = "run.planner_checkpoint"

// Audit reads remain available after interruption; execution admission is a
// separate check and must happen before an approved old tool can be processed.
func (r *RunRepository) CheckPlannerCheckpointActive(ctx context.Context, runID string, c chat.WorkflowAIExecution) error {
	var active int
	err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM runs r
		JOIN workflow_ai_chat_runs b ON b.chat_run_id=r.id JOIN workflow_ai_executions e ON e.id=b.execution_id
		JOIN workflow_steps s ON s.id=e.workflow_step_id JOIN workflow_runs w ON w.id=e.workflow_run_id
		WHERE r.id=? AND r.status='running' AND e.id=? AND e.status='running'
		AND e.workflow_run_id=? AND e.workflow_step_id=? AND e.attempt=?
		AND e.input_sha256=? AND e.prompt_sha256=? AND e.output_schema_sha256=?
		AND s.status='running' AND s.attempt=e.attempt AND s.input_sha256=e.input_sha256
		AND w.current_step_ordinal=s.ordinal AND w.status='running' AND w.cancel_requested=0`,
		runID, c.ID, c.WorkflowRunID, c.WorkflowStepID, c.Attempt, c.InputSHA256, c.PromptSHA256, c.OutputSchemaSHA256).Scan(&active)
	if err != nil {
		return err
	}
	if active != 1 {
		return fmt.Errorf("planner execution is no longer the active frozen step")
	}
	return nil
}

// Private, immutable host events, like implementation checkpoints. No schema
// migration, UI draft replacement, cross-attempt adoption or scientific approval.
func readPlannerCheckpoint(ctx context.Context, q implementationQueryer, runID string) (chat.PlannerCheckpoint, bool, error) {
	var data []byte
	err := q.QueryRowContext(ctx, `SELECT payload_json FROM run_events WHERE aggregate_id=? AND event_type=? ORDER BY sequence DESC LIMIT 1`, runID, plannerCheckpointEvent).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return chat.PlannerCheckpoint{}, false, nil
	}
	if err != nil {
		return chat.PlannerCheckpoint{}, false, err
	}
	var v chat.PlannerCheckpoint
	if len(data) > 1<<20 || json.Unmarshal(data, &v) != nil || v.RunID != runID {
		return v, false, fmt.Errorf("invalid persisted planner checkpoint")
	}
	if v.CandidateTurnIndex != 0 {
		var status string
		if err := q.QueryRowContext(ctx, `SELECT draft_text,status FROM model_turn_journal WHERE run_id=? AND turn_index=?`, runID, v.CandidateTurnIndex).Scan(&v.Candidate, &status); err != nil {
			return v, false, err
		} else if status != "completed" {
			return v, false, fmt.Errorf("planner checkpoint baseline journal is not completed")
		}
	}
	if err := chat.ValidatePlannerCheckpoint(v); err != nil {
		return v, false, err
	}
	return v, true, nil
}

func checkPlannerCheckpointCandidate(ctx context.Context, q implementationQueryer, v chat.PlannerCheckpoint) error {
	if v.Candidate == "" {
		return nil
	}
	var draft, status string
	if err := q.QueryRowContext(ctx, `SELECT draft_text,status FROM model_turn_journal WHERE run_id=? AND turn_index=?`, v.RunID, v.CandidateTurnIndex).Scan(&draft, &status); err != nil {
		return err
	}
	if status != "completed" || draft != v.Candidate {
		return fmt.Errorf("planner checkpoint baseline does not match its completed original journal")
	}
	return nil
}

func (r *RunRepository) PlannerCheckpoint(ctx context.Context, runID string) (chat.PlannerCheckpoint, bool, error) {
	v, found, err := readPlannerCheckpoint(ctx, r.db, runID)
	if err != nil || !found {
		return v, found, err
	}
	c, exists, err := r.WorkflowAIContract(ctx, runID)
	if err != nil {
		return v, false, err
	}
	if !exists || !workflow.IsResearchPlannerSchema(c.OutputSchema) || !v.Matches(runID, c) {
		return v, false, fmt.Errorf("planner checkpoint frozen binding changed")
	}
	if err := checkPlannerCheckpointCandidate(ctx, r.db, v); err != nil {
		return v, false, err
	}
	return v, true, nil
}

func (r *RunRepository) SavePlannerCheckpoint(ctx context.Context, v chat.PlannerCheckpoint) error {
	if err := chat.ValidatePlannerCheckpoint(v); err != nil {
		return err
	}
	c, found, err := r.WorkflowAIContract(ctx, v.RunID)
	if err != nil {
		return err
	}
	if !found || !workflow.IsResearchPlannerSchema(c.OutputSchema) || !v.Matches(v.RunID, c) {
		return fmt.Errorf("planner checkpoint frozen binding changed")
	}
	data, err := json.Marshal(v)
	if err != nil || len(data) > 1<<20 {
		return fmt.Errorf("planner checkpoint payload exceeds private event bounds")
	}
	eventID, err := id.New()
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active int
	status := "completed"
	if v.RequestInFlight {
		status = "streaming"
	}
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM runs r
		JOIN workflow_ai_chat_runs b ON b.chat_run_id=r.id JOIN workflow_ai_executions e ON e.id=b.execution_id
		JOIN workflow_steps s ON s.id=e.workflow_step_id JOIN workflow_runs w ON w.id=e.workflow_run_id
		JOIN model_turn_journal j ON j.run_id=r.id AND j.turn_index=r.model_turns
		WHERE r.id=? AND r.status='running' AND r.model_turns=? AND j.status=?
		AND e.id=? AND e.status='running' AND e.workflow_run_id=? AND e.workflow_step_id=? AND e.attempt=?
		AND e.input_sha256=? AND e.prompt_sha256=? AND e.output_schema_sha256=?
		AND s.status='running' AND s.attempt=e.attempt AND s.input_sha256=e.input_sha256
		AND w.current_step_ordinal=s.ordinal AND w.status='running' AND w.cancel_requested=0`,
		v.RunID, v.TurnIndex, status, v.ExecutionID, v.WorkflowRunID, v.WorkflowStepID, v.Attempt, v.InputSHA256, v.PromptSHA256, v.SchemaSHA256).Scan(&active)
	if err != nil {
		return err
	}
	if active != 1 {
		return fmt.Errorf("planner checkpoint does not match the active response and attempt")
	}
	if err := checkPlannerCheckpointCandidate(ctx, tx, v); err != nil {
		return err
	}
	previous, exists, err := readPlannerCheckpoint(ctx, tx, v.RunID)
	if err != nil {
		return err
	}
	if exists {
		if chat.SamePlannerCheckpoint(previous, v) {
			return nil
		}
		if !chat.ValidPlannerCheckpointTransition(previous, v) {
			return fmt.Errorf("planner checkpoint cannot reset budgets or rewrite an undecided request")
		}
	} else if !v.RequestInFlight || v.TurnIndex != 1 || v.ProgressCorrections != 0 || v.ContentCorrections != 0 || v.SkillCorrections != 0 || v.Candidate != "" || v.Correction != "" || v.SkillPending || v.LastContentDiagnostic != "" {
		return fmt.Errorf("planner checkpoint cannot invent a legacy recovery baseline")
	}
	event := events.New(eventID, v.RunID, "run", plannerCheckpointEvent, 0, data)
	if err := appendNextEventTx(ctx, tx, &event); err != nil {
		return err
	}
	return tx.Commit()
}

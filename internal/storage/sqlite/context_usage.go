package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/events"
	"github.com/wangh00/SciAide/internal/id"
)

func (r *RunRepository) contextUsageMatchesSuccess(ctx context.Context, value chat.ContextUsageObservation) bool {
	run, err := r.Get(ctx, value.RunID)
	if err != nil || !value.Matches(run) {
		return false
	}
	var completed string
	err = r.db.QueryRowContext(ctx, `SELECT completed_at FROM model_request_usage WHERE run_id=? AND turn_index=? AND request_kind='conversation' AND status_code>=200 AND status_code<300 AND error_code='' AND input_tokens=? AND model_profile_id=? AND model_id=? AND api_protocol=?
		AND turn_index=(SELECT MAX(turn_index) FROM model_request_usage WHERE run_id=? AND request_kind='conversation')`,
		value.RunID, value.TurnIndex, value.InputTokens, value.ModelProfileID, value.ModelID, value.APIProtocol, value.RunID).Scan(&completed)
	if err != nil {
		return false
	}
	completedAt, err := parseTime(completed)
	if err != nil {
		return false
	}
	// The loop clears the anchor when a checkpoint changes. Apply that same
	// rule on restart even if no subsequent conversation request was saved.
	var checkpointCreated string
	err = r.db.QueryRowContext(ctx, `SELECT created_at FROM conversation_context_checkpoints WHERE conversation_id=? ORDER BY revision DESC LIMIT 1`, run.ConversationID).Scan(&checkpointCreated)
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	if err != nil {
		return false
	}
	checkpointAt, err := parseTime(checkpointCreated)
	return err == nil && !checkpointAt.After(completedAt)
}

func (r *RunRepository) SaveContextUsage(ctx context.Context, value chat.ContextUsageObservation) error {
	if !r.contextUsageMatchesSuccess(ctx, value) {
		return fmt.Errorf("context usage observation is not bound to a successful conversation request")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	eventID, err := id.New()
	if err != nil {
		return err
	}
	_, err = r.AppendNext(ctx, events.New(eventID, value.RunID, "run", "run.context_usage", 0, data))
	return err
}

func (r *RunRepository) LatestContextUsage(ctx context.Context, runID string) (chat.ContextUsageObservation, bool, error) {
	var data []byte
	err := r.db.QueryRowContext(ctx, `SELECT payload_json FROM run_events WHERE aggregate_id=? AND event_type='run.context_usage' ORDER BY sequence DESC LIMIT 1`, runID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return chat.ContextUsageObservation{}, false, nil
	}
	if err != nil {
		return chat.ContextUsageObservation{}, false, err
	}
	var value chat.ContextUsageObservation
	if err := json.Unmarshal(data, &value); err != nil {
		return value, false, err
	}
	if value.RunID != runID || !r.contextUsageMatchesSuccess(ctx, value) {
		return chat.ContextUsageObservation{}, false, nil
	}
	return value, true, nil
}

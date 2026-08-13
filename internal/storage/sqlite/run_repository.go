package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/events"
)

type RunRepository struct{ db *sql.DB }

func NewRunRepository(db *sql.DB) *RunRepository { return &RunRepository{db: db} }

func (r *RunRepository) CreateWithMessages(ctx context.Context, value chat.Run, userMessage, assistantMessage conversation.Message) error {
	if !value.PermissionMode.Valid() {
		value.PermissionMode = conversation.PermissionPlan
	}
	if !value.RequestedReasoningLevel.Valid() {
		value.RequestedReasoningLevel = "medium"
	}
	if value.ContextWindowTokens <= 0 {
		value.ContextWindowTokens = 200_000
	}
	if !value.APIProtocol.Valid() {
		value.APIProtocol = modelprofile.ProtocolOpenAIChat
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin run: %w", err)
	}
	defer tx.Rollback()
	if err := insertMessage(ctx, tx, userMessage); err != nil {
		return err
	}
	if err := insertMessage(ctx, tx, assistantMessage); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runs(id, conversation_id, user_message_id, assistant_message_id, model_profile_id, model_id, api_protocol, requested_reasoning_level, resolved_reasoning_level, context_window_tokens, context_compacted, permission_mode, status, error_code, error_message, input_tokens, fresh_input_tokens, output_tokens, cached_input_tokens, cache_write_tokens, cache_reported_turns, cache_reported_fresh_input_tokens, cache_hit_turns, model_turns, finish_reason, created_at, started_at, completed_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		value.ID, value.ConversationID, value.UserMessageID, nullableString(value.AssistantMessageID), value.ModelProfileID, value.ModelID, value.APIProtocol, value.RequestedReasoningLevel, value.ResolvedReasoningLevel, value.ContextWindowTokens, value.ContextCompacted, value.PermissionMode, value.Status, value.ErrorCode, value.ErrorMessage, value.InputTokens, value.FreshInputTokens, value.OutputTokens, value.CachedInputTokens, value.CacheWriteTokens, value.CacheReportedTurns, value.CacheReportedFreshInputTokens, value.CacheHitTurns, value.ModelTurns, value.FinishReason, formatTime(value.CreatedAt), nullableTime(value.StartedAt), nullableTime(value.CompletedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE conversations SET updated_at=? WHERE id=?`, formatTime(value.UpdatedAt), value.ConversationID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *RunRepository) Get(ctx context.Context, id string) (chat.Run, error) {
	return scanRun(r.db.QueryRowContext(ctx, runSelect+` WHERE id = ?`, id))
}

func (r *RunRepository) LatestForConversation(ctx context.Context, conversationID string) (chat.Run, bool, error) {
	value, err := scanRun(r.db.QueryRowContext(ctx, runSelect+` WHERE conversation_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, conversationID))
	if errors.Is(err, sql.ErrNoRows) {
		return chat.Run{}, false, nil
	}
	if err != nil {
		return chat.Run{}, false, err
	}
	return value, true, nil
}

func (r *RunRepository) IncrementModelTurns(ctx context.Context, runID string, maximum int, at time.Time) (chat.Run, error) {
	if maximum <= 0 {
		return chat.Run{}, fmt.Errorf("model turn budget must be positive")
	}
	result, err := r.db.ExecContext(ctx, `UPDATE runs SET model_turns=model_turns+1, updated_at=? WHERE id=? AND status='running' AND model_turns < ?`, formatTime(at), runID, maximum)
	if err != nil {
		return chat.Run{}, fmt.Errorf("increment model turns: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		var status chat.RunStatus
		var turns int
		if err := r.db.QueryRowContext(ctx, `SELECT status,model_turns FROM runs WHERE id=?`, runID).Scan(&status, &turns); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return chat.Run{}, fmt.Errorf("run not found")
			}
			return chat.Run{}, err
		}
		if status != chat.RunRunning {
			return chat.Run{}, fmt.Errorf("run is not running")
		}
		return chat.Run{}, chat.ErrModelTurnBudgetExceeded
	}
	value, err := r.Get(ctx, runID)
	if err != nil {
		return chat.Run{}, fmt.Errorf("model turn consumed but checkpoint could not be loaded: %w", err)
	}
	return value, nil
}

func (r *RunRepository) Update(ctx context.Context, value chat.Run) error {
	if !value.APIProtocol.Valid() {
		value.APIProtocol = modelprofile.ProtocolOpenAIChat
	}
	result, err := r.db.ExecContext(ctx, `UPDATE runs SET assistant_message_id=?, api_protocol=?, resolved_reasoning_level=?, context_compacted=?, status=?, error_code=?, error_message=?, input_tokens=?, fresh_input_tokens=?, output_tokens=?, cached_input_tokens=?, cache_write_tokens=?, cache_reported_turns=?, cache_reported_fresh_input_tokens=?, cache_hit_turns=?, finish_reason=?, started_at=?, completed_at=?, updated_at=? WHERE id=? AND status NOT IN ('completed','failed','cancelled','interrupted')`,
		nullableString(value.AssistantMessageID), value.APIProtocol, value.ResolvedReasoningLevel, value.ContextCompacted, value.Status, value.ErrorCode, value.ErrorMessage, value.InputTokens, value.FreshInputTokens, value.OutputTokens, value.CachedInputTokens, value.CacheWriteTokens, value.CacheReportedTurns, value.CacheReportedFreshInputTokens, value.CacheHitTurns, value.FinishReason, nullableTime(value.StartedAt), nullableTime(value.CompletedAt), formatTime(value.UpdatedAt), value.ID)
	if err != nil {
		return fmt.Errorf("update run: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		var exists int
		if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE id=?`, value.ID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("run not found")
		}
		return fmt.Errorf("run is terminal")
	}
	return nil
}

func (r *RunRepository) ProjectIDForRun(ctx context.Context, runID string) (string, error) {
	var projectID string
	if err := r.db.QueryRowContext(ctx, `SELECT c.project_id FROM runs r JOIN conversations c ON c.id=r.conversation_id WHERE r.id=?`, runID).Scan(&projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("run not found")
		}
		return "", fmt.Errorf("resolve run project: %w", err)
	}
	return projectID, nil
}

func (r *RunRepository) ListToolCallIDs(ctx context.Context, runID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM tool_calls WHERE run_id=? AND status IN ('pending','awaiting_approval','running') ORDER BY created_at,id`, runID)
	if err != nil {
		return nil, fmt.Errorf("list active run tool calls: %w", err)
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *RunRepository) TransitionStatus(ctx context.Context, runID string, expected, next chat.RunStatus, at time.Time) error {
	if !canTransitionRunForApproval(expected, next) {
		return fmt.Errorf("invalid approval run transition: %s to %s", expected, next)
	}
	result, err := r.db.ExecContext(ctx, `UPDATE runs SET status=?, updated_at=? WHERE id=? AND status=?`, next, formatTime(at), runID, expected)
	if err != nil {
		return fmt.Errorf("transition run status: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("run status transition conflict")
	}
	return nil
}

func canTransitionRunForApproval(from, to chat.RunStatus) bool {
	return (from == chat.RunRunning && to == chat.RunWaitingApproval) || (from == chat.RunWaitingApproval && to == chat.RunRunning)
}

func (r *RunRepository) CancelRun(ctx context.Context, runID, errorCode, errorMessage string, at time.Time, event events.Envelope) (chat.Run, bool, error) {
	return r.terminateRun(ctx, runID, chat.RunCancelled, errorCode, errorMessage, at, event)
}

func (r *RunRepository) FailRun(ctx context.Context, runID, errorCode, errorMessage string, at time.Time, event events.Envelope) (chat.Run, bool, error) {
	return r.terminateRun(ctx, runID, chat.RunFailed, errorCode, errorMessage, at, event)
}

func (r *RunRepository) terminateRun(ctx context.Context, runID string, next chat.RunStatus, errorCode, errorMessage string, at time.Time, event events.Envelope) (chat.Run, bool, error) {
	if next != chat.RunCancelled && next != chat.RunFailed {
		return chat.Run{}, false, fmt.Errorf("invalid run termination status %q", next)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return chat.Run{}, false, fmt.Errorf("begin cancel run: %w", err)
	}
	defer tx.Rollback()
	var status chat.RunStatus
	if err := tx.QueryRowContext(ctx, `SELECT status FROM runs WHERE id=?`, runID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return chat.Run{}, false, fmt.Errorf("run not found")
		}
		return chat.Run{}, false, err
	}
	if isTerminalRunStatus(status) {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			return chat.Run{}, false, err
		}
		value, err := r.Get(ctx, runID)
		return value, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE approvals SET status='expired', resolved_scope='call', resolved_at=? WHERE run_id=? AND status='pending'`, formatTime(at), runID); err != nil {
		return chat.Run{}, false, fmt.Errorf("expire run approvals: %w", err)
	}
	var affectedCalls []string
	callRows, err := tx.QueryContext(ctx, `SELECT id FROM tool_calls WHERE run_id=? AND status IN ('pending','awaiting_approval','running') ORDER BY created_at,id`, runID)
	if err != nil {
		return chat.Run{}, false, fmt.Errorf("list terminating tool calls: %w", err)
	}
	for callRows.Next() {
		var callID string
		if err := callRows.Scan(&callID); err != nil {
			_ = callRows.Close()
			return chat.Run{}, false, err
		}
		affectedCalls = append(affectedCalls, callID)
	}
	if err := callRows.Err(); err != nil {
		_ = callRows.Close()
		return chat.Run{}, false, err
	}
	if err := callRows.Close(); err != nil {
		return chat.Run{}, false, err
	}
	toolStatus, toolCode := "interrupted", "RUN_FAILED"
	if next == chat.RunCancelled {
		toolStatus, toolCode = "cancelled", "TOOL_CANCELLED"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status=?, error_code=?, error_message=?, completed_at=?, updated_at=? WHERE run_id=? AND status IN ('pending','awaiting_approval','running')`, toolStatus, toolCode, errorMessage, formatTime(at), formatTime(at), runID); err != nil {
		return chat.Run{}, false, fmt.Errorf("cancel run tool calls: %w", err)
	}
	resultStatus := "cancelled"
	if next == chat.RunFailed {
		resultStatus = "error"
	}
	for _, callID := range affectedCalls {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tool_results(tool_call_id,status,text_content,artifacts_json,citations_json,truncated,meta_json,created_at) VALUES (?,?,?,'[]','[]',0,'{}',?) ON CONFLICT(tool_call_id) DO NOTHING`, callID, resultStatus, errorMessage, formatTime(at)); err != nil {
			return chat.Run{}, false, fmt.Errorf("record terminated tool result: %w", err)
		}
	}
	messageStatus := "incomplete"
	if next == chat.RunFailed {
		var hasText int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM message_parts WHERE message_id=(SELECT assistant_message_id FROM runs WHERE id=?) AND part_type='text' AND text_content<>'')`, runID).Scan(&hasText); err != nil {
			return chat.Run{}, false, fmt.Errorf("inspect assistant message: %w", err)
		}
		if hasText == 0 {
			messageStatus = "failed"
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET status=?, updated_at=? WHERE id=(SELECT assistant_message_id FROM runs WHERE id=?)`, messageStatus, formatTime(at), runID); err != nil {
		return chat.Run{}, false, fmt.Errorf("cancel assistant message: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET status=?, error_code=?, error_message=?, completed_at=?, updated_at=? WHERE id=? AND status IN ('queued','running','waiting_approval')`, next, errorCode, errorMessage, formatTime(at), formatTime(at), runID)
	if err != nil {
		return chat.Run{}, false, fmt.Errorf("cancel run: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return chat.Run{}, false, fmt.Errorf("run cancellation conflict")
	}
	if err := appendNextEventTx(ctx, tx, &event); err != nil {
		return chat.Run{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return chat.Run{}, false, err
	}
	value, err := r.Get(ctx, runID)
	return value, true, err
}

func isTerminalRunStatus(status chat.RunStatus) bool {
	return status == chat.RunCompleted || status == chat.RunFailed || status == chat.RunCancelled || status == chat.RunInterrupted
}

func (r *RunRepository) InterruptActive(ctx context.Context, at time.Time) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin interrupt active runs: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET status='incomplete', updated_at=? WHERE id IN (SELECT assistant_message_id FROM runs WHERE status IN ('queued', 'running', 'waiting_approval'))`, formatTime(at)); err != nil {
		return 0, fmt.Errorf("interrupt assistant messages: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET status='interrupted', error_code='APP_RESTARTED', error_message='应用退出时运行尚未完成', completed_at=?, updated_at=? WHERE status IN ('queued', 'running', 'waiting_approval')`, formatTime(at), formatTime(at))
	if err != nil {
		return 0, fmt.Errorf("interrupt active runs: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return affected, nil
}

func (r *RunRepository) Append(ctx context.Context, event events.Envelope) error {
	payload := string(event.Payload)
	_, err := r.db.ExecContext(ctx, `INSERT INTO run_events(event_id, version, aggregate_id, aggregate_type, sequence, event_type, timestamp, payload_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		event.EventID, event.Version, event.AggregateID, event.AggregateType, event.Sequence, event.Type, formatTime(event.Timestamp), payload)
	if err != nil {
		return fmt.Errorf("append run event: %w", err)
	}
	return nil
}

func (r *RunRepository) AppendNext(ctx context.Context, event events.Envelope) (events.Envelope, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return event, fmt.Errorf("begin run event append: %w", err)
	}
	defer tx.Rollback()
	if err := appendNextEventTx(ctx, tx, &event); err != nil {
		return event, err
	}
	if err := tx.Commit(); err != nil {
		return event, err
	}
	return event, nil
}

func (r *RunRepository) UsageDashboard(ctx context.Context, query chat.UsageQuery) (chat.UsageDashboard, error) {
	result := chat.UsageDashboard{Query: query, Daily: []chat.DailyUsage{}, Models: []chat.ModelUsage{}}
	where, args := usageWhere(query, "r")

	summaryQuery := `SELECT COUNT(*), COALESCE(SUM(r.model_turns),0),
		COALESCE(SUM(r.fresh_input_tokens),0), COALESCE(SUM(r.output_tokens),0),
		COALESCE(SUM(r.cached_input_tokens),0), COALESCE(SUM(r.cache_write_tokens),0),
		COALESCE(SUM(r.cache_reported_turns),0), COALESCE(SUM(r.cache_hit_turns),0),
		COALESCE(SUM(r.cache_reported_fresh_input_tokens),0)
		FROM runs r` + where
	if err := scanUsageSummary(r.db.QueryRowContext(ctx, summaryQuery, args...), &result.Summary); err != nil {
		return result, fmt.Errorf("read global usage summary: %w", err)
	}

	dailyRows, err := r.db.QueryContext(ctx, `SELECT substr(datetime(r.created_at, 'localtime'),1,10), COUNT(*),
		COALESCE(SUM(r.model_turns),0), COALESCE(SUM(r.fresh_input_tokens),0),
		COALESCE(SUM(r.output_tokens),0), COALESCE(SUM(r.cached_input_tokens),0),
		COALESCE(SUM(r.cache_write_tokens),0), COALESCE(SUM(r.cache_reported_turns),0),
		COALESCE(SUM(r.cache_hit_turns),0), COALESCE(SUM(r.cache_reported_fresh_input_tokens),0)
		FROM runs r`+where+` GROUP BY 1 ORDER BY 1`, args...)
	if err != nil {
		return result, fmt.Errorf("read daily usage: %w", err)
	}
	defer dailyRows.Close()
	for dailyRows.Next() {
		var item chat.DailyUsage
		var reportedFresh int
		if err := dailyRows.Scan(&item.Date, &item.RunCount, &item.ModelTurns, &item.FreshInputTokens,
			&item.OutputTokens, &item.CacheReadTokens, &item.CacheCreationTokens,
			&item.CacheReportedTurns, &item.CacheHitTurns, &reportedFresh); err != nil {
			return result, err
		}
		deriveUsageSummary(&item.UsageSummary, reportedFresh)
		result.Daily = append(result.Daily, item)
	}
	if err := dailyRows.Err(); err != nil {
		return result, err
	}

	modelRows, err := r.db.QueryContext(ctx, `SELECT r.model_profile_id, COALESCE(p.name,''), r.model_id,
		COUNT(*), COALESCE(SUM(r.model_turns),0), COALESCE(SUM(r.fresh_input_tokens),0),
		COALESCE(SUM(r.output_tokens),0), COALESCE(SUM(r.cached_input_tokens),0),
		COALESCE(SUM(r.cache_write_tokens),0), COALESCE(SUM(r.cache_reported_turns),0),
		COALESCE(SUM(r.cache_hit_turns),0), COALESCE(SUM(r.cache_reported_fresh_input_tokens),0)
		FROM runs r LEFT JOIN model_profiles p ON p.id=r.model_profile_id`+where+`
		GROUP BY r.model_profile_id, p.name, r.model_id
		ORDER BY (COALESCE(SUM(r.fresh_input_tokens),0)+COALESCE(SUM(r.output_tokens),0)+COALESCE(SUM(r.cached_input_tokens),0)+COALESCE(SUM(r.cache_write_tokens),0)) DESC, r.model_id`, args...)
	if err != nil {
		return result, fmt.Errorf("read model usage: %w", err)
	}
	defer modelRows.Close()
	for modelRows.Next() {
		var item chat.ModelUsage
		var reportedFresh int
		if err := modelRows.Scan(&item.ModelProfileID, &item.ProfileName, &item.ModelID,
			&item.RunCount, &item.ModelTurns, &item.FreshInputTokens, &item.OutputTokens,
			&item.CacheReadTokens, &item.CacheCreationTokens, &item.CacheReportedTurns,
			&item.CacheHitTurns, &reportedFresh); err != nil {
			return result, err
		}
		deriveUsageSummary(&item.UsageSummary, reportedFresh)
		result.Models = append(result.Models, item)
	}
	return result, modelRows.Err()
}

func usageWhere(query chat.UsageQuery, alias string) (string, []any) {
	prefix := alias + "."
	conditions := []string{"1=1"}
	args := []any{}
	if query.StartDate != "" {
		conditions = append(conditions, "date("+prefix+"created_at, 'localtime') >= date(?)")
		args = append(args, query.StartDate)
	}
	if query.EndDate != "" {
		conditions = append(conditions, "date("+prefix+"created_at, 'localtime') <= date(?)")
		args = append(args, query.EndDate)
	}
	if query.ModelProfileID != "" {
		conditions = append(conditions, prefix+"model_profile_id = ?")
		args = append(args, query.ModelProfileID)
	}
	if query.ModelID != "" {
		conditions = append(conditions, prefix+"model_id = ?")
		args = append(args, query.ModelID)
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

func scanUsageSummary(row rowScanner, target *chat.UsageSummary) error {
	var reportedFresh int
	if err := row.Scan(&target.RunCount, &target.ModelTurns, &target.FreshInputTokens,
		&target.OutputTokens, &target.CacheReadTokens, &target.CacheCreationTokens,
		&target.CacheReportedTurns, &target.CacheHitTurns, &reportedFresh); err != nil {
		return err
	}
	deriveUsageSummary(target, reportedFresh)
	return nil
}

func deriveUsageSummary(target *chat.UsageSummary, reportedFresh int) {
	target.RealTotalTokens = target.FreshInputTokens + target.OutputTokens + target.CacheReadTokens + target.CacheCreationTokens
	target.CacheDataAvailable = target.CacheReportedTurns > 0
	cacheableInput := reportedFresh + target.CacheReadTokens + target.CacheCreationTokens
	if target.CacheDataAvailable && cacheableInput > 0 {
		target.CacheHitRate = float64(target.CacheReadTokens) / float64(cacheableInput)
	}
}

func appendNextEventTx(ctx context.Context, tx *sql.Tx, event *events.Envelope) error {
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM run_events WHERE aggregate_id = ?`, event.AggregateID).Scan(&event.Sequence); err != nil {
		return fmt.Errorf("read next run event sequence: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_events(event_id, version, aggregate_id, aggregate_type, sequence, event_type, timestamp, payload_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		event.EventID, event.Version, event.AggregateID, event.AggregateType, event.Sequence, event.Type, formatTime(event.Timestamp), string(event.Payload)); err != nil {
		return fmt.Errorf("append run event: %w", err)
	}
	return nil
}

type sqlExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertMessage(ctx context.Context, tx sqlExecer, value conversation.Message) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages(id, conversation_id, run_id, role, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		value.ID, value.ConversationID, nullableString(value.RunID), value.Role, value.Status, formatTime(value.CreatedAt), formatTime(value.UpdatedAt)); err != nil {
		return fmt.Errorf("insert run message: %w", err)
	}
	for _, part := range value.Parts {
		var payload any
		if len(part.Payload) > 0 {
			payload = string(part.Payload)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO message_parts(id, message_id, ordinal, part_type, text_content, payload_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			part.ID, value.ID, part.Ordinal, part.Type, part.Text, payload, formatTime(part.CreatedAt)); err != nil {
			return fmt.Errorf("insert run message part: %w", err)
		}
	}
	return nil
}

func scanRun(row rowScanner) (chat.Run, error) {
	var value chat.Run
	var createdAt, updatedAt string
	var startedAt, completedAt sql.NullString
	if err := row.Scan(&value.ID, &value.ConversationID, &value.UserMessageID, &value.AssistantMessageID, &value.ModelProfileID, &value.ModelID, &value.APIProtocol, &value.RequestedReasoningLevel, &value.ResolvedReasoningLevel, &value.ContextWindowTokens, &value.ContextCompacted, &value.PermissionMode, &value.Status,
		&value.ErrorCode, &value.ErrorMessage, &value.InputTokens, &value.FreshInputTokens, &value.OutputTokens, &value.CachedInputTokens, &value.CacheWriteTokens, &value.CacheReportedTurns, &value.CacheReportedFreshInputTokens, &value.CacheHitTurns, &value.ModelTurns, &value.FinishReason, &createdAt, &startedAt, &completedAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return chat.Run{}, fmt.Errorf("run not found: %w", sql.ErrNoRows)
		}
		return chat.Run{}, err
	}
	var err error
	value.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return value, err
	}
	value.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return value, err
	}
	if startedAt.Valid {
		parsed, err := parseTime(startedAt.String)
		if err != nil {
			return value, err
		}
		value.StartedAt = &parsed
	}
	if completedAt.Valid {
		parsed, err := parseTime(completedAt.String)
		if err != nil {
			return value, err
		}
		value.CompletedAt = &parsed
	}
	return value, nil
}

const runSelect = `SELECT id, conversation_id, user_message_id, COALESCE(assistant_message_id, ''), model_profile_id, model_id, api_protocol, requested_reasoning_level, resolved_reasoning_level, context_window_tokens, context_compacted, permission_mode, status, error_code, error_message, input_tokens, fresh_input_tokens, output_tokens, cached_input_tokens, cache_write_tokens, cache_reported_turns, cache_reported_fresh_input_tokens, cache_hit_turns, model_turns, finish_reason, created_at, started_at, completed_at, updated_at FROM runs`

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

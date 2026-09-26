package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/events"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/modelcap"
)

type RunRepository struct{ db *sql.DB }

func NewRunRepository(db *sql.DB) *RunRepository { return &RunRepository{db: db} }

func (r *RunRepository) ListConversationActivity(ctx context.Context, projectID string) ([]chat.ConversationActivity, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT r.conversation_id,r.status FROM runs r
		JOIN conversations c ON c.id=r.conversation_id
		WHERE c.project_id=? AND r.status IN ('queued','running','waiting_approval')
		AND NOT EXISTS (SELECT 1 FROM workflow_conversations w WHERE w.conversation_id=c.id)`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []chat.ConversationActivity{}
	for rows.Next() {
		var value chat.ConversationActivity
		if err := rows.Scan(&value.ConversationID, &value.Status); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

const (
	maxProviderItemsPerTurn   = 128
	maxProviderItemBytes      = 8 * 1024 * 1024
	maxProviderTurnBytes      = 16 * 1024 * 1024
	maxProviderItemTypeBytes  = 128
	maxProviderItemIDBytes    = 512
	maxProviderItemPhaseBytes = 64
	maxRunStepCommentaryRunes = 100_000
	maxRunStepSummaryRunes    = 4_000
	maxModelTurnDraftRunes    = 200_000
	maxTerminationActivities  = 8
	maxTerminationTools       = 12
	maxTerminationTextRunes   = 2_000
)

type runTerminationContext struct {
	Kind       string                   `json:"kind"`
	Status     chat.RunStatus           `json:"status"`
	Message    string                   `json:"message"`
	Draft      string                   `json:"draft,omitempty"`
	Activities []runTerminationActivity `json:"activities,omitempty"`
	Tools      []runTerminationTool     `json:"tools,omitempty"`
}

func (r *RunRepository) BeginModelTurn(ctx context.Context, runID string, turnIndex int, at time.Time) error {
	runID = strings.TrimSpace(runID)
	if runID == "" || turnIndex <= 0 || at.IsZero() {
		return fmt.Errorf("invalid model turn journal identity")
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO model_turn_journal(run_id,turn_index,status,draft_text,finish_reason,provider_item_count,started_at,completed_at,updated_at) VALUES (?,?,'streaming','','',0,?,NULL,?)`,
		runID, turnIndex, formatTime(at), formatTime(at))
	if err != nil {
		return fmt.Errorf("begin model turn journal: %w", err)
	}
	return nil
}

func (r *RunRepository) UpdateModelTurnDraft(ctx context.Context, runID string, turnIndex int, draft string, providerItemCount int, at time.Time) error {
	if len([]rune(draft)) > maxModelTurnDraftRunes || providerItemCount < 0 || providerItemCount > maxProviderItemsPerTurn {
		return fmt.Errorf("invalid model turn draft")
	}
	result, err := r.db.ExecContext(ctx, `UPDATE model_turn_journal SET draft_text=?,provider_item_count=?,updated_at=? WHERE run_id=? AND turn_index=? AND status='streaming'`,
		draft, providerItemCount, formatTime(at), strings.TrimSpace(runID), turnIndex)
	if err != nil {
		return fmt.Errorf("update model turn draft: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 1 {
		return nil
	}
	var status chat.ModelTurnStatus
	if err := r.db.QueryRowContext(ctx, `SELECT status FROM model_turn_journal WHERE run_id=? AND turn_index=?`, strings.TrimSpace(runID), turnIndex).Scan(&status); err != nil {
		return fmt.Errorf("model turn journal not found: %w", err)
	}
	if status == chat.ModelTurnStreaming {
		return fmt.Errorf("model turn draft was not updated")
	}
	return nil
}

func (r *RunRepository) LatestModelTurnJournal(ctx context.Context, runID string) (chat.ModelTurnJournal, bool, error) {
	var value chat.ModelTurnJournal
	var completedAt sql.NullString
	var startedAt, updatedAt string
	var status chat.ModelTurnStatus
	err := r.db.QueryRowContext(ctx, `SELECT run_id,turn_index,status,draft_text,finish_reason,provider_item_count,started_at,completed_at,updated_at FROM model_turn_journal WHERE run_id=? ORDER BY turn_index DESC LIMIT 1`, strings.TrimSpace(runID)).Scan(
		&value.RunID, &value.TurnIndex, &status, &value.DraftText, &value.FinishReason, &value.ProviderItemCount, &startedAt, &completedAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return chat.ModelTurnJournal{}, false, nil
	}
	if err != nil {
		return chat.ModelTurnJournal{}, false, fmt.Errorf("read latest model turn journal: %w", err)
	}
	value.Status = status
	var parseErr error
	value.StartedAt, parseErr = parseTime(startedAt)
	if parseErr != nil {
		return chat.ModelTurnJournal{}, false, parseErr
	}
	value.UpdatedAt, parseErr = parseTime(updatedAt)
	if parseErr != nil {
		return chat.ModelTurnJournal{}, false, parseErr
	}
	if completedAt.Valid && completedAt.String != "" {
		completed, parseErr := parseTime(completedAt.String)
		if parseErr != nil {
			return chat.ModelTurnJournal{}, false, parseErr
		}
		value.CompletedAt = &completed
	}
	return value, true, nil
}

func (r *RunRepository) FinishModelTurn(ctx context.Context, runID string, turnIndex int, status chat.ModelTurnStatus, finishReason string, providerItemCount int, at time.Time) error {
	if status != chat.ModelTurnCompleted && status != chat.ModelTurnInterrupted && status != chat.ModelTurnFailed {
		return fmt.Errorf("invalid model turn terminal status")
	}
	if providerItemCount < 0 || providerItemCount > maxProviderItemsPerTurn {
		return fmt.Errorf("invalid model turn provider item count")
	}
	result, err := r.db.ExecContext(ctx, `UPDATE model_turn_journal SET status=?,finish_reason=?,provider_item_count=MAX(provider_item_count,?),completed_at=?,updated_at=? WHERE run_id=? AND turn_index=? AND status='streaming'`,
		status, strings.TrimSpace(finishReason), providerItemCount, formatTime(at), formatTime(at), strings.TrimSpace(runID), turnIndex)
	if err != nil {
		return fmt.Errorf("finish model turn journal: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 1 {
		return nil
	}
	var existing chat.ModelTurnStatus
	if err := r.db.QueryRowContext(ctx, `SELECT status FROM model_turn_journal WHERE run_id=? AND turn_index=?`, strings.TrimSpace(runID), turnIndex).Scan(&existing); err != nil {
		return fmt.Errorf("model turn journal not found: %w", err)
	}
	if existing == status || existing == chat.ModelTurnInterrupted || existing == chat.ModelTurnFailed {
		return nil
	}
	return fmt.Errorf("model turn journal already ended as %s", existing)
}

type runTerminationActivity struct {
	TurnIndex        int    `json:"turnIndex"`
	Commentary       string `json:"commentary,omitempty"`
	ReasoningSummary string `json:"reasoningSummary,omitempty"`
}

type runTerminationTool struct {
	Name         string `json:"name"`
	Arguments    string `json:"arguments"`
	Status       string `json:"status"`
	ErrorCode    string `json:"errorCode,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
	Result       string `json:"result,omitempty"`
}

// SaveRunStep stores only completed non-final model turns. The primary key
// makes approval resume and event replay idempotent, while the equality check
// prevents an already-audited step from being silently rewritten.
func (r *RunRepository) SaveRunStep(ctx context.Context, step chat.RunStep) error {
	step.RunID = strings.TrimSpace(step.RunID)
	step.Commentary = strings.TrimSpace(step.Commentary)
	step.ReasoningSummary = strings.TrimSpace(step.ReasoningSummary)
	if step.RunID == "" || step.TurnIndex <= 0 || step.CompletedAt.IsZero() ||
		len([]rune(step.Commentary)) > maxRunStepCommentaryRunes || len([]rune(step.ReasoningSummary)) > maxRunStepSummaryRunes {
		return fmt.Errorf("invalid run step")
	}
	if step.Commentary == "" && step.ReasoningSummary == "" && !step.ReasoningObserved {
		return nil
	}
	if step.CreatedAt.IsZero() {
		step.CreatedAt = step.CompletedAt
	}
	result, err := r.db.ExecContext(ctx, `INSERT INTO run_steps(run_id,turn_index,commentary_text,reasoning_summary,reasoning_observed,reasoning_signature_observed,created_at,completed_at) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(run_id,turn_index) DO NOTHING`,
		step.RunID, step.TurnIndex, step.Commentary, step.ReasoningSummary, step.ReasoningObserved, step.ReasoningSignatureObserved, formatTime(step.CreatedAt), formatTime(step.CompletedAt))
	if err != nil {
		return fmt.Errorf("insert run step: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 1 {
		return nil
	}
	var existing chat.RunStep
	var createdAt, completedAt string
	if err := r.db.QueryRowContext(ctx, `SELECT run_id,turn_index,commentary_text,reasoning_summary,reasoning_observed,reasoning_signature_observed,created_at,completed_at FROM run_steps WHERE run_id=? AND turn_index=?`, step.RunID, step.TurnIndex).Scan(
		&existing.RunID, &existing.TurnIndex, &existing.Commentary, &existing.ReasoningSummary, &existing.ReasoningObserved, &existing.ReasoningSignatureObserved, &createdAt, &completedAt); err != nil {
		return fmt.Errorf("read existing run step: %w", err)
	}
	existing.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return err
	}
	existing.CompletedAt, err = parseTime(completedAt)
	if err != nil {
		return err
	}
	if existing.Commentary != step.Commentary || existing.ReasoningSummary != step.ReasoningSummary ||
		existing.ReasoningObserved != step.ReasoningObserved || existing.ReasoningSignatureObserved != step.ReasoningSignatureObserved {
		return fmt.Errorf("run step is immutable and conflicts with persisted state")
	}
	return nil
}

func (r *RunRepository) ListRunSteps(ctx context.Context, runID string) ([]chat.RunStep, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT run_id,turn_index,commentary_text,reasoning_summary,reasoning_observed,reasoning_signature_observed,created_at,completed_at FROM run_steps WHERE run_id=? ORDER BY turn_index`, strings.TrimSpace(runID))
	if err != nil {
		return nil, fmt.Errorf("list run steps: %w", err)
	}
	defer rows.Close()
	steps := make([]chat.RunStep, 0)
	for rows.Next() {
		var value chat.RunStep
		var createdAt, completedAt string
		if err := rows.Scan(&value.RunID, &value.TurnIndex, &value.Commentary, &value.ReasoningSummary, &value.ReasoningObserved, &value.ReasoningSignatureObserved, &createdAt, &completedAt); err != nil {
			return nil, err
		}
		value.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		value.CompletedAt, err = parseTime(completedAt)
		if err != nil {
			return nil, err
		}
		steps = append(steps, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list run steps: %w", err)
	}
	return steps, nil
}

// SaveProviderTurn persists completed provider-native content items without
// exposing them through the conversation repository. Existing items are
// immutable: an idempotent retry may write the same value, but never replace a
// signature or encrypted reasoning payload already used by a tool turn.
func (r *RunRepository) SaveProviderTurn(ctx context.Context, runID string, turn model.ProviderTurn, at time.Time) error {
	runID = strings.TrimSpace(runID)
	if runID == "" || turn.TurnIndex <= 0 || !turn.Protocol.Valid() {
		return fmt.Errorf("invalid provider turn identity")
	}
	if len(turn.Items) == 0 || len(turn.Items) > maxProviderItemsPerTurn {
		return fmt.Errorf("invalid provider item count")
	}
	seen := make(map[int]struct{}, len(turn.Items))
	totalBytes := 0
	for _, item := range turn.Items {
		if item.Ordinal < 0 || len(item.Type) == 0 || len(item.Type) > maxProviderItemTypeBytes || strings.TrimSpace(item.Type) != item.Type {
			return fmt.Errorf("invalid provider item metadata")
		}
		if len(item.ItemID) > maxProviderItemIDBytes || strings.TrimSpace(item.ItemID) != item.ItemID ||
			len(item.Phase) > maxProviderItemPhaseBytes || strings.TrimSpace(item.Phase) != item.Phase {
			return fmt.Errorf("invalid provider item identity or phase")
		}
		if _, exists := seen[item.Ordinal]; exists {
			return fmt.Errorf("duplicate provider item ordinal")
		}
		seen[item.Ordinal] = struct{}{}
		if len(item.Payload) == 0 || len(item.Payload) > maxProviderItemBytes || !json.Valid(item.Payload) || item.Payload[0] != '{' {
			return fmt.Errorf("invalid provider item payload")
		}
		totalBytes += len(item.Payload)
		if totalBytes > maxProviderTurnBytes {
			return fmt.Errorf("provider turn payload exceeds limit")
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin provider turn save: %w", err)
	}
	defer tx.Rollback()
	var runProtocol modelcap.APIProtocol
	if err := tx.QueryRowContext(ctx, `SELECT api_protocol FROM runs WHERE id=?`, runID).Scan(&runProtocol); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("run not found")
		}
		return fmt.Errorf("read run provider protocol: %w", err)
	}
	if runProtocol != turn.Protocol {
		return fmt.Errorf("provider turn protocol does not match run")
	}
	createdAt := formatTime(at)
	for _, item := range turn.Items {
		result, err := tx.ExecContext(ctx, `INSERT INTO provider_turn_items(run_id,turn_index,api_protocol,item_ordinal,provider_item_id,item_type,phase,provider_call_id,payload_json,created_at) VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT(run_id,turn_index,item_ordinal) DO NOTHING`,
			runID, turn.TurnIndex, turn.Protocol, item.Ordinal, item.ItemID, item.Type, item.Phase, item.CallID, string(item.Payload), createdAt)
		if err != nil {
			return fmt.Errorf("insert provider item: %w", err)
		}
		if affected, _ := result.RowsAffected(); affected == 1 {
			continue
		}
		var protocol modelcap.APIProtocol
		var itemID, itemType, phase, callID, payload string
		if err := tx.QueryRowContext(ctx, `SELECT api_protocol,provider_item_id,item_type,phase,provider_call_id,payload_json FROM provider_turn_items WHERE run_id=? AND turn_index=? AND item_ordinal=?`, runID, turn.TurnIndex, item.Ordinal).Scan(&protocol, &itemID, &itemType, &phase, &callID, &payload); err != nil {
			return fmt.Errorf("read existing provider item: %w", err)
		}
		if protocol != turn.Protocol || itemID != item.ItemID || itemType != item.Type || phase != item.Phase || callID != item.CallID || !bytes.Equal([]byte(payload), item.Payload) {
			return fmt.Errorf("provider item is immutable and conflicts with persisted state")
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit provider turn: %w", err)
	}
	return nil
}

func (r *RunRepository) ListProviderTurns(ctx context.Context, runID string) ([]model.ProviderTurn, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT turn_index,api_protocol,item_ordinal,provider_item_id,item_type,phase,provider_call_id,payload_json FROM provider_turn_items WHERE run_id=? ORDER BY turn_index,item_ordinal`, strings.TrimSpace(runID))
	if err != nil {
		return nil, fmt.Errorf("list provider turns: %w", err)
	}
	defer rows.Close()
	turns := make([]model.ProviderTurn, 0)
	for rows.Next() {
		var turnIndex, ordinal int
		var protocol modelcap.APIProtocol
		var itemID, itemType, phase, callID, payload string
		if err := rows.Scan(&turnIndex, &protocol, &ordinal, &itemID, &itemType, &phase, &callID, &payload); err != nil {
			return nil, err
		}
		if len(turns) == 0 || turns[len(turns)-1].TurnIndex != turnIndex {
			turns = append(turns, model.ProviderTurn{TurnIndex: turnIndex, Protocol: protocol, Items: []model.ProviderItem{}})
		}
		turn := &turns[len(turns)-1]
		if turn.Protocol != protocol {
			return nil, fmt.Errorf("provider turn contains mixed protocols")
		}
		turn.Items = append(turn.Items, model.ProviderItem{Ordinal: ordinal, ItemID: itemID, Type: itemType, Phase: phase, CallID: callID, Payload: json.RawMessage(payload)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list provider turns: %w", err)
	}
	return turns, nil
}

func (r *RunRepository) CreateWithMessages(ctx context.Context, value chat.Run, userMessage, assistantMessage conversation.Message) error {
	return r.createWithMessages(ctx, value, userMessage, assistantMessage, nil)
}

func (r *RunRepository) CreateWorkflowAIWithMessages(ctx context.Context, value chat.Run, userMessage, assistantMessage conversation.Message, execution chat.WorkflowAIExecution) error {
	return r.createWithMessages(ctx, value, userMessage, assistantMessage, &execution)
}

func (r *RunRepository) createWithMessages(ctx context.Context, value chat.Run, userMessage, assistantMessage conversation.Message, workflowAI *chat.WorkflowAIExecution) error {
	if !value.PermissionMode.Valid() {
		value.PermissionMode = conversation.PermissionPlan
	}
	if !value.RequestedReasoningLevel.Valid() {
		value.RequestedReasoningLevel = modelcap.DefaultReasoningLevel
	}
	contextBudget := modelcap.ResolveContextBudget(value.ContextWindowTokens, value.AutoCompactTokenLimit, value.ContextWindowSource)
	value.ContextWindowTokens = contextBudget.WindowTokens
	if value.ContextBudgetTokens <= 0 || value.ContextBudgetTokens > contextBudget.EffectiveTokens {
		value.ContextBudgetTokens = contextBudget.EffectiveTokens
	}
	value.AutoCompactTokenLimit = min(contextBudget.AutoCompactTokens, value.ContextBudgetTokens)
	value.ContextWindowSource = contextBudget.Source
	if !value.APIProtocol.Valid() {
		value.APIProtocol = modelprofile.ProtocolOpenAIChat
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin run: %w", err)
	}
	defer tx.Rollback()
	if workflowAI == nil {
		blocked, err := blocksOrdinaryWorkflowChat(ctx, tx, value.ConversationID)
		if err != nil {
			return err
		}
		if blocked {
			return fmt.Errorf("科研任务已恢复执行，请等待当前阶段完成后再对话")
		}
	}
	if err := insertMessage(ctx, tx, userMessage); err != nil {
		return err
	}
	if err := insertMessage(ctx, tx, assistantMessage); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runs(id, conversation_id, user_message_id, assistant_message_id, model_profile_id, model_id, api_protocol, requested_reasoning_level, resolved_reasoning_level, context_window_tokens, context_budget_tokens, auto_compact_token_limit, context_window_source, context_compacted, permission_mode, status, error_code, error_message, error_details, input_tokens, fresh_input_tokens, output_tokens, reasoning_tokens, reasoning_observed, reasoning_signature_observed, reasoning_summary, cached_input_tokens, cache_write_tokens, cache_reported_turns, cache_reported_fresh_input_tokens, cache_hit_turns, model_turns, finish_reason, created_at, started_at, completed_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		value.ID, value.ConversationID, value.UserMessageID, nullableString(value.AssistantMessageID), value.ModelProfileID, value.ModelID, value.APIProtocol, value.RequestedReasoningLevel, value.ResolvedReasoningLevel, value.ContextWindowTokens, value.ContextBudgetTokens, value.AutoCompactTokenLimit, value.ContextWindowSource, value.ContextCompacted, value.PermissionMode, value.Status, value.ErrorCode, value.ErrorMessage, value.ErrorDetails, value.InputTokens, value.FreshInputTokens, value.OutputTokens, value.ReasoningTokens, value.ReasoningObserved, value.ReasoningSignatureObserved, value.ReasoningSummary, value.CachedInputTokens, value.CacheWriteTokens, value.CacheReportedTurns, value.CacheReportedFreshInputTokens, value.CacheHitTurns, value.ModelTurns, value.FinishReason, formatTime(value.CreatedAt), nullableTime(value.StartedAt), nullableTime(value.CompletedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET web_search_disabled=? WHERE id=?`, value.WebSearchDisabled, value.ID); err != nil {
		return fmt.Errorf("save web search policy: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE conversations SET updated_at=? WHERE id=?`, formatTime(value.UpdatedAt), value.ConversationID); err != nil {
		return err
	}
	// Persist naming in the same transaction as the first accepted message/Run.
	// Workflow prompts and task follow-ups must never rename research conversations.
	if workflowAI == nil {
		if title := conversation.TitleFromMessage(userMessage); title != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE conversations SET title=?,auto_title_pending=0
				WHERE id=? AND auto_title_pending=1
				AND NOT EXISTS (SELECT 1 FROM workflow_conversations WHERE conversation_id=?)`, title, value.ConversationID, value.ConversationID); err != nil {
				return fmt.Errorf("save conversation title: %w", err)
			}
		}
	}
	if workflowAI != nil {
		if len(workflowAI.PromptText) == 0 || len(workflowAI.PromptText) > 262144 || len(workflowAI.PromptSHA256) != 64 || len(workflowAI.InputSHA256) != 64 || len(workflowAI.OutputSchemaSHA256) != 64 || !json.Valid(workflowAI.AllowedTools) || !json.Valid(workflowAI.OutputSchema) {
			return fmt.Errorf("invalid Workflow AI execution snapshot")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO workflow_ai_executions(
			id,workflow_run_id,workflow_step_id,attempt,node_kind,model_profile_id,model_id,reasoning_level,
			prompt_version,prompt_text,prompt_sha256,input_sha256,allowed_tools_json,output_schema_json,output_schema_sha256,
			status,created_at,started_at,updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'running',?,?,?)`, workflowAI.ID, workflowAI.WorkflowRunID, workflowAI.WorkflowStepID, workflowAI.Attempt, workflowAI.NodeKind,
			value.ModelProfileID, value.ModelID, value.RequestedReasoningLevel, workflowAI.PromptVersion, workflowAI.PromptText, workflowAI.PromptSHA256,
			workflowAI.InputSHA256, string(workflowAI.AllowedTools), string(workflowAI.OutputSchema), workflowAI.OutputSchemaSHA256, formatTime(workflowAI.CreatedAt), formatTime(workflowAI.CreatedAt), formatTime(workflowAI.CreatedAt))
		if err != nil {
			return fmt.Errorf("insert Workflow AI execution: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_ai_chat_runs(execution_id,chat_run_id,created_at) VALUES (?,?,?)`, workflowAI.ID, value.ID, formatTime(workflowAI.CreatedAt)); err != nil {
			return fmt.Errorf("bind Workflow AI Chat Run: %w", err)
		}
		if len(workflowAI.Citations) > 0 {
			if strings.TrimSpace(workflowAI.CitationToolCallID) == "" {
				return fmt.Errorf("Workflow AI citation seed call identity is required")
			}
			seed := workflowCitationSeedResult(workflowAI.Citations, workflowAI.CreatedAt)
			call := tool.Call{ID: workflowAI.CitationToolCallID, RunID: value.ID, SubjectKind: tool.SubjectChatRun, ProviderCallID: "workflow-citation-seed:" + workflowAI.ID, ToolName: citation.WorkflowSeedToolName, ToolVersion: "1", Arguments: json.RawMessage(`{}`), Status: tool.CallPending, Risk: tool.RiskLow, Permissions: []tool.PermissionRequirement{}, Idempotent: true, IdempotencyKey: "workflow-citation-seed:" + workflowAI.ID, CreatedAt: workflowAI.CreatedAt, UpdatedAt: workflowAI.CreatedAt}
			if err := (&ToolRepository{db: r.db}).create(ctx, tx, call); err != nil {
				return fmt.Errorf("create Workflow AI citation seed: %w", err)
			}
			if err := finishToolCall(ctx, tx, call.ID, tool.CallPending, tool.CallCompleted, seed, "", "", workflowAI.CreatedAt); err != nil {
				return fmt.Errorf("finish Workflow AI citation seed: %w", err)
			}
		}
	}
	return tx.Commit()
}

func workflowCitationSeedResult(values []tool.CitationRef, at time.Time) tool.Result {
	var text strings.Builder
	text.WriteString("Host-verified evidence selected earlier in this Workflow follows. Cite only these reissued markers in this Chat Run; source-Run markers shown elsewhere will not validate here.\n\n")
	for _, value := range values {
		fmt.Fprintf(&text, "%s | %s | %s\n%s\n\n", value.Reference, value.SourceName, value.Locator, value.Quote)
	}
	structured, _ := json.Marshal(map[string]any{"source": "workflow_citation_selection", "count": len(values)})
	return tool.Result{Status: tool.ResultSuccess, Text: strings.TrimSpace(text.String()), Structured: structured, Artifacts: []tool.ArtifactRef{}, Citations: append([]tool.CitationRef(nil), values...), CreatedAt: at}
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

// IsWorkflowAIRun identifies host-created Workflow stage requests from their
// durable binding. Prompt text is untrusted and must never be used for this
// decision.
func (r *RunRepository) IsWorkflowAIRun(ctx context.Context, runID string) (bool, error) {
	var found int
	if err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_ai_chat_runs WHERE chat_run_id=?)`, strings.TrimSpace(runID)).Scan(&found); err != nil {
		return false, fmt.Errorf("inspect Workflow AI Chat Run binding: %w", err)
	}
	return found == 1, nil
}

// WorkflowAIContract reads the immutable contract captured when a Workflow
// stage created its Chat Run. It intentionally resolves by the durable Chat
// binding instead of the current Workflow conversation projection: the latter
// can be briefly unavailable while a newly created stage is being projected.
func (r *RunRepository) WorkflowAIContract(ctx context.Context, runID string) (chat.WorkflowAIExecution, bool, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return chat.WorkflowAIExecution{}, false, nil
	}
	var value chat.WorkflowAIExecution
	var allowedTools, outputSchema, createdAt string
	err := r.db.QueryRowContext(ctx, `SELECT execution.id,execution.workflow_run_id,execution.workflow_step_id,execution.attempt,execution.node_kind,execution.prompt_version,execution.prompt_text,execution.prompt_sha256,execution.input_sha256,execution.allowed_tools_json,execution.output_schema_json,execution.output_schema_sha256,execution.created_at
		FROM workflow_ai_chat_runs binding
		JOIN workflow_ai_executions execution ON execution.id=binding.execution_id
		WHERE binding.chat_run_id=?`, runID).Scan(
		&value.ID, &value.WorkflowRunID, &value.WorkflowStepID, &value.Attempt, &value.NodeKind,
		&value.PromptVersion, &value.PromptText, &value.PromptSHA256, &value.InputSHA256,
		&allowedTools, &outputSchema, &value.OutputSchemaSHA256, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return chat.WorkflowAIExecution{}, false, nil
	}
	if err != nil {
		return chat.WorkflowAIExecution{}, false, fmt.Errorf("read Workflow AI contract: %w", err)
	}
	if !json.Valid([]byte(allowedTools)) || !json.Valid([]byte(outputSchema)) {
		return chat.WorkflowAIExecution{}, false, fmt.Errorf("Workflow AI contract contains invalid JSON")
	}
	value.AllowedTools = json.RawMessage(allowedTools)
	value.OutputSchema = json.RawMessage(outputSchema)
	if value.CreatedAt, err = parseTime(createdAt); err != nil {
		return chat.WorkflowAIExecution{}, false, fmt.Errorf("parse Workflow AI contract timestamp: %w", err)
	}
	return value, true, nil
}

// BlocksOrdinaryChatForWorkflowConversation keeps one execution owner while
// a Workflow advances. An Agent Stage review is the only active window where
// a completed stage may be discussed before explicit adoption.
func (r *RunRepository) BlocksOrdinaryChatForWorkflowConversation(ctx context.Context, conversationID string) (bool, error) {
	return blocksOrdinaryWorkflowChat(ctx, r.db, conversationID)
}

func blocksOrdinaryWorkflowChat(ctx context.Context, reader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, conversationID string) (bool, error) {
	var found int
	if err := reader.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM workflow_conversations binding
		JOIN workflow_runs workflow_run ON workflow_run.id=binding.workflow_run_id
		LEFT JOIN workflow_steps current_step ON current_step.workflow_run_id=workflow_run.id
			AND current_step.ordinal=workflow_run.current_step_ordinal
		WHERE binding.conversation_id=?
		AND workflow_run.status NOT IN ('completed','failed','cancelled','interrupted')
		AND NOT (
			workflow_run.status='waiting_human_confirmation'
			AND COALESCE(current_step.node_kind,'')='agent_stage'
			AND COALESCE(current_step.status,'')='waiting_human_confirmation'
		)
	)`, strings.TrimSpace(conversationID)).Scan(&found); err != nil {
		return false, fmt.Errorf("inspect active research Workflow conversation: %w", err)
	}
	return found == 1, nil
}

func (r *RunRepository) IncrementModelTurns(ctx context.Context, runID string, at time.Time) (chat.Run, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE runs SET model_turns=model_turns+1, updated_at=? WHERE id=? AND status='running'`, formatTime(at), runID)
	if err != nil {
		return chat.Run{}, fmt.Errorf("increment model turns: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		var status chat.RunStatus
		if err := r.db.QueryRowContext(ctx, `SELECT status FROM runs WHERE id=?`, runID).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return chat.Run{}, fmt.Errorf("run not found")
			}
			return chat.Run{}, err
		}
		if status != chat.RunRunning {
			return chat.Run{}, fmt.Errorf("run is not running")
		}
		return chat.Run{}, fmt.Errorf("model turn was not recorded")
	}
	value, err := r.Get(ctx, runID)
	if err != nil {
		return chat.Run{}, fmt.Errorf("model turn consumed but checkpoint could not be loaded: %w", err)
	}
	return value, nil
}

// RecordModelUsage durably records one final provider-request outcome. Internal
// network retries reuse the same request ID, while an image probe, multimodal
// fallback, and primary request may share a turn index. Only successful
// outcomes contribute token aggregates.
func (r *RunRepository) RecordModelUsage(ctx context.Context, value chat.RequestUsage) (chat.Run, bool, error) {
	value.ID = strings.TrimSpace(value.ID)
	value.RunID = strings.TrimSpace(value.RunID)
	value.RequestKind = strings.TrimSpace(value.RequestKind)
	value.ProfileName = strings.TrimSpace(value.ProfileName)
	value.ErrorCode = strings.TrimSpace(value.ErrorCode)
	value.ErrorMessage = strings.TrimSpace(value.ErrorMessage)
	if value.StatusCode == 0 {
		value.StatusCode = 200
	}
	successful := value.StatusCode >= 200 && value.StatusCode < 300
	if value.ID == "" || value.RunID == "" || value.TurnIndex <= 0 ||
		!writableRequestKind(value.RequestKind) || len([]rune(value.ProfileName)) > 200 ||
		value.StartedAt.IsZero() || value.CompletedAt.IsZero() || value.CompletedAt.Before(value.StartedAt) ||
		value.StatusCode < 100 || value.StatusCode > 599 ||
		value.InputTokens < 0 || value.FreshInputTokens < 0 || value.OutputTokens < 0 ||
		value.ReasoningTokens < 0 || value.CachedInputTokens < 0 || value.CacheWriteTokens < 0 ||
		value.FirstTokenMillis != nil && *value.FirstTokenMillis < 0 ||
		(!successful && (value.InputTokens != 0 || value.FreshInputTokens != 0 || value.OutputTokens != 0 ||
			value.ReasoningTokens != 0 || value.CachedInputTokens != 0 || value.CacheWriteTokens != 0 || value.CacheDetailsReported)) ||
		(successful && (value.ErrorCode != "" || value.ErrorMessage != "")) {
		return chat.Run{}, false, fmt.Errorf("invalid model request usage")
	}
	value.DurationMillis = value.CompletedAt.Sub(value.StartedAt).Milliseconds()
	if value.DurationMillis < 0 {
		value.DurationMillis = 0
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return chat.Run{}, false, fmt.Errorf("begin model request usage: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO model_request_usage(
		id,run_id,turn_index,request_kind,model_profile_id,profile_name,model_id,api_protocol,
		input_tokens,fresh_input_tokens,output_tokens,reasoning_tokens,cached_input_tokens,cache_write_tokens,
		cache_details_reported,status_code,error_code,error_message,first_token_millis,is_streaming,
		started_at,completed_at,duration_millis)
		SELECT ?,r.id,?,?,COALESCE(NULLIF(?,''),r.model_profile_id),?,COALESCE(NULLIF(?,''),r.model_id),COALESCE(NULLIF(?,''),r.api_protocol),?,?,?,?,?,?,?,?,?,?,?,?,?,?,?
		FROM runs r WHERE r.id=?
		ON CONFLICT(id) DO NOTHING`,
		value.ID, value.TurnIndex, value.RequestKind, value.ModelProfileID, value.ProfileName, value.ModelID, value.APIProtocol,
		value.InputTokens, value.FreshInputTokens, value.OutputTokens, value.ReasoningTokens,
		value.CachedInputTokens, value.CacheWriteTokens, value.CacheDetailsReported,
		value.StatusCode, value.ErrorCode, value.ErrorMessage, nullableInt64(value.FirstTokenMillis), value.IsStreaming,
		formatTime(value.StartedAt), formatTime(value.CompletedAt), value.DurationMillis, value.RunID)
	if err != nil {
		return chat.Run{}, false, fmt.Errorf("insert model request usage: %w", err)
	}
	inserted, _ := result.RowsAffected()
	if inserted == 1 && successful {
		cacheReported, cacheHit, reportedFresh := 0, 0, 0
		if value.CacheDetailsReported {
			cacheReported, reportedFresh = 1, value.FreshInputTokens
			if value.CachedInputTokens > 0 {
				cacheHit = 1
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET
			input_tokens=input_tokens+?, fresh_input_tokens=fresh_input_tokens+?, output_tokens=output_tokens+?,
			reasoning_tokens=reasoning_tokens+?, reasoning_observed=CASE WHEN ? > 0 THEN 1 ELSE reasoning_observed END,
			cached_input_tokens=cached_input_tokens+?, cache_write_tokens=cache_write_tokens+?,
			cache_reported_turns=cache_reported_turns+?, cache_reported_fresh_input_tokens=cache_reported_fresh_input_tokens+?,
			cache_hit_turns=cache_hit_turns+?, model_turns=MAX(model_turns,?), updated_at=? WHERE id=?`,
			value.InputTokens, value.FreshInputTokens, value.OutputTokens, value.ReasoningTokens, value.ReasoningTokens,
			value.CachedInputTokens, value.CacheWriteTokens, cacheReported, reportedFresh, cacheHit,
			value.TurnIndex, formatTime(value.CompletedAt), value.RunID); err != nil {
			return chat.Run{}, false, fmt.Errorf("apply model request usage: %w", err)
		}
	} else if inserted == 1 {
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET model_turns=MAX(model_turns,?),updated_at=? WHERE id=?`,
			value.TurnIndex, formatTime(value.CompletedAt), value.RunID); err != nil {
			return chat.Run{}, false, fmt.Errorf("apply failed model request outcome: %w", err)
		}
	} else {
		var existing chat.RequestUsage
		var startedAt, completedAt string
		var firstTokenMillis sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT id,run_id,turn_index,request_kind,input_tokens,fresh_input_tokens,
			output_tokens,reasoning_tokens,cached_input_tokens,cache_write_tokens,cache_details_reported,
			status_code,error_code,error_message,first_token_millis,is_streaming,
			started_at,completed_at,duration_millis FROM model_request_usage WHERE id=?`, value.ID).Scan(&existing.ID, &existing.RunID, &existing.TurnIndex, &existing.RequestKind,
			&existing.InputTokens, &existing.FreshInputTokens, &existing.OutputTokens, &existing.ReasoningTokens,
			&existing.CachedInputTokens, &existing.CacheWriteTokens, &existing.CacheDetailsReported,
			&existing.StatusCode, &existing.ErrorCode, &existing.ErrorMessage, &firstTokenMillis, &existing.IsStreaming,
			&startedAt, &completedAt, &existing.DurationMillis); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return chat.Run{}, false, fmt.Errorf("run not found")
			}
			return chat.Run{}, false, fmt.Errorf("read existing model request usage: %w", err)
		}
		if existing.RunID != value.RunID || existing.TurnIndex != value.TurnIndex || existing.RequestKind != value.RequestKind || existing.InputTokens != value.InputTokens ||
			existing.FreshInputTokens != value.FreshInputTokens || existing.OutputTokens != value.OutputTokens ||
			existing.ReasoningTokens != value.ReasoningTokens || existing.CachedInputTokens != value.CachedInputTokens ||
			existing.CacheWriteTokens != value.CacheWriteTokens || existing.CacheDetailsReported != value.CacheDetailsReported ||
			existing.StatusCode != value.StatusCode || existing.ErrorCode != value.ErrorCode || existing.ErrorMessage != value.ErrorMessage ||
			existing.IsStreaming != value.IsStreaming || !sameNullableInt64(firstTokenMillis, value.FirstTokenMillis) {
			return chat.Run{}, false, fmt.Errorf("model request usage is immutable and conflicts with persisted state")
		}
	}
	run, err := scanRun(tx.QueryRowContext(ctx, runSelect+` WHERE id=?`, value.RunID))
	if err != nil {
		return chat.Run{}, false, fmt.Errorf("load run after model request usage: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return chat.Run{}, false, fmt.Errorf("commit model request usage: %w", err)
	}
	return run, inserted == 1, nil
}

func writableRequestKind(value string) bool {
	switch value {
	case "conversation", "compaction", "image_probe", "multimodal_fallback":
		return true
	default:
		return false
	}
}

func (r *RunRepository) Update(ctx context.Context, value chat.Run) error {
	return updateRun(ctx, r.db, value)
}

// RecordCompaction updates only accounting fields on a terminal run. The run
// state, messages, finish reason and completion timestamp remain immutable.
func (r *RunRepository) RecordCompaction(ctx context.Context, value chat.Run) error {
	switch value.Status {
	case chat.RunCompleted, chat.RunFailed, chat.RunCancelled, chat.RunInterrupted:
	default:
		return fmt.Errorf("compaction accounting requires a terminal run")
	}
	if value.ID == "" || !value.ContextCompacted {
		return fmt.Errorf("invalid compaction accounting")
	}
	result, err := r.db.ExecContext(ctx, `UPDATE runs SET context_compacted=1, input_tokens=?, fresh_input_tokens=?, output_tokens=?, reasoning_tokens=?, reasoning_observed=?, cached_input_tokens=?, cache_write_tokens=?, cache_reported_turns=?, cache_reported_fresh_input_tokens=?, cache_hit_turns=?, model_turns=?, updated_at=? WHERE id=? AND status=?`,
		value.InputTokens, value.FreshInputTokens, value.OutputTokens, value.ReasoningTokens, value.ReasoningObserved, value.CachedInputTokens, value.CacheWriteTokens, value.CacheReportedTurns, value.CacheReportedFreshInputTokens, value.CacheHitTurns, value.ModelTurns, formatTime(value.UpdatedAt), value.ID, value.Status)
	if err != nil {
		return fmt.Errorf("record compaction accounting: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return fmt.Errorf("terminal run changed before compaction accounting was recorded")
	}
	return nil
}

func (r *RunRepository) Complete(ctx context.Context, value chat.Run, text string, citations []conversation.Citation) error {
	if value.ID == "" || value.AssistantMessageID == "" || value.Status != chat.RunCompleted || value.CompletedAt == nil || value.ErrorCode != "" || value.ErrorMessage != "" || value.ErrorDetails != "" {
		return fmt.Errorf("invalid completed run")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin run completion: %w", err)
	}
	defer tx.Rollback()
	if err := completeAssistantMessageWithCitations(ctx, tx, value.AssistantMessageID, value.ID, text, citations, value.UpdatedAt); err != nil {
		return err
	}
	if err := updateRun(ctx, tx, value); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit run completion: %w", err)
	}
	return nil
}

func updateRun(ctx context.Context, executor runUpdateExecutor, value chat.Run) error {
	if !value.APIProtocol.Valid() {
		value.APIProtocol = modelprofile.ProtocolOpenAIChat
	}
	contextBudget := modelcap.ResolveContextBudget(value.ContextWindowTokens, value.AutoCompactTokenLimit, value.ContextWindowSource)
	value.ContextWindowTokens = contextBudget.WindowTokens
	if value.ContextBudgetTokens <= 0 || value.ContextBudgetTokens > contextBudget.EffectiveTokens {
		value.ContextBudgetTokens = contextBudget.EffectiveTokens
	}
	value.AutoCompactTokenLimit = min(contextBudget.AutoCompactTokens, value.ContextBudgetTokens)
	value.ContextWindowSource = contextBudget.Source
	result, err := executor.ExecContext(ctx, `UPDATE runs SET assistant_message_id=?, api_protocol=?, resolved_reasoning_level=?, context_window_tokens=?, context_budget_tokens=?, auto_compact_token_limit=?, context_window_source=?, context_compacted=?, status=?, error_code=?, error_message=?, error_details=?, input_tokens=?, fresh_input_tokens=?, output_tokens=?, reasoning_tokens=?, reasoning_observed=?, reasoning_signature_observed=?, reasoning_summary=?, cached_input_tokens=?, cache_write_tokens=?, cache_reported_turns=?, cache_reported_fresh_input_tokens=?, cache_hit_turns=?, finish_reason=?, started_at=?, completed_at=?, updated_at=? WHERE id=? AND status NOT IN ('completed','failed','cancelled','interrupted')`,
		nullableString(value.AssistantMessageID), value.APIProtocol, value.ResolvedReasoningLevel, value.ContextWindowTokens, value.ContextBudgetTokens, value.AutoCompactTokenLimit, value.ContextWindowSource, value.ContextCompacted, value.Status, value.ErrorCode, value.ErrorMessage, value.ErrorDetails, value.InputTokens, value.FreshInputTokens, value.OutputTokens, value.ReasoningTokens, value.ReasoningObserved, value.ReasoningSignatureObserved, value.ReasoningSummary, value.CachedInputTokens, value.CacheWriteTokens, value.CacheReportedTurns, value.CacheReportedFreshInputTokens, value.CacheHitTurns, value.FinishReason, nullableTime(value.StartedAt), nullableTime(value.CompletedAt), formatTime(value.UpdatedAt), value.ID)
	if err != nil {
		return fmt.Errorf("update run: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		var exists int
		if err := executor.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE id=?`, value.ID).Scan(&exists); err != nil {
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
	turnStatus := chat.ModelTurnInterrupted
	if next == chat.RunFailed {
		resultStatus = "error"
		turnStatus = chat.ModelTurnFailed
	}
	for _, callID := range affectedCalls {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tool_results(tool_call_id,status,text_content,artifacts_json,citations_json,truncated,meta_json,created_at) VALUES (?,?,?,'[]','[]',0,'{}',?) ON CONFLICT(tool_call_id) DO NOTHING`, callID, resultStatus, errorMessage, formatTime(at)); err != nil {
			return chat.Run{}, false, fmt.Errorf("record terminated tool result: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE model_turn_journal SET status=?,completed_at=?,updated_at=? WHERE run_id=? AND status='streaming'`, turnStatus, formatTime(at), formatTime(at), runID); err != nil {
		return chat.Run{}, false, fmt.Errorf("close model turn journal: %w", err)
	}
	if err := appendRunTerminationContextTx(ctx, tx, runID, next, errorMessage, at); err != nil {
		return chat.Run{}, false, fmt.Errorf("record run termination context: %w", err)
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

func appendRunTerminationContextTx(ctx context.Context, tx *sql.Tx, runID string, status chat.RunStatus, message string, at time.Time) error {
	value := runTerminationContext{Kind: "run_termination_context", Status: status, Message: truncateTerminationText(message)}
	var draft string
	if err := tx.QueryRowContext(ctx, `SELECT draft_text FROM model_turn_journal WHERE run_id=? ORDER BY turn_index DESC LIMIT 1`, runID).Scan(&draft); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	value.Draft = truncateTerminationText(draft)

	rows, err := tx.QueryContext(ctx, `SELECT turn_index,commentary_text,reasoning_summary FROM run_steps WHERE run_id=? ORDER BY turn_index DESC LIMIT ?`, runID, maxTerminationActivities)
	if err != nil {
		return err
	}
	for rows.Next() {
		var item runTerminationActivity
		if err := rows.Scan(&item.TurnIndex, &item.Commentary, &item.ReasoningSummary); err != nil {
			_ = rows.Close()
			return err
		}
		item.Commentary = truncateTerminationText(item.Commentary)
		item.ReasoningSummary = truncateTerminationText(item.ReasoningSummary)
		value.Activities = append(value.Activities, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	reverseTerminationActivities(value.Activities)

	rows, err = tx.QueryContext(ctx, `SELECT c.tool_name,c.arguments_json,c.status,c.error_code,c.error_message,COALESCE(r.text_content,'') FROM tool_calls c LEFT JOIN tool_results r ON r.tool_call_id=c.id WHERE c.run_id=? ORDER BY c.created_at DESC,c.id DESC LIMIT ?`, runID, maxTerminationTools)
	if err != nil {
		return err
	}
	for rows.Next() {
		var item runTerminationTool
		if err := rows.Scan(&item.Name, &item.Arguments, &item.Status, &item.ErrorCode, &item.ErrorMessage, &item.Result); err != nil {
			_ = rows.Close()
			return err
		}
		item.Arguments = truncateTerminationText(item.Arguments)
		item.ErrorMessage = truncateTerminationText(item.ErrorMessage)
		item.Result = truncateTerminationText(item.Result)
		value.Tools = append(value.Tools, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	reverseTerminationTools(value.Tools)
	if value.Draft == "" && len(value.Activities) == 0 && len(value.Tools) == 0 {
		return nil
	}

	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var messageID string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(assistant_message_id,'') FROM runs WHERE id=?`, runID).Scan(&messageID); err != nil {
		return err
	}
	if messageID == "" {
		return nil
	}
	var ordinal int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal),-1)+1 FROM message_parts WHERE message_id=?`, messageID).Scan(&ordinal); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO message_parts(id,message_id,ordinal,part_type,text_content,payload_json,created_at) VALUES (?,?,?,'tool_result','',?,?)`, runID+":termination-context", messageID, ordinal, string(payload), formatTime(at))
	return err
}

func truncateTerminationText(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > maxTerminationTextRunes {
		return string(runes[:maxTerminationTextRunes]) + "..."
	}
	return value
}

func reverseTerminationActivities(values []runTerminationActivity) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func reverseTerminationTools(values []runTerminationTool) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
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
	rows, err := tx.QueryContext(ctx, `SELECT id FROM runs WHERE status IN ('queued','running','waiting_approval') ORDER BY created_at,id`)
	if err != nil {
		return 0, fmt.Errorf("list active runs for recovery: %w", err)
	}
	runIDs := make([]string, 0)
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			_ = rows.Close()
			return 0, err
		}
		runIDs = append(runIDs, runID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, runID := range runIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE model_turn_journal SET status='interrupted',completed_at=?,updated_at=? WHERE run_id=? AND status='streaming'`, formatTime(at), formatTime(at), runID); err != nil {
			return 0, fmt.Errorf("interrupt model turn journal: %w", err)
		}
		if err := appendRunTerminationContextTx(ctx, tx, runID, chat.RunInterrupted, "应用退出时运行尚未完成", at); err != nil {
			return 0, fmt.Errorf("record recovered run context: %w", err)
		}
	}
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
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin run event append: %w", err)
	}
	defer tx.Rollback()
	payload := string(event.Payload)
	_, err = tx.ExecContext(ctx, `INSERT INTO run_events(event_id, version, aggregate_id, aggregate_type, sequence, event_type, timestamp, payload_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		event.EventID, event.Version, event.AggregateID, event.AggregateType, event.Sequence, event.Type, formatTime(event.Timestamp), payload)
	if err != nil {
		return fmt.Errorf("append run event: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_event_sequences(aggregate_id,last_sequence) VALUES (?,?) ON CONFLICT(aggregate_id) DO UPDATE SET last_sequence=MAX(last_sequence,excluded.last_sequence)`, event.AggregateID, event.Sequence); err != nil {
		return fmt.Errorf("advance run event sequence: %w", err)
	}
	return tx.Commit()
}

func (r *RunRepository) LatestEventSequence(ctx context.Context, runID string) (int64, error) {
	var sequence int64
	if err := r.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT last_sequence FROM run_event_sequences WHERE aggregate_id=?),0)`, strings.TrimSpace(runID)).Scan(&sequence); err != nil {
		return 0, fmt.Errorf("read latest run event sequence: %w", err)
	}
	return sequence, nil
}

func (r *RunRepository) EventByID(ctx context.Context, eventID string) (events.Envelope, error) {
	var value events.Envelope
	var timestamp, payload string
	if err := r.db.QueryRowContext(ctx, `SELECT version,event_id,aggregate_id,aggregate_type,sequence,event_type,timestamp,payload_json FROM run_events WHERE event_id=?`, strings.TrimSpace(eventID)).Scan(
		&value.Version, &value.EventID, &value.AggregateID, &value.AggregateType, &value.Sequence, &value.Type, &timestamp, &payload); err != nil {
		return value, fmt.Errorf("read run event: %w", err)
	}
	var err error
	value.Timestamp, err = parseTime(timestamp)
	if err != nil {
		return value, err
	}
	value.Payload = json.RawMessage(payload)
	return value, nil
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
	where, args := usageWhere(query, "u")
	success := "u.status_code>=200 AND u.status_code<300"

	summaryQuery := `SELECT COUNT(DISTINCT CASE WHEN ` + success + ` THEN u.run_id END),
		COALESCE(SUM(CASE WHEN ` + success + ` THEN 1 ELSE 0 END),0), COUNT(*),
		COALESCE(SUM(CASE WHEN ` + success + ` THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN ` + success + ` THEN 0 ELSE 1 END),0),
		COALESCE(SUM(CASE WHEN ` + success + ` THEN u.fresh_input_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN ` + success + ` THEN u.output_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN ` + success + ` THEN u.reasoning_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN ` + success + ` THEN u.cached_input_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN ` + success + ` THEN u.cache_write_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN ` + success + ` THEN u.cache_details_reported ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN ` + success + ` AND u.cache_details_reported=1 AND u.cached_input_tokens>0 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN ` + success + ` AND u.cache_details_reported=1 THEN u.fresh_input_tokens ELSE 0 END),0)
		FROM model_request_usage u` + where
	if err := scanUsageSummary(r.db.QueryRowContext(ctx, summaryQuery, args...), &result.Summary); err != nil {
		return result, fmt.Errorf("read global usage summary: %w", err)
	}

	dailyRows, err := r.db.QueryContext(ctx, `SELECT substr(datetime(u.started_at, 'localtime'),1,10),
		COUNT(DISTINCT CASE WHEN `+success+` THEN u.run_id END),
		COALESCE(SUM(CASE WHEN `+success+` THEN 1 ELSE 0 END),0), COUNT(*),
		COALESCE(SUM(CASE WHEN `+success+` THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN `+success+` THEN 0 ELSE 1 END),0),
		COALESCE(SUM(CASE WHEN `+success+` THEN u.fresh_input_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN `+success+` THEN u.output_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN `+success+` THEN u.reasoning_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN `+success+` THEN u.cached_input_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN `+success+` THEN u.cache_write_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN `+success+` THEN u.cache_details_reported ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN `+success+` AND u.cache_details_reported=1 AND u.cached_input_tokens>0 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN `+success+` AND u.cache_details_reported=1 THEN u.fresh_input_tokens ELSE 0 END),0)
		FROM model_request_usage u`+where+` GROUP BY 1 ORDER BY 1`, args...)
	if err != nil {
		return result, fmt.Errorf("read daily usage: %w", err)
	}
	defer dailyRows.Close()
	for dailyRows.Next() {
		var item chat.DailyUsage
		var reportedFresh int
		if err := dailyRows.Scan(&item.Date, &item.RunCount, &item.ModelTurns, &item.RequestCount,
			&item.SuccessfulRequests, &item.FailedRequests, &item.FreshInputTokens,
			&item.OutputTokens, &item.ReasoningTokens, &item.CacheReadTokens, &item.CacheCreationTokens,
			&item.CacheReportedTurns, &item.CacheHitTurns, &reportedFresh); err != nil {
			return result, err
		}
		deriveUsageSummary(&item.UsageSummary, reportedFresh)
		result.Daily = append(result.Daily, item)
	}
	if err := dailyRows.Err(); err != nil {
		return result, err
	}

	modelRows, err := r.db.QueryContext(ctx, `SELECT u.model_profile_id, COALESCE(NULLIF(u.profile_name,''),p.name,''), u.model_id,
		COUNT(DISTINCT CASE WHEN `+success+` THEN u.run_id END),
		COALESCE(SUM(CASE WHEN `+success+` THEN 1 ELSE 0 END),0), COUNT(*),
		COALESCE(SUM(CASE WHEN `+success+` THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN `+success+` THEN 0 ELSE 1 END),0),
		COALESCE(SUM(CASE WHEN `+success+` THEN u.fresh_input_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN `+success+` THEN u.output_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN `+success+` THEN u.reasoning_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN `+success+` THEN u.cached_input_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN `+success+` THEN u.cache_write_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN `+success+` THEN u.cache_details_reported ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN `+success+` AND u.cache_details_reported=1 AND u.cached_input_tokens>0 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN `+success+` AND u.cache_details_reported=1 THEN u.fresh_input_tokens ELSE 0 END),0)
		FROM model_request_usage u LEFT JOIN model_profiles p ON p.id=u.model_profile_id`+where+`
		GROUP BY u.model_profile_id, COALESCE(NULLIF(u.profile_name,''),p.name,''), u.model_id
		ORDER BY (COALESCE(SUM(u.fresh_input_tokens),0)+COALESCE(SUM(u.output_tokens),0)+COALESCE(SUM(u.cached_input_tokens),0)+COALESCE(SUM(u.cache_write_tokens),0)) DESC, u.model_id`, args...)
	if err != nil {
		return result, fmt.Errorf("read model usage: %w", err)
	}
	defer modelRows.Close()
	for modelRows.Next() {
		var item chat.ModelUsage
		var reportedFresh int
		if err := modelRows.Scan(&item.ModelProfileID, &item.ProfileName, &item.ModelID,
			&item.RunCount, &item.ModelTurns, &item.RequestCount, &item.SuccessfulRequests, &item.FailedRequests,
			&item.FreshInputTokens, &item.OutputTokens,
			&item.ReasoningTokens, &item.CacheReadTokens, &item.CacheCreationTokens, &item.CacheReportedTurns,
			&item.CacheHitTurns, &reportedFresh); err != nil {
			return result, err
		}
		deriveUsageSummary(&item.UsageSummary, reportedFresh)
		result.Models = append(result.Models, item)
	}
	return result, modelRows.Err()
}

func (r *RunRepository) UsageRequests(ctx context.Context, query chat.UsageRequestQuery) (chat.UsageRequestPage, error) {
	result := chat.UsageRequestPage{Items: []chat.RequestUsage{}, Offset: query.Offset, Limit: query.Limit, Query: query}
	where, args := usageWhere(chat.UsageQuery{
		StartDate: query.StartDate, EndDate: query.EndDate, StartTime: query.StartTime, EndTime: query.EndTime,
		ModelProfileID: query.ModelProfileID, ModelID: query.ModelID,
	}, "u")
	if query.StatusCode != 0 {
		where += " AND u.status_code=?"
		args = append(args, query.StatusCode)
	}
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_request_usage u`+where, args...).Scan(&result.Total); err != nil {
		return result, fmt.Errorf("count model request usage: %w", err)
	}
	pageArgs := append(append([]any(nil), args...), query.Limit, query.Offset)
	rows, err := r.db.QueryContext(ctx, `SELECT u.id,u.run_id,u.turn_index,u.request_kind,u.model_profile_id,
		COALESCE(NULLIF(u.profile_name,''),p.name,''),u.model_id,u.api_protocol,u.input_tokens,u.fresh_input_tokens,u.output_tokens,
		u.reasoning_tokens,u.cached_input_tokens,u.cache_write_tokens,u.cache_details_reported,
		u.status_code,u.error_code,u.error_message,u.first_token_millis,u.is_streaming,
		u.started_at,u.completed_at,u.duration_millis
		FROM model_request_usage u LEFT JOIN model_profiles p ON p.id=u.model_profile_id`+where+`
		ORDER BY u.started_at DESC,u.id DESC LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return result, fmt.Errorf("list model request usage: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item chat.RequestUsage
		var startedAt, completedAt string
		var firstTokenMillis sql.NullInt64
		if err := rows.Scan(&item.ID, &item.RunID, &item.TurnIndex, &item.RequestKind, &item.ModelProfileID,
			&item.ProfileName, &item.ModelID, &item.APIProtocol, &item.InputTokens, &item.FreshInputTokens,
			&item.OutputTokens, &item.ReasoningTokens, &item.CachedInputTokens, &item.CacheWriteTokens,
			&item.CacheDetailsReported, &item.StatusCode, &item.ErrorCode, &item.ErrorMessage,
			&firstTokenMillis, &item.IsStreaming, &startedAt, &completedAt, &item.DurationMillis); err != nil {
			return result, err
		}
		if firstTokenMillis.Valid {
			value := firstTokenMillis.Int64
			item.FirstTokenMillis = &value
		}
		item.StartedAt, err = parseTime(startedAt)
		if err != nil {
			return result, err
		}
		item.CompletedAt, err = parseTime(completedAt)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, item)
	}
	return result, rows.Err()
}

func usageWhere(query chat.UsageQuery, alias string) (string, []any) {
	prefix := alias + "."
	conditions := []string{"1=1"}
	args := []any{}
	if query.StartDate != "" {
		if query.StartTime != "" {
			conditions = append(conditions, "datetime("+prefix+"started_at, 'localtime') >= datetime(?)")
			args = append(args, query.StartDate+" "+query.StartTime)
		} else {
			conditions = append(conditions, "date("+prefix+"started_at, 'localtime') >= date(?)")
			args = append(args, query.StartDate)
		}
	}
	if query.EndDate != "" {
		if query.EndTime != "" {
			conditions = append(conditions, "datetime("+prefix+"started_at, 'localtime') < datetime(?, '+1 minute')")
			args = append(args, query.EndDate+" "+query.EndTime)
		} else {
			conditions = append(conditions, "date("+prefix+"started_at, 'localtime') <= date(?)")
			args = append(args, query.EndDate)
		}
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
	if err := row.Scan(&target.RunCount, &target.ModelTurns, &target.RequestCount,
		&target.SuccessfulRequests, &target.FailedRequests, &target.FreshInputTokens,
		&target.OutputTokens, &target.ReasoningTokens, &target.CacheReadTokens, &target.CacheCreationTokens,
		&target.CacheReportedTurns, &target.CacheHitTurns, &reportedFresh); err != nil {
		return err
	}
	deriveUsageSummary(target, reportedFresh)
	return nil
}

func deriveUsageSummary(target *chat.UsageSummary, reportedFresh int) {
	target.RealTotalTokens = target.FreshInputTokens + target.OutputTokens + target.CacheReadTokens + target.CacheCreationTokens
	if target.RequestCount > 0 {
		target.SuccessRate = float64(target.SuccessfulRequests) / float64(target.RequestCount)
	}
	target.CacheDataAvailable = target.CacheReportedTurns > 0
	cacheableInput := reportedFresh + target.CacheReadTokens + target.CacheCreationTokens
	if target.CacheDataAvailable && cacheableInput > 0 {
		target.CacheHitRate = float64(target.CacheReadTokens) / float64(cacheableInput)
	}
}

func appendNextEventTx(ctx context.Context, tx *sql.Tx, event *events.Envelope) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_event_sequences(aggregate_id,last_sequence) VALUES (?,1) ON CONFLICT(aggregate_id) DO UPDATE SET last_sequence=last_sequence+1`, event.AggregateID); err != nil {
		return fmt.Errorf("advance run event sequence: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT last_sequence FROM run_event_sequences WHERE aggregate_id=?`, event.AggregateID).Scan(&event.Sequence); err != nil {
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

type runUpdateExecutor interface {
	sqlExecer
	QueryRowContext(context.Context, string, ...any) *sql.Row
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
	if err := row.Scan(&value.ID, &value.ConversationID, &value.UserMessageID, &value.AssistantMessageID, &value.ModelProfileID, &value.ModelID, &value.APIProtocol, &value.RequestedReasoningLevel, &value.ResolvedReasoningLevel, &value.ContextWindowTokens, &value.ContextBudgetTokens, &value.AutoCompactTokenLimit, &value.ContextWindowSource, &value.ContextCompacted, &value.PermissionMode, &value.Status,
		&value.ErrorCode, &value.ErrorMessage, &value.ErrorDetails, &value.InputTokens, &value.FreshInputTokens, &value.OutputTokens, &value.ReasoningTokens, &value.ReasoningObserved, &value.ReasoningSignatureObserved, &value.ReasoningSummary, &value.CachedInputTokens, &value.CacheWriteTokens, &value.CacheReportedTurns, &value.CacheReportedFreshInputTokens, &value.CacheHitTurns, &value.ModelTurns, &value.FinishReason, &createdAt, &startedAt, &completedAt, &updatedAt, &value.WebSearchDisabled); err != nil {
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

const runSelect = `SELECT id, conversation_id, user_message_id, COALESCE(assistant_message_id, ''), model_profile_id, model_id, api_protocol, requested_reasoning_level, resolved_reasoning_level, context_window_tokens, context_budget_tokens, auto_compact_token_limit, context_window_source, context_compacted, permission_mode, status, error_code, error_message, error_details, input_tokens, fresh_input_tokens, output_tokens, reasoning_tokens, reasoning_observed, reasoning_signature_observed, reasoning_summary, cached_input_tokens, cache_write_tokens, cache_reported_turns, cache_reported_fresh_input_tokens, cache_hit_turns, model_turns, finish_reason, created_at, started_at, completed_at, updated_at, web_search_disabled FROM runs`

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

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func sameNullableInt64(stored sql.NullInt64, value *int64) bool {
	if value == nil {
		return !stored.Valid
	}
	return stored.Valid && stored.Int64 == *value
}

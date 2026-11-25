package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/wangh00/SciAide/internal/app/workflow"
)

// Derive only on explicit read; normal guidance/state polling does not scan
// history. At most four source rows leave SQLite, never the full note history.
func (r *WorkflowRuntimeRepository) GetResearchWorkingStateProvenance(ctx context.Context, projectID, taskID string, expectedVersion int) (workflow.ResearchWorkingStateProvenance, error) {
	var empty workflow.ResearchWorkingStateProvenance
	projectID, taskID = strings.TrimSpace(projectID), strings.TrimSpace(taskID)
	if projectID == "" || taskID == "" || expectedVersion < 1 {
		return empty, fmt.Errorf("需要当前项目、科研任务和工作记录版本")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	note, err := readResearchWorkingState(ctx, tx, projectID, taskID)
	if err != nil {
		return empty, err
	}
	if note == nil || note.Version != expectedVersion {
		return empty, fmt.Errorf("研究工作记录版本已变化，请刷新后读取字段来源")
	}
	value := workflow.ResearchWorkingStateProvenance{ProjectID: projectID, ResearchTaskID: taskID, StateID: note.ID, Version: note.Version,
		Status: "unverified_submission_provenance", Fields: []workflow.ResearchWorkingStateFieldSource{}}
	completeHistory, err := researchWorkingHistoryComplete(ctx, tx, *note)
	if err != nil {
		return empty, err
	}
	for _, field := range []string{"currentFocus", "rationale", "nextStep", "unknowns"} {
		source, err := readResearchWorkingFieldSource(ctx, tx, *note, field, completeHistory)
		if err != nil {
			return empty, err
		}
		value.Fields = append(value.Fields, source)
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return value, nil
}

func researchWorkingHistoryComplete(ctx context.Context, tx *sql.Tx, note workflow.ResearchWorkingState) (bool, error) {
	var count, versions int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(DISTINCT s.version) FROM research_working_states s`+effectiveWorkingState+`
 WHERE s.project_id=? AND s.research_task_id=? AND s.version<=?`, note.ProjectID, note.ResearchTaskID, note.Version).Scan(&count, &versions)
	return count == note.Version && versions == note.Version, err
}

func readResearchWorkingFieldSource(ctx context.Context, tx *sql.Tx, note workflow.ResearchWorkingState, field string, completeHistory bool) (workflow.ResearchWorkingStateFieldSource, error) {
	missing := workflow.ResearchWorkingStateFieldSource{Field: field, Status: "unavailable"}
	var source workflow.ResearchWorkingStateFieldSource
	var raw, created, sourceValue string
	var callRunID, toolName string
	var bound bool
	column := map[string]string{"currentFocus": "current_focus", "rationale": "rationale", "nextStep": "next_step", "unknowns": "unknowns_json"}[field]
	if column == "" {
		return missing, fmt.Errorf("未知研究工作记录字段")
	}
	// field is from the fixed host list above, never model text. Invalid JSON
	// is selected as an unknown boundary, not skipped to invent an older origin.
	err := tx.QueryRowContext(ctx, `SELECT s.id,s.version,s.source_chat_run_id,s.source_user_message_id,s.source_call_id,s.created_at,tc.arguments_json,COALESCE(tc.run_id,''),tc.tool_name,s.`+column+`,
 EXISTS(SELECT 1 FROM messages human JOIN runs cr ON cr.id=s.source_chat_run_id AND cr.conversation_id=human.conversation_id
 JOIN research_discussion_conversations d ON d.conversation_id=cr.conversation_id AND d.project_id=s.project_id AND d.research_task_id=s.research_task_id
 WHERE human.id=s.source_user_message_id AND human.run_id=cr.id AND human.role='user' AND human.status='complete'
 AND tc.workflow_run_id IS NULL AND NOT EXISTS(SELECT 1 FROM workflow_ai_chat_runs internal WHERE internal.chat_run_id=cr.id))
 FROM research_working_states s`+effectiveWorkingState+`
 WHERE s.project_id=? AND s.research_task_id=? AND s.version<=?
 AND CASE WHEN json_valid(tc.arguments_json) THEN json_type(tc.arguments_json,?) IS NOT NULL ELSE 1 END
 ORDER BY s.version DESC LIMIT 1`, note.ProjectID, note.ResearchTaskID, note.Version, "$."+field).
		Scan(&source.StateID, &source.Version, &source.SourceChatRunID, &source.SourceUserMessageID, &source.SourceCallID, &created, &raw, &callRunID, &toolName, &sourceValue, &bound)
	if errors.Is(err, sql.ErrNoRows) {
		if completeHistory && (field == "nextStep" && note.NextStep == "" || field == "unknowns" && len(note.Unknowns) == 0) {
			missing.Status = "not_recorded"
		}
		return missing, nil
	}
	if err != nil {
		return missing, err
	}
	command, err := workflow.ParseResearchWorkingStateUpdate(json.RawMessage(raw))
	if err != nil || !bound || !command.HasField(field) || source.Version != command.ExpectedVersion+1 ||
		!completeHistory && source.StateID != note.ID || source.Version == note.Version && source.StateID != note.ID ||
		source.SourceChatRunID == "" || source.SourceUserMessageID == "" || source.SourceCallID == "" || callRunID != source.SourceChatRunID || toolName != workflow.ResearchStateUpdateTool {
		return missing, nil
	}
	// A matching value alone is never sufficient to attribute a field; it must
	// be an explicit original argument of the last effective submission.
	matched := false
	switch field {
	case "currentFocus":
		matched = note.CurrentFocus == command.CurrentFocus && sourceValue == command.CurrentFocus
	case "rationale":
		matched = note.Rationale == command.Rationale && sourceValue == command.Rationale
	case "nextStep":
		matched = note.NextStep == command.NextStep && sourceValue == command.NextStep
	case "unknowns":
		var unknowns []string
		matched = json.Unmarshal([]byte(sourceValue), &unknowns) == nil && reflect.DeepEqual(note.Unknowns, command.Unknowns) && reflect.DeepEqual(unknowns, command.Unknowns)
	}
	if !matched {
		return missing, nil
	}
	at, err := parseTime(created)
	if err != nil {
		return missing, err
	}
	source.CreatedAt = &at
	source.Field, source.Status = field, "recorded"
	for _, id := range command.Basis[field] {
		basis, _, err := readWorkingBasis(ctx, tx, note.ProjectID, note.ResearchTaskID, source.SourceCallID, id)
		if err != nil {
			basis = workflow.ResearchWorkingBasisSource{ReadCallID: id, Status: "unavailable"}
		}
		source.Basis = append(source.Basis, basis)
	}
	return source, nil
}

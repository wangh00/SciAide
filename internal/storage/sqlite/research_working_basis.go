package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

// Verify the real persisted read, not model-supplied query/candidate/hash text.
// The parent submission is also fixed: later reads cannot retroactively become
// a basis for an already proposed working note, even during a paused batch.
func readWorkingBasis(ctx context.Context, tx *sql.Tx, projectID, taskID, parentCallID, readCallID string) (workflow.ResearchWorkingBasisSource, string, error) {
	missing := workflow.ResearchWorkingBasisSource{ReadCallID: readCallID, Status: "unavailable"}
	var arguments, raw, modelContext, completed, proposed string
	var truncated bool
	err := tx.QueryRowContext(ctx, `SELECT tc.arguments_json,tr.structured_json,tr.model_context_text,tc.completed_at,parent.created_at,tr.truncated
 FROM tool_calls tc JOIN tool_results tr ON tr.tool_call_id=tc.id
 JOIN runs cr ON cr.id=tc.run_id JOIN research_discussion_conversations d ON d.conversation_id=cr.conversation_id
 JOIN messages human ON human.id=cr.user_message_id AND human.conversation_id=cr.conversation_id AND human.run_id=cr.id AND human.role='user' AND human.status='complete'
 JOIN tool_calls parent ON parent.id=? AND parent.tool_name=?
 WHERE tc.id=? AND tc.tool_name=? AND tc.status='completed' AND tr.status='success'
 AND tc.workflow_run_id IS NULL AND tc.rowid<parent.rowid AND d.project_id=? AND d.research_task_id=?
 AND NOT EXISTS(SELECT 1 FROM workflow_ai_chat_runs internal WHERE internal.chat_run_id=cr.id)`, parentCallID, workflow.ResearchStateUpdateTool, readCallID, workflow.ResearchDiscoveryReadTool, projectID, taskID).
		Scan(&arguments, &raw, &modelContext, &completed, &proposed, &truncated)
	if errors.Is(err, sql.ErrNoRows) {
		return missing, "", fmt.Errorf("关联需要本任务在提议前已成功完成的摘要页读取回执")
	}
	if err != nil {
		return missing, "", err
	}
	finished, err := parseTime(completed)
	if err != nil {
		return missing, "", err
	}
	started, err := parseTime(proposed)
	if err != nil {
		return missing, "", err
	}
	if truncated || finished.After(started) {
		return missing, "", fmt.Errorf("摘要页尚未在本次提议之前成功提供，不能用后续或截断结果补造依据")
	}
	p, err := workflow.ParseResearchWorkingAbstractPage(json.RawMessage(raw))
	if err != nil {
		return missing, "", err
	}
	if err = workflow.ValidateResearchWorkingAbstractSelector(json.RawMessage(arguments), p); err != nil {
		return missing, "", err
	}
	deliveredID, provided, err := workflow.ParseResearchWorkingAbstractReceipt(json.RawMessage(modelContext))
	if err != nil || deliveredID != readCallID || provided != p {
		return missing, "", fmt.Errorf("原模型回执未提供同一摘要页")
	}
	var snapshot string
	err = tx.QueryRowContext(ctx, `SELECT s.snapshot_json FROM research_query_candidates s
 JOIN research_queries q ON q.id=s.query_id JOIN research_query_origins o ON o.query_id=q.id
 JOIN research_tasks task ON task.id=o.task_id AND task.project_id=q.project_id
 WHERE q.project_id=? AND o.task_id=? AND q.id=? AND s.candidate_id=? AND q.snapshot_version>0 AND task.status<>'archived'`, projectID, taskID, p.QueryID, p.CandidateID).Scan(&snapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return missing, "", fmt.Errorf("原查询摘要快照缺失或归属不符；不以新检索或最新题录替代")
	}
	if err != nil {
		return missing, "", err
	}
	var candidate research.Candidate
	if json.Unmarshal([]byte(snapshot), &candidate) != nil || candidate.ID != p.CandidateID || candidate.ProjectID != projectID {
		return missing, "", fmt.Errorf("原查询摘要身份无效")
	}
	text := candidate.Preferred.Abstract
	runes := []rune(text)
	digest := sha256.Sum256([]byte(text))
	end := p.Offset + len([]rune(p.Content))
	if p.TotalRunes != len(runes) || p.SHA256 != hex.EncodeToString(digest[:]) || end > len(runes) || p.Content != string(runes[p.Offset:end]) {
		return missing, "", fmt.Errorf("摘要读取回执与原快照/实际提供页不一致")
	}
	return workflow.ResearchWorkingBasisSource{ReadCallID: readCallID, Status: "linked_unverified_abstract", QueryID: p.QueryID, CandidateID: p.CandidateID, SHA256: p.SHA256, Offset: p.Offset, EndOffset: end, TotalRunes: p.TotalRunes}, p.Content, nil
}

func (r *WorkflowRuntimeRepository) GetResearchWorkingStateBasis(ctx context.Context, projectID, taskID string, expectedVersion int, field, readCallID string) (workflow.ResearchWorkingBasisRead, error) {
	var empty workflow.ResearchWorkingBasisRead
	projectID, taskID = strings.TrimSpace(projectID), strings.TrimSpace(taskID)
	if projectID == "" || taskID == "" || expectedVersion < 1 || readCallID == "" {
		return empty, fmt.Errorf("需要当前任务、记录版本和显式关联的摘要页")
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
		return empty, fmt.Errorf("工作记录版本已变化，请刷新后读取关联摘要页")
	}
	complete, err := researchWorkingHistoryComplete(ctx, tx, *note)
	if err != nil {
		return empty, err
	}
	source, err := readResearchWorkingFieldSource(ctx, tx, *note, field, complete)
	if err != nil {
		return empty, err
	}
	selected := false
	for _, basis := range source.Basis {
		selected = selected || basis.ReadCallID == readCallID && basis.Status == "linked_unverified_abstract"
	}
	if source.Status != "recorded" || !selected {
		return empty, fmt.Errorf("这条摘要页未与当前字段显式关联，或原读取记录已无法核对")
	}
	basis, content, err := readWorkingBasis(ctx, tx, projectID, taskID, source.SourceCallID, readCallID)
	if err != nil {
		return empty, err
	}
	value := workflow.ResearchWorkingBasisRead{ProjectID: projectID, ResearchTaskID: taskID, StateID: note.ID, Version: note.Version, Field: field, Basis: basis, Content: content}
	if err := tx.Commit(); err != nil {
		return empty, err
	}
	return value, nil
}

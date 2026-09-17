package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

func (r *WorkflowRuntimeRepository) ReadResearchTimeline(ctx context.Context, query workflow.ResearchTimelineQuery) (workflow.ResearchTimelinePage, error) {
	page := workflow.ResearchTimelinePage{TaskID: query.TaskID, Entries: []workflow.ResearchTimelineEntry{}}
	latest, err := r.GetLatestTaskRun(ctx, query.ProjectID, query.TaskID)
	if err != nil {
		return page, fmt.Errorf("当前项目不存在该科研任务")
	}
	page.LatestRunID, page.ConversationID = latest.ID, latest.ConversationID
	before := int64(0)
	after := int64(0)
	if query.After != "" {
		after, err = strconv.ParseInt(query.After, 10, 64)
		if err != nil || after <= 0 || query.Before != "" {
			return page, fmt.Errorf("invalid timeline cursor")
		}
	}
	if query.Before != "" {
		before, err = strconv.ParseInt(query.Before, 10, 64)
		if err != nil || before <= 0 {
			return page, fmt.Errorf("invalid timeline cursor")
		}
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	order := "DESC"
	if after > 0 {
		order = "ASC"
	}
	rows, err := r.db.QueryContext(ctx, `SELECT t.sequence,t.workflow_run_id,COALESCE(wc.conversation_id,''),t.kind,t.source_id,t.event_type,t.snapshot_json,t.created_at
		FROM research_timeline t JOIN workflow_runs wr ON wr.id=t.workflow_run_id LEFT JOIN workflow_conversations wc ON wc.workflow_run_id=wr.id
		WHERE t.research_task_id=? AND wr.project_id=? AND (?=0 OR t.sequence<?) AND (?=0 OR t.sequence>?)
		AND NOT(t.kind='message' AND EXISTS(SELECT 1 FROM messages m JOIN workflow_ai_chat_runs b ON b.chat_run_id=m.run_id WHERE m.id=t.source_id AND m.role='user'))
		ORDER BY t.sequence `+order+` LIMIT ?`, query.TaskID, query.ProjectID, before, before, after, after, limit+1)
	if err != nil {
		return page, err
	}
	type item struct {
		entry  workflow.ResearchTimelineEntry
		source string
	}
	items := []item{}
	for rows.Next() {
		var v item
		var sequence int64
		var snapshot string
		if err := rows.Scan(&sequence, &v.entry.RunID, &v.entry.ConversationID, &v.entry.Kind, &v.source, &v.entry.EventType, &snapshot, &v.entry.CreatedAt); err != nil {
			rows.Close()
			return page, err
		}
		v.entry.ID = v.entry.Kind + ":" + v.source
		v.entry.Sequence = strconv.FormatInt(sequence, 10)
		v.entry.Snapshot = json.RawMessage(snapshot)
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return page, err
	}
	rows.Close()
	page.HasMore = len(items) > limit
	if page.HasMore {
		items = items[:limit]
	}
	if len(items) > 0 {
		page.NextBefore = items[len(items)-1].entry.Sequence
	}
	if after > 0 {
		for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
			items[left], items[right] = items[right], items[left]
		}
	}
	details := map[string]workflow.RunDetail{}
	for i := len(items) - 1; i >= 0; i-- {
		v := items[i]
		entry := v.entry
		detail, ok := details[entry.RunID]
		if !ok {
			detail.Run, err = scanWorkflowRun(r.db.QueryRowContext(ctx, workflowRunSelect+` WHERE wr.project_id=? AND wr.id=?`, query.ProjectID, entry.RunID))
			if err != nil {
				return page, err
			}
			detail.Steps, err = r.listSteps(ctx, entry.RunID)
			if err != nil {
				return page, err
			}
			detail.Events, err = r.listEvents(ctx, entry.RunID)
			if err != nil {
				return page, err
			}
			details[entry.RunID] = detail
		}
		switch entry.Kind {
		case "message":
			message, err := NewConversationRepository(r.db).GetTimelineMessage(ctx, v.source, entry.ConversationID)
			if err != nil {
				return page, err
			}
			entry.Message = &message
		case "proposal":
			if detail.RevisionProposals == nil {
				detail.RevisionProposals, err = r.ListResearchRevisionProposals(ctx, entry.RunID)
				if err != nil {
					return page, err
				}
				details[entry.RunID] = detail
			}
			for _, p := range detail.RevisionProposals {
				if p.ID == v.source {
					value := p
					entry.Proposal = &value
					break
				}
			}
		case "event":
			var snapshot map[string]json.RawMessage
			if err := json.Unmarshal(entry.Snapshot, &snapshot); err != nil {
				return page, err
			}
			var payload struct {
				StepID     string `json:"stepId"`
				ToolCallID string `json:"toolCallId"`
			}
			_ = json.Unmarshal(snapshot["event"], &payload)
			latestEvent := ""
			for _, event := range detail.Events {
				if event.Type != entry.EventType {
					continue
				}
				var other struct {
					StepID string `json:"stepId"`
				}
				_ = json.Unmarshal(event.Payload, &other)
				if other.StepID == payload.StepID {
					latestEvent = event.ID
				}
			}
			currentEvent := entry.RunID == latest.ID && latestEvent == v.source
			if payload.StepID != "" {
				var captured workflow.Step
				_ = json.Unmarshal(snapshot["step"], &captured)
				for _, step := range detail.Steps {
					if step.ID == payload.StepID {
						if captured.ID != "" {
							entry.Step = &captured
						}
						entry.Active = currentEvent && (captured.ID == "" || captured.Attempt == step.Attempt) && ((entry.EventType == "workflow.human_confirmation_requested" || entry.EventType == "workflow.agent_stage_review_requested") && step.Status == workflow.StepWaitingHumanConfirmation || (entry.EventType == "workflow.failed" || entry.EventType == "workflow.interrupted" || entry.EventType == "workflow.outcome_unknown") && (step.Status == workflow.StepFailed || step.Status == workflow.StepInterrupted || step.Status == workflow.StepOutcomeUnknown))
						if entry.Active {
							copy := step
							entry.Step = &copy
						}
						break
					}
				}
			}
			if entry.EventType == "workflow.completed" {
				entry.Active = currentEvent && detail.Run.Status == workflow.RunCompleted
				if detail.Run.WorkflowPurpose == workflow.PurposeResearchStarter {
					for _, event := range detail.Events {
						if event.Type == "research.route_adopted" {
							entry.Active = false
						}
					}
				}
			}
			if entry.EventType == "research.route_adopted" {
				entry.Active = currentEvent && detail.Run.WorkflowPurpose == workflow.PurposeResearchStarter
			}
			if entry.EventType == "workflow.tool_proposed" && payload.ToolCallID != "" {
				call, err := NewToolRepository(r.db).Get(ctx, payload.ToolCallID)
				if err != nil {
					return page, err
				}
				activity := workflow.ToolActivity{ID: call.ID, ToolName: call.ToolName, Status: call.Status, Risk: call.Risk, Summary: call.ToolName, Arguments: tool.SafeActivityArguments(call.Arguments), Permissions: tool.SafeActivityPermissions(call.Permissions), ErrorMessage: tool.SafeActivityText(call.ErrorMessage, 500), CreatedAt: call.CreatedAt, StartedAt: call.StartedAt, CompletedAt: call.CompletedAt}
				if call.Result != nil {
					activity.OutputSummary = tool.SafeActivityText(call.Result.Text, 1600)
				}
				entry.Tool = &activity
			}
		}
		page.Entries = append(page.Entries, entry)
	}
	return page, nil
}

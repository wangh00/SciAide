package debugapi

import (
	"net/http"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/userinput"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/id"
	w "github.com/wangh00/SciAide/internal/transport/wails"
)

func (s *Server) ownWorkflow(c Case, runID string) error {
	run, err := s.services.Workflow.GetRun(c.ProjectID, runID)
	if err != nil {
		return err
	}
	if run.Run.ResearchTaskID != c.TaskID {
		return invalidRequest("run does not belong to this debug case")
	}
	return nil
}
func (s *Server) ownChat(c Case, runID string) (w.RunSnapshot, error) {
	snap, err := s.services.Chat.GetRunSnapshot(runID)
	if err != nil {
		return snap, err
	}
	conv, err := s.services.Conversations.GetConversation(snap.Run.ConversationID)
	if err != nil {
		return snap, err
	}
	if conv.ProjectID != c.ProjectID {
		return snap, invalidRequest("chat does not belong to this debug case")
	}
	return snap, nil
}

func (s *Server) action(c Case, name string, r *http.Request) (any, error) {
	if c.InitialRunID == "" {
		return nil, &apperr.Error{Code: "DEBUG_CASE_INITIALIZATION_PENDING", UserMessage: "请先用相同 requestId 显式重放初始创建请求，确认案例初始化状态。"}
	}
	bindProject := func(projectID *string) error {
		if *projectID != "" && *projectID != c.ProjectID {
			return invalidRequest("cross-project action denied")
		}
		*projectID = c.ProjectID
		return nil
	}
	switch name {
	case "message":
		var cmd struct {
			Text, ClientMessageID string
			WebSearchEnabled      bool
		}
		if err := decode(r, &cmd); err != nil {
			return nil, err
		}
		if cmd.ClientMessageID == "" {
			cmd.ClientMessageID, _ = id.New()
		}
		current, err := s.services.Workflow.GetCurrentResearchTaskRun(c.ProjectID, c.TaskID)
		if err != nil {
			return nil, err
		}
		return s.services.Chat.StartChat(chat.StartCommand{ConversationID: discussionConversation(c, current), ModelProfileID: c.ModelProfileID, ModelID: c.ModelID, Text: cmd.Text, ClientMessageID: cmd.ClientMessageID, WebSearchEnabled: cmd.WebSearchEnabled})
	case "answer":
		var cmd userinput.Response
		if err := decode(r, &cmd); err != nil {
			return nil, err
		}
		snap, err := s.ownChat(c, cmd.RunID)
		if err != nil {
			return nil, err
		}
		if cmd.ConversationID != "" && cmd.ConversationID != snap.Run.ConversationID {
			return nil, invalidRequest("cross-conversation answer denied")
		}
		cmd.ConversationID = snap.Run.ConversationID
		return s.services.Chat.RespondToUserInput(cmd)
	case "approve":
		var cmd struct {
			RunID, ApprovalID string
			Allow             bool
		}
		if err := decode(r, &cmd); err != nil {
			return nil, err
		}
		snap, err := s.ownChat(c, cmd.RunID)
		if err != nil {
			return nil, err
		}
		found := false
		for _, approval := range snap.PendingApprovals {
			found = found || approval.ID == cmd.ApprovalID
		}
		if !found {
			return nil, invalidRequest("approval is not pending in this case")
		}
		return s.services.Permissions.ResolveApproval(permission.ResolveCommand{ApprovalID: cmd.ApprovalID, Allow: cmd.Allow, Scope: permission.ScopeCall})
	case "approve-workflow":
		var cmd struct {
			RunID, ApprovalID string
			Allow             bool
		}
		if err := decode(r, &cmd); err != nil {
			return nil, err
		}
		if err := s.ownWorkflow(c, cmd.RunID); err != nil {
			return nil, err
		}
		detail, err := s.services.Workflow.GetRun(c.ProjectID, cmd.RunID)
		if err != nil {
			return nil, err
		}
		found := false
		for _, p := range detail.PendingApprovals {
			found = found || p.ID == cmd.ApprovalID
		}
		if !found {
			return nil, invalidRequest("workflow approval is not pending in this case")
		}
		return s.services.Workflow.ResolveApproval(permission.ResolveCommand{ApprovalID: cmd.ApprovalID, Allow: cmd.Allow, Scope: permission.ScopeCall})
	case "adopt":
		var cmd struct {
			ProjectID, RunID, RouteID string
			ConfirmExecution          bool
		}
		if err := decode(r, &cmd); err != nil {
			return nil, err
		}
		// The current production adoption API may immediately execute with
		// full access. Never hide that behind an omitted permission field.
		if !s.options.AllowFullAccess || !cmd.ConfirmExecution {
			return nil, invalidRequest("route adoption requires --allow-full-access and confirmExecution=true; it may immediately execute the route")
		}
		if err := bindProject(&cmd.ProjectID); err != nil {
			return nil, err
		}
		if err := s.ownWorkflow(c, cmd.RunID); err != nil {
			return nil, err
		}
		return s.services.Workflow.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: cmd.ProjectID, RunID: cmd.RunID, RouteID: cmd.RouteID})
	case "start-route":
		var cmd workflow.StartAdoptedResearchRouteCommand
		if err := decode(r, &cmd); err != nil {
			return nil, err
		}
		if err := bindProject(&cmd.ProjectID); err != nil {
			return nil, err
		}
		if err := s.ownWorkflow(c, cmd.StarterRunID); err != nil {
			return nil, err
		}
		if cmd.PermissionMode == "" {
			if s.options.AllowFullAccess {
				cmd.PermissionMode = "full_access"
			} else {
				cmd.PermissionMode = "plan"
			}
		}
		if cmd.PermissionMode == "full_access" && !s.options.AllowFullAccess {
			return nil, invalidRequest("server does not permit full_access")
		}
		return s.services.Workflow.StartAdoptedResearchRoute(cmd)
	case "retry":
		var cmd workflow.RetryCommand
		if err := decode(r, &cmd); err != nil {
			return nil, err
		}
		if err := bindProject(&cmd.ProjectID); err != nil {
			return nil, err
		}
		if err := s.ownWorkflow(c, cmd.RunID); err != nil {
			return nil, err
		}
		return s.services.Workflow.Retry(cmd)
	case "confirm-revision":
		var cmd struct {
			workflow.ConfirmResearchRevisionCommand
			ConfirmExecution bool `json:"confirmExecution"`
		}
		if err := decode(r, &cmd); err != nil {
			return nil, err
		}
		if !cmd.Confirmed || !cmd.ConfirmExecution {
			return nil, invalidRequest("revision execution requires confirmed=true and confirmExecution=true")
		}
		if err := bindProject(&cmd.ProjectID); err != nil {
			return nil, err
		}
		if err := s.ownWorkflow(c, cmd.RunID); err != nil {
			return nil, err
		}
		return s.services.Workflow.ConfirmResearchRevision(cmd.ConfirmResearchRevisionCommand)
	case "decide":
		var cmd workflow.HumanDecisionCommand
		if err := decode(r, &cmd); err != nil {
			return nil, err
		}
		if err := bindProject(&cmd.ProjectID); err != nil {
			return nil, err
		}
		if err := s.ownWorkflow(c, cmd.RunID); err != nil {
			return nil, err
		}
		return s.services.Workflow.Decide(cmd)
	case "clarify":
		var cmd workflow.AnswerResearchClarificationCommand
		if err := decode(r, &cmd); err != nil {
			return nil, err
		}
		if err := bindProject(&cmd.ProjectID); err != nil {
			return nil, err
		}
		if err := s.ownWorkflow(c, cmd.StarterRunID); err != nil {
			return nil, err
		}
		return s.services.Workflow.AnswerResearchClarification(cmd)
	case "replan":
		var cmd workflow.ReplanResearchStarterCommand
		if err := decode(r, &cmd); err != nil {
			return nil, err
		}
		if err := bindProject(&cmd.ProjectID); err != nil {
			return nil, err
		}
		if err := s.ownWorkflow(c, cmd.StarterRunID); err != nil {
			return nil, err
		}
		return s.services.Workflow.ReplanResearchStarter(cmd)
	case "pause", "resume", "cancel":
		var cmd struct{ RunID string }
		if err := decode(r, &cmd); err != nil {
			return nil, err
		}
		if err := s.ownWorkflow(c, cmd.RunID); err != nil {
			return nil, err
		}
		if name == "pause" {
			return s.services.Workflow.Pause(c.ProjectID, cmd.RunID)
		}
		if name == "resume" {
			return s.services.Workflow.Resume(c.ProjectID, cmd.RunID)
		}
		return s.services.Workflow.Cancel(c.ProjectID, cmd.RunID)
	case "cancel-chat":
		var cmd struct{ RunID string }
		if err := decode(r, &cmd); err != nil {
			return nil, err
		}
		if _, err := s.ownChat(c, cmd.RunID); err != nil {
			return nil, err
		}
		err := s.services.Chat.CancelRun(cmd.RunID)
		return map[string]bool{"cancelled": err == nil}, err
	}
	return nil, invalidRequest("unknown allowed debug action")
}

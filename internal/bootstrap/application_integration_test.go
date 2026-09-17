package bootstrap

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestApplicationCanRestartOnSameData(t *testing.T) {
	root := t.TempDir()
	first, err := New(Options{RootDir: root})
	if err != nil {
		t.Fatalf("first New() error = %v", err)
	}
	first.Startup(context.Background())
	created, err := first.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{
		Name: "可重复启动", Description: "baseline",
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}

	second, err := New(Options{RootDir: root})
	if err != nil {
		t.Fatalf("second New() error = %v", err)
	}
	defer second.Close()
	projects, err := second.ProjectFacade.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects() error = %v", err)
	}
	if len(projects) != 1 || projects[0].ID != created.ID {
		t.Fatalf("projects = %#v, want created project", projects)
	}
}

func TestApplicationRecoveryExpiresApprovalBeforeInterruptingCallAndRun(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	application, err := New(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	projectValue, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "恢复测试"})
	if err != nil {
		t.Fatal(err)
	}
	conversationValue, err := application.ConversationFacade.CreateConversation(wailstransport.CreateConversationRequest{ProjectID: projectValue.ID, Title: "P2.2"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	profile := modelprofile.Profile{ID: "recovery-profile", Name: "fixture", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "fixture", Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true, IsDefault: true}}, SecretRef: "recovery-secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewModelProfileRepository(application.store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	user := conversation.Message{ID: "recovery-user", ConversationID: conversationValue.ID, RunID: "recovery-run", Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "recovery-user-part", MessageID: "recovery-user", Type: "text", CreatedAt: now}}}
	assistant := conversation.Message{ID: "recovery-assistant", ConversationID: conversationValue.ID, RunID: "recovery-run", Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "recovery-assistant-part", MessageID: "recovery-assistant", Type: "text", CreatedAt: now}}}
	run := chat.Run{ID: "recovery-run", ConversationID: conversationValue.ID, UserMessageID: user.ID, AssistantMessageID: assistant.ID, ModelProfileID: profile.ID, ModelID: "fixture", Status: chat.RunWaitingApproval, CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewRunRepository(application.store.DB()).CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}
	call := tool.Call{ID: "recovery-call", RunID: run.ID, ProviderCallID: "recovery-provider", ToolName: "builtin.workspace.read", ToolVersion: "1", Arguments: json.RawMessage(`{"path":"paper.md"}`), Status: tool.CallAwaitingApproval, Risk: tool.RiskModerate, Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead, Resource: "paper.md"}}, CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewToolRepository(application.store.DB()).Create(ctx, call); err != nil {
		t.Fatal(err)
	}
	approval := permission.Approval{ID: "recovery-approval", RunID: run.ID, ToolCallID: call.ID, ProjectID: projectValue.ID, ToolName: call.ToolName, ToolVersion: call.ToolVersion, PermissionKind: tool.PermissionToolInvoke, Resource: call.ToolName, Risk: call.Risk, Status: permission.ApprovalPending, RequestedScope: permission.ScopeProject, CreatedAt: now}
	if _, err := application.store.DB().ExecContext(ctx, `INSERT INTO approvals(id,run_id,tool_call_id,project_id,tool_name,tool_version,permission_kind,resource,risk,status,requested_scope,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, approval.ID, approval.RunID, approval.ToolCallID, approval.ProjectID, approval.ToolName, approval.ToolVersion, approval.PermissionKind, approval.Resource, approval.Risk, approval.Status, permission.ScopeCall, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	loadedApproval, err := sqlite.NewPermissionRepository(restarted.store.DB()).GetApproval(ctx, approval.ID)
	if err != nil || loadedApproval.Status != permission.ApprovalExpired {
		t.Fatalf("approval = %#v, %v", loadedApproval, err)
	}
	loadedCall, err := sqlite.NewToolRepository(restarted.store.DB()).Get(ctx, call.ID)
	if err != nil || loadedCall.Status != tool.CallInterrupted {
		t.Fatalf("call = %#v, %v", loadedCall, err)
	}
	loadedRun, err := sqlite.NewRunRepository(restarted.store.DB()).Get(ctx, run.ID)
	if err != nil || loadedRun.Status != chat.RunInterrupted {
		t.Fatalf("run = %#v, %v", loadedRun, err)
	}
	var sequences []string
	rows, err := restarted.store.DB().QueryContext(ctx, `SELECT event_type FROM run_events WHERE aggregate_id=? ORDER BY sequence`, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var eventType string
		if err := rows.Scan(&eventType); err != nil {
			t.Fatal(err)
		}
		sequences = append(sequences, eventType)
	}
	if len(sequences) != 1 || sequences[0] != "approval.expired" {
		t.Fatalf("recovery audit events = %v", sequences)
	}
}

func TestApplicationRestartPreservesPendingWorkflowApproval(t *testing.T) {
	root := t.TempDir()
	first, err := New(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	created, err := first.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Workflow approval restart"})
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	saved, err := first.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: created.ID, Definition: workflow.Definition{
		SchemaVersion: 1, Name: "Restart pending approval",
		Inputs: []workflow.Port{},
		Nodes: []workflow.Node{{
			ID: "execute", Name: "Execute", Kind: workflow.NodeTool, ToolName: "builtin.shell.execute",
			Arguments: json.RawMessage(`{"shell":"powershell","command":"Write-Output 'restart-ok'","timeoutSeconds":10}`),
		}},
		Edges: []workflow.Edge{}, Outputs: []workflow.Output{},
	}})
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	started, err := first.WorkflowFacade.Start(workflow.StartCommand{ProjectID: created.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{}`)})
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	waiting := waitForWorkflowState(t, first, created.ID, started.Run.ID, workflow.RunWaitingApproval, 10*time.Second)
	if len(waiting.PendingApprovals) != 1 || len(waiting.Steps) != 1 || waiting.Steps[0].ToolCallID == "" {
		first.Close()
		t.Fatalf("pending Workflow checkpoint = %#v", waiting)
	}
	approvalID, callID := waiting.PendingApprovals[0].ID, waiting.Steps[0].ToolCallID
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	recovered := waitForWorkflowState(t, restarted, created.ID, started.Run.ID, workflow.RunWaitingApproval, 10*time.Second)
	if len(recovered.PendingApprovals) != 1 || recovered.PendingApprovals[0].ID != approvalID || recovered.PendingApprovals[0].Status != permission.ApprovalPending || recovered.Steps[0].ToolCallID != callID {
		t.Fatalf("restarted Workflow approval checkpoint = %#v", recovered)
	}
	call, err := sqlite.NewToolRepository(restarted.store.DB()).Get(context.Background(), callID)
	if err != nil || call.Status != tool.CallAwaitingApproval {
		t.Fatalf("restarted Workflow ToolCall = %#v, %v", call, err)
	}
	completed := completeApprovedWorkflow(t, restarted, created.ID, started.Run.ID, 20*time.Second)
	if completed.Steps[0].Status != workflow.StepCompleted {
		t.Fatalf("restarted Workflow did not complete = %#v", completed)
	}
}

func TestApplicationRegistersKnowledgeSearch(t *testing.T) {
	application, err := New(Options{RootDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	definitions, err := application.ToolFacade.ListTools()
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		if definition.QualifiedName == "builtin.knowledge.search" {
			return
		}
	}
	t.Fatal("builtin.knowledge.search is not registered")
}

func TestApplicationRegistersFixedResearchToolSurface(t *testing.T) {
	application, err := New(Options{RootDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	definitions, err := application.ToolFacade.ListTools()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"builtin.research.task.read":               false,
		"builtin.research.revision.propose":        false,
		"builtin.research.catalog":                 false,
		"builtin.research.search":                  false,
		"builtin.research.fetch":                   false,
		"builtin.research.full_text.read":          false,
		"builtin.research.workflow.search":         false,
		"builtin.research.workflow.import":         false,
		"builtin.research.workflow.sync":           false,
		"builtin.research.workflow.python.ensure":  false,
		"builtin.research.workflow.python.prepare": false,
		"builtin.research.workflow.review.gate":    false,
		"builtin.research.workflow.report":         false,
	}
	for _, definition := range definitions {
		if !strings.HasPrefix(definition.QualifiedName, "builtin.research.") {
			continue
		}
		if _, known := want[definition.QualifiedName]; !known {
			t.Fatalf("unexpected research tool %q", definition.QualifiedName)
		}
		want[definition.QualifiedName] = true
	}
	for name, registered := range want {
		if !registered {
			t.Fatalf("%s is not registered", name)
		}
	}
}

func TestApplicationReferenceWorkflowTemplatesValidateAgainstLiveRegistry(t *testing.T) {
	application, err := New(Options{RootDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	projectValue, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Workflow 模板"})
	if err != nil {
		t.Fatal(err)
	}
	templates := application.WorkflowFacade.Templates()
	if len(templates) < 2 || templates[0].ID != "trusted-research-closure" {
		t.Fatalf("reference templates = %#v", templates)
	}
	for _, template := range templates {
		preview := application.WorkflowFacade.Validate(projectValue.ID, template.Definition)
		if !preview.Valid {
			t.Fatalf("template %q does not validate against live registry: %#v", template.ID, preview.Diagnostics)
		}
	}
}

func TestApplicationRegistersAndExecutesBuiltinWorkspaceTool(t *testing.T) {
	ctx := context.Background()
	application, err := New(Options{RootDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	projectValue, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "内置工具"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectValue.WorkspacePath, "paper.md"), []byte("科研内容"), 0o600); err != nil {
		t.Fatal(err)
	}
	conversationValue, err := application.ConversationFacade.CreateConversation(wailstransport.CreateConversationRequest{ProjectID: projectValue.ID, Title: "工具执行"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	profile := modelprofile.Profile{ID: "builtin-profile", Name: "fixture", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "fixture", Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true, IsDefault: true}}, SecretRef: "builtin-secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewModelProfileRepository(application.store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	user := conversation.Message{ID: "builtin-user", ConversationID: conversationValue.ID, RunID: "builtin-run", Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "builtin-user-part", MessageID: "builtin-user", Type: "text", CreatedAt: now}}}
	assistant := conversation.Message{ID: "builtin-assistant", ConversationID: conversationValue.ID, RunID: "builtin-run", Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "builtin-assistant-part", MessageID: "builtin-assistant", Type: "text", CreatedAt: now}}}
	run := chat.Run{ID: "builtin-run", ConversationID: conversationValue.ID, UserMessageID: user.ID, AssistantMessageID: assistant.ID, ModelProfileID: profile.ID, ModelID: "fixture", Status: chat.RunRunning, CreatedAt: now, UpdatedAt: now}
	runRepository := sqlite.NewRunRepository(application.store.DB())
	if err := runRepository.CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}
	definition := tool.Definition{QualifiedName: "builtin.workspace.read_text", Description: "读取当前科研项目 Workspace 中一个 UTF-8 文本文件的有界内容。", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["path"],"properties":{"path":{"type":"string","minLength":1,"maxLength":4096},"maxBytes":{"type":"integer","minimum":1,"maximum":262144}}}`), OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["path","content","bytesRead","originalBytes","truncated"],"properties":{"path":{"type":"string"},"content":{"type":"string"},"bytesRead":{"type":"integer","minimum":0},"originalBytes":{"type":"integer","minimum":0},"truncated":{"type":"boolean"}}}`), Risk: tool.RiskLow, Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead, Resource: "."}}, Idempotent: true, Version: "1"}
	service := tool.NewService(sqlite.NewToolRepository(application.store.DB()), tool.JSONSchemaValidator{})
	call, err := service.Propose(ctx, definition, tool.CreateCommand{RunID: run.ID, ProviderCallID: "builtin-provider", Arguments: json.RawMessage(`{"path":"paper.md"}`)})
	if err != nil {
		t.Fatal(err)
	}
	call, err = service.Start(ctx, call.ID)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := application.tools.Execute(ctx, projectValue.ID, call.ID)
	if err != nil || execution.Result.Status != tool.ResultSuccess || execution.Result.Text != "科研内容" {
		t.Fatalf("Execute() = %#v, %v", execution, err)
	}
}

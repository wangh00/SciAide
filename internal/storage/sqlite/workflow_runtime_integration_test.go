package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/artifact"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

type runtimeFixtureTool struct {
	name        string
	risk        tool.RiskLevel
	idempotent  bool
	permissions []tool.PermissionRequirement
	invoke      func(context.Context, tool.Invocation) (tool.Result, error)
}

func (f runtimeFixtureTool) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{
		QualifiedName: f.name, Description: "Workflow Runtime fixture", Version: "1", Risk: f.risk,
		Idempotent: f.idempotent, Permissions: f.permissions,
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["query"],"properties":{"query":{"type":"string"}}}`),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["answer"],"properties":{"answer":{"type":"string"},"candidates":{"type":"array","items":{"type":"object"}}}}`),
	}, nil
}
func (f runtimeFixtureTool) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if f.invoke != nil {
		return f.invoke(ctx, invocation)
	}
	var arguments struct {
		Query string `json:"query"`
	}
	_ = json.Unmarshal(invocation.Arguments, &arguments)
	structured, _ := json.Marshal(map[string]any{"answer": "result:" + arguments.Query})
	return tool.Result{Status: tool.ResultSuccess, Text: "completed", Structured: structured, Artifacts: []tool.ArtifactRef{}, Citations: []tool.CitationRef{}}, nil
}

type workflowRuntimeHarness struct {
	store       *Store
	project     project.Project
	definitions *workflow.Service
	runtime     *workflow.RuntimeService
	executor    *tool.Executor
}

func newWorkflowRuntimeHarness(t *testing.T, tools ...runtimeFixtureTool) *workflowRuntimeHarness {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workflow-runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(t.TempDir(), "workspaces"), filepath.Join(t.TempDir(), "trash"))
	selected, err := projects.Create(ctx, "Workflow Runtime", "")
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	for _, fixture := range tools {
		if err := registry.Register(ctx, fixture); err != nil {
			t.Fatal(err)
		}
	}
	definitionRepo := NewWorkflowRepository(store.DB())
	definitions, err := workflow.NewService(definitionRepo, projects, workflow.NewCompiler(registry))
	if err != nil {
		t.Fatal(err)
	}
	toolService := tool.NewService(NewToolRepository(store.DB()), tool.JSONSchemaValidator{})
	permissionEngine := permission.NewEngine(NewPermissionRepository(store.DB()))
	runtimeRepo := NewWorkflowRuntimeRepository(store.DB())
	executor := tool.NewExecutor(registry, toolService, tool.CompositeProjectResolver{Runs: NewRunRepository(store.DB()), Workflows: runtimeRepo}, tool.ExecutorOptions{})
	if err := executor.SetArtifactRegistrar(artifact.NewService(NewArtifactRepository(store.DB()), projects)); err != nil {
		t.Fatal(err)
	}
	runtimeService, err := workflow.NewRuntimeService(runtimeRepo, definitionRepo, projects, registry, toolService, permissionEngine, executor)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = runtimeService.Close()
		if err := store.Close(); err != nil {
			t.Errorf("close workflow store: %v", err)
		}
	})
	return &workflowRuntimeHarness{store: store, project: selected, definitions: definitions, runtime: runtimeService, executor: executor}
}

func (h *workflowRuntimeHarness) save(t *testing.T, definition workflow.Definition) workflow.SaveResult {
	t.Helper()
	result, err := h.definitions.Save(context.Background(), workflow.SaveCommand{ProjectID: h.project.ID, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func waitWorkflowStatus(t *testing.T, runtime *workflow.RuntimeService, projectID, runID string, statuses ...workflow.RunStatus) workflow.RunDetail {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		detail, err := runtime.Get(context.Background(), projectID, runID)
		if err != nil {
			t.Fatal(err)
		}
		for _, status := range statuses {
			if detail.Run.Status == status {
				return detail
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	detail, _ := runtime.Get(context.Background(), projectID, runID)
	t.Fatalf("Workflow Run status = %s, want one of %v; detail=%#v", detail.Run.Status, statuses, detail)
	return workflow.RunDetail{}
}

func TestWorkflowRuntimeExecutesBoundInputsAndFreezesCheckpoints(t *testing.T) {
	h := newWorkflowRuntimeHarness(t, runtimeFixtureTool{name: "fixture.runtime", risk: tool.RiskLow, idempotent: true})
	saved := h.save(t, workflow.Definition{SchemaVersion: 1, Name: "Runtime", Inputs: []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}}, Nodes: []workflow.Node{{ID: "search", Name: "Search", Kind: workflow.NodeTool, ToolName: "fixture.runtime", Arguments: json.RawMessage(`{}`)}}, Edges: []workflow.Edge{{FromNode: "$input", FromPort: "topic", ToNode: "search", ToPort: "query"}}, Outputs: []workflow.Output{{Name: "answer", Type: workflow.TypeString, FromNode: "search", FromPort: "structured.answer", Required: true}}})
	started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"topic":"alpha"}`)})
	if err != nil {
		t.Fatal(err)
	}
	detail := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunCompleted)
	if detail.Run.ConversationID == "" {
		t.Fatal("research Workflow did not create a bound conversation")
	}
	researchConversation, err := NewConversationRepository(h.store.DB()).GetConversation(context.Background(), detail.Run.ConversationID)
	if err != nil || researchConversation.ProjectID != h.project.ID || researchConversation.PermissionMode != detail.Run.PermissionMode || researchConversation.Title != "Research: Runtime" {
		t.Fatalf("research conversation = %#v, %v", researchConversation, err)
	}
	conversationRepository := NewConversationRepository(h.store.DB())
	listedConversations, err := conversationRepository.ListConversations(context.Background(), h.project.ID)
	if err != nil || len(listedConversations) != 0 {
		t.Fatalf("research-only ordinary conversation list = %#v, %v", listedConversations, err)
	}
	ordinaryConversation := conversation.Conversation{ID: "ordinary-conversation", ProjectID: h.project.ID, Title: "Ordinary chat", PermissionMode: conversation.PermissionPlan, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := conversationRepository.CreateConversation(context.Background(), ordinaryConversation); err != nil {
		t.Fatal(err)
	}
	listedConversations, err = conversationRepository.ListConversations(context.Background(), h.project.ID)
	if err != nil || len(listedConversations) != 1 || listedConversations[0].ID != ordinaryConversation.ID {
		t.Fatalf("ordinary conversation list = %#v, %v", listedConversations, err)
	}
	if loaded, err := conversationRepository.GetConversation(context.Background(), detail.Run.ConversationID); err != nil || loaded.ID != detail.Run.ConversationID {
		t.Fatalf("research conversation remains directly readable = %#v, %v", loaded, err)
	}
	if err := conversationRepository.DeleteConversation(context.Background(), detail.Run.ConversationID); err == nil {
		t.Fatal("bound research conversation was deleted independently")
	}
	if err := NewConversationRepository(h.store.DB()).UpdatePermissionMode(context.Background(), detail.Run.ConversationID, "full_access", time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("research conversation permission update error = %v", err)
	}
	if string(detail.Run.Outputs) != `{"answer":"result:alpha"}` || len(detail.Steps) != 1 || detail.Steps[0].Status != workflow.StepCompleted || detail.Steps[0].ToolCallID == "" {
		t.Fatalf("completed Workflow = %#v", detail)
	}
	var subjectCount, eventCount int
	if err := h.store.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE workflow_run_id=? AND run_id IS NULL`, detail.Run.ID).Scan(&subjectCount); err != nil || subjectCount != 1 {
		t.Fatalf("Workflow ToolCall subject = %d, %v", subjectCount, err)
	}
	if err := h.store.DB().QueryRow(`SELECT COUNT(*) FROM workflow_events WHERE workflow_run_id=?`, detail.Run.ID).Scan(&eventCount); err != nil || eventCount < 5 {
		t.Fatalf("Workflow events = %d, %v", eventCount, err)
	}
	if err := h.definitions.Delete(context.Background(), h.project.ID, saved.Workflow.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := NewConversationRepository(h.store.DB()).GetConversation(context.Background(), detail.Run.ConversationID); err == nil {
		t.Fatal("Workflow deletion retained its research conversation")
	}
	var bindings int
	if err := h.store.DB().QueryRow(`SELECT COUNT(*) FROM workflow_conversations WHERE conversation_id=?`, detail.Run.ConversationID).Scan(&bindings); err != nil || bindings != 0 {
		t.Fatalf("Workflow conversation bindings after deletion = %d, %v", bindings, err)
	}
}

func TestWorkflowRuntimeSeparatesRegisteredDeliverablesFromRunArtifacts(t *testing.T) {
	h := newWorkflowRuntimeHarness(t, runtimeFixtureTool{name: "fixture.registered", risk: tool.RiskLow, idempotent: true})
	saved := h.save(t, workflow.Definition{
		SchemaVersion: 1,
		Name:          "Registered deliverable",
		Inputs:        []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}},
		Nodes:         []workflow.Node{{ID: "search", Name: "Search", Kind: workflow.NodeTool, ToolName: "fixture.registered", Arguments: json.RawMessage(`{}`)}},
		Edges:         []workflow.Edge{{FromNode: "$input", FromPort: "topic", ToNode: "search", ToPort: "query"}},
		Outputs:       []workflow.Output{{Name: "answer", Type: workflow.TypeString, FromNode: "search", FromPort: "structured.answer", Required: true}},
	})
	started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"topic":"artifact-check"}`)})
	if err != nil {
		t.Fatal(err)
	}
	detail := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunCompleted)
	insertWorkflowArtifactForTest(t, h.store.DB(), h.project.ID, detail.Run.ID, "analysis-file", "analysis-output/result.json", "analysis")
	loaded, err := h.runtime.Get(context.Background(), h.project.ID, detail.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ArtifactCount != 1 || len(loaded.RegisteredDeliverables) != 0 {
		t.Fatalf("analysis artifact was treated as registered deliverable: count=%d registered=%v", loaded.ArtifactCount, loaded.RegisteredDeliverables)
	}
	insertWorkflowArtifactForTest(t, h.store.DB(), h.project.ID, detail.Run.ID, "report-file", "report.md", "report_draft")
	loaded, err = h.runtime.Get(context.Background(), h.project.ID, detail.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ArtifactCount != 2 || len(loaded.RegisteredDeliverables) != 1 || loaded.RegisteredDeliverables[0] != "report_draft" {
		t.Fatalf("registered deliverable projection = count=%d registered=%v", loaded.ArtifactCount, loaded.RegisteredDeliverables)
	}
}

func insertWorkflowArtifactForTest(t *testing.T, db *sql.DB, projectID, workflowRunID, artifactID, fileName, deliverable string) {
	t.Helper()
	now := formatTime(time.Now().UTC())
	sha := fmt.Sprintf("%064x", len(artifactID)+len(fileName)+len(deliverable))
	blobID := "blob-" + artifactID
	versionID := "version-" + artifactID
	lineageID := "lineage-" + artifactID
	if _, err := db.Exec(`INSERT INTO artifact_blobs(id,project_id,sha256,size_bytes,mime_type,storage_relative_path,created_at) VALUES (?,?,?,?,?,?,?)`, blobID, projectID, sha, 1, "application/json", "objects/"+artifactID, now); err != nil {
		t.Fatal(err)
	}
	provenance, err := json.Marshal(map[string]any{"extra": map[string]string{"workflowDeliverable": deliverable}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifacts(id,project_id,scope_kind,research_task_id,name,kind,status,current_version_id,created_at,updated_at,trashed_at) VALUES (?,?,?,? ,?,?,'active',NULL,?,?,NULL)`, artifactID, projectID, "legacy_project", "", artifactID, "document", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_versions(id,artifact_id,blob_id,version_number,file_name,mime_type,size_bytes,sha256,source_kind,source_key,provenance_json,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, versionID, artifactID, blobID, 1, fileName, "application/json", 1, sha, "tool", "test:"+artifactID, string(provenance), now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_lineage(id,artifact_version_id,ordinal,relation_kind,source_id_snapshot,source_run_id,source_workflow_run_id,label,metadata_json,created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, lineageID, versionID, 0, "workflow_run", workflowRunID, nil, workflowRunID, "test", "{}", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE artifacts SET current_version_id=? WHERE id=?`, versionID, artifactID); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyWorkflowRunWithoutResearchConversationRemainsReadable(t *testing.T) {
	h := newWorkflowRuntimeHarness(t)
	saved := h.save(t, workflow.Definition{SchemaVersion: 1, Name: "Legacy", Nodes: []workflow.Node{{ID: "confirm", Name: "Confirm", Kind: workflow.NodeHumanConfirmation, Prompt: "Continue?", Arguments: json.RawMessage(`{}`)}}})
	now := formatTime(time.Now().UTC())
	compilation, _ := json.Marshal(saved.Version.Compilation)
	runID := "legacy-without-conversation"
	if _, err := h.store.DB().Exec(`INSERT INTO workflow_runs(id,project_id,workflow_id,workflow_version_id,status,permission_mode,inputs_json,inputs_sha256,compilation_json,compilation_sha256,outputs_json,current_step_ordinal,created_at,updated_at) VALUES (?,?,?,?,?,'plan','{}',?,?,?,'{}',0,?,?)`, runID, h.project.ID, saved.Workflow.ID, saved.Version.ID, workflow.RunPaused, strings.Repeat("a", 64), string(compilation), saved.Version.CompilationSHA256, now, now); err != nil {
		t.Fatal(err)
	}
	detail, err := h.runtime.Get(context.Background(), h.project.ID, runID)
	if err != nil || detail.Run.ConversationID != "" || detail.Run.Status != workflow.RunPaused {
		t.Fatalf("legacy Workflow Run = %#v, %v", detail.Run, err)
	}
}

func TestWorkflowRuntimeListsProjectTasksAcrossReusablePlans(t *testing.T) {
	h := newWorkflowRuntimeHarness(t)
	definition := func(name string) workflow.Definition {
		return workflow.Definition{SchemaVersion: 1, Name: name, Nodes: []workflow.Node{{ID: "confirm", Name: "Confirm", Kind: workflow.NodeHumanConfirmation, Prompt: "Continue?", Arguments: json.RawMessage(`{}`)}}}
	}
	first := h.save(t, definition("First plan"))
	second := h.save(t, definition("Second plan"))
	for _, workflowID := range []string{first.Workflow.ID, second.Workflow.ID} {
		if _, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: workflowID, Inputs: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	repository := NewWorkflowRuntimeRepository(h.store.DB())
	projectRuns, err := repository.ListRuns(context.Background(), h.project.ID, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(projectRuns) != 2 || projectRuns[0].WorkflowID == projectRuns[1].WorkflowID {
		t.Fatalf("project task list = %#v", projectRuns)
	}
	filtered, err := repository.ListRuns(context.Background(), h.project.ID, first.Workflow.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].WorkflowID != first.Workflow.ID {
		t.Fatalf("plan-filtered task list = %#v", filtered)
	}
}

func TestResearchWorkflowConversationChatGateFollowsDurableStageState(t *testing.T) {
	h := newWorkflowRuntimeHarness(t)
	saved := h.save(t, workflow.Definition{SchemaVersion: 1, Name: "Chat gate", Nodes: []workflow.Node{{ID: "review", Name: "Review", Kind: workflow.NodeHumanConfirmation, Prompt: "Continue?", Arguments: json.RawMessage(`{}`)}}})
	started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	detail := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation)
	repository := NewRunRepository(h.store.DB())
	assertBlocked := func(want bool) {
		t.Helper()
		blocked, gateErr := repository.BlocksOrdinaryChatForWorkflowConversation(context.Background(), detail.Run.ConversationID)
		if gateErr != nil || blocked != want {
			t.Fatalf("chat gate blocked=%v, want %v: %v", blocked, want, gateErr)
		}
	}
	setState := func(runStatus workflow.RunStatus, stepKind workflow.NodeKind, stepStatus workflow.StepStatus) {
		t.Helper()
		if _, updateErr := h.store.DB().Exec(`UPDATE workflow_runs SET status=? WHERE id=?`, runStatus, detail.Run.ID); updateErr != nil {
			t.Fatal(updateErr)
		}
		if _, updateErr := h.store.DB().Exec(`UPDATE workflow_steps SET node_kind=?,status=? WHERE id=?`, stepKind, stepStatus, detail.Steps[0].ID); updateErr != nil {
			t.Fatal(updateErr)
		}
	}

	assertBlocked(true) // Ordinary human confirmation.
	setState(workflow.RunWaitingHumanConfirmation, workflow.NodeCandidateSelection, workflow.StepWaitingHumanConfirmation)
	assertBlocked(true)
	setState(workflow.RunWaitingHumanConfirmation, workflow.NodeCitationSelection, workflow.StepWaitingHumanConfirmation)
	assertBlocked(true)
	setState(workflow.RunWaitingHumanConfirmation, workflow.NodeAgentStage, workflow.StepWaitingHumanConfirmation)
	assertBlocked(false) // Legacy Agent Stage is the only active discussion window.
	setState(workflow.RunWaitingHumanConfirmation, workflow.NodeAgentStage, workflow.StepRunning)
	assertBlocked(true) // Inconsistent persistence fails closed.
	setState(workflow.RunRunning, workflow.NodeAgentStage, workflow.StepRunning)
	assertBlocked(true)
	setState(workflow.RunCompleted, workflow.NodeAgentStage, workflow.StepCompleted)
	assertBlocked(false)
	setState(workflow.RunCancelled, workflow.NodeAgentStage, workflow.StepCancelled)
	assertBlocked(false)
}

func TestProjectDeletionRejectsActiveResearchWorkflowAndCleansTerminalBinding(t *testing.T) {
	h := newWorkflowRuntimeHarness(t)
	saved := h.save(t, workflow.Definition{SchemaVersion: 1, Name: "Project lifecycle", Nodes: []workflow.Node{{ID: "confirm", Name: "Confirm", Kind: workflow.NodeHumanConfirmation, Prompt: "Continue?", Arguments: json.RawMessage(`{}`)}}})
	started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	detail := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation)
	projects := NewProjectRepository(h.store.DB())
	if err := projects.Delete(context.Background(), h.project.ID); err == nil || !strings.Contains(err.Error(), "active research Workflow") {
		t.Fatalf("active research project deletion error = %v", err)
	}
	if _, err := h.runtime.Cancel(context.Background(), h.project.ID, detail.Run.ID); err != nil {
		t.Fatal(err)
	}
	waitWorkflowStatus(t, h.runtime, h.project.ID, detail.Run.ID, workflow.RunCancelled)
	if err := projects.Delete(context.Background(), h.project.ID); err != nil {
		t.Fatal(err)
	}
	var bindings, conversations int
	_ = h.store.DB().QueryRow(`SELECT COUNT(*) FROM workflow_conversations WHERE workflow_run_id=?`, detail.Run.ID).Scan(&bindings)
	_ = h.store.DB().QueryRow(`SELECT COUNT(*) FROM conversations WHERE id=?`, detail.Run.ConversationID).Scan(&conversations)
	if bindings != 0 || conversations != 0 {
		t.Fatalf("project deletion retained research binding=%d conversation=%d", bindings, conversations)
	}
}

func TestWorkflowRuntimeHumanConfirmationAndApprovalResume(t *testing.T) {
	h := newWorkflowRuntimeHarness(t, runtimeFixtureTool{name: "fixture.approved", risk: tool.RiskHigh, idempotent: false, permissions: []tool.PermissionRequirement{{Kind: tool.PermissionProcessExecute}}})
	saved := h.save(t, workflow.Definition{SchemaVersion: 1, Name: "Review", Inputs: []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}}, Nodes: []workflow.Node{
		{ID: "confirm", Name: "Confirm", Kind: workflow.NodeHumanConfirmation, Prompt: "Continue?", Arguments: json.RawMessage(`{}`)},
		{ID: "execute", Name: "Execute", Kind: workflow.NodeTool, ToolName: "fixture.approved", Arguments: json.RawMessage(`{}`)},
	}, Edges: []workflow.Edge{{FromNode: "$input", FromPort: "topic", ToNode: "confirm", ToPort: "context"}, {FromNode: "$input", FromPort: "topic", ToNode: "execute", ToPort: "query"}}, Outputs: []workflow.Output{{Name: "answer", Type: workflow.TypeString, FromNode: "execute", FromPort: "structured.answer", Required: true}}})
	started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"topic":"beta"}`)})
	if err != nil {
		t.Fatal(err)
	}
	detail := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation)
	confirmed, err := h.runtime.Decide(context.Background(), workflow.HumanDecisionCommand{ProjectID: h.project.ID, RunID: detail.Run.ID, StepID: detail.Steps[0].ID, Approved: true, Note: "reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	detail = waitWorkflowStatus(t, h.runtime, h.project.ID, confirmed.Run.ID, workflow.RunWaitingApproval)
	if len(detail.PendingApprovals) != 1 || detail.Steps[1].Status != workflow.StepWaitingApproval {
		t.Fatalf("pending approval = %#v", detail)
	}
	resolved, err := h.runtime.ResolveApproval(context.Background(), permission.ResolveCommand{ApprovalID: detail.PendingApprovals[0].ID, Allow: true, Scope: permission.ScopeCall})
	if err != nil {
		t.Fatal(err)
	}
	detail = waitWorkflowStatus(t, h.runtime, h.project.ID, resolved.Run.ID, workflow.RunWaitingApproval, workflow.RunCompleted)
	for detail.Run.Status == workflow.RunWaitingApproval {
		if len(detail.PendingApprovals) == 0 {
			t.Fatal("missing next permission approval")
		}
		resolved, err = h.runtime.ResolveApproval(context.Background(), permission.ResolveCommand{ApprovalID: detail.PendingApprovals[0].ID, Allow: true, Scope: permission.ScopeCall})
		if err != nil {
			t.Fatal(err)
		}
		detail = waitWorkflowStatus(t, h.runtime, h.project.ID, resolved.Run.ID, workflow.RunWaitingApproval, workflow.RunCompleted)
	}
	if detail.Run.Status != workflow.RunCompleted || string(detail.Run.Outputs) != `{"answer":"result:beta"}` {
		t.Fatalf("resolved Workflow = %#v", detail)
	}
}

func TestWorkflowRuntimeFullAccessIsFrozenAndSkipsApproval(t *testing.T) {
	h := newWorkflowRuntimeHarness(t, runtimeFixtureTool{name: "fixture.full-access", risk: tool.RiskHigh, idempotent: true, permissions: []tool.PermissionRequirement{{Kind: tool.PermissionProcessExecute}}})
	saved := h.save(t, workflow.Definition{SchemaVersion: 1, Name: "Full Access", Inputs: []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}}, Nodes: []workflow.Node{{ID: "execute", Name: "Execute", Kind: workflow.NodeTool, ToolName: "fixture.full-access", Arguments: json.RawMessage(`{}`)}}, Edges: []workflow.Edge{{FromNode: "$input", FromPort: "topic", ToNode: "execute", ToPort: "query"}}})
	started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, PermissionMode: "full_access", Inputs: json.RawMessage(`{"topic":"automatic"}`)})
	if err != nil {
		t.Fatal(err)
	}
	detail := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunCompleted)
	if detail.Run.PermissionMode != "full_access" || len(detail.PendingApprovals) != 0 {
		t.Fatalf("Full Access Workflow = %#v", detail)
	}
	var mode string
	if err := h.store.DB().QueryRow(`SELECT permission_mode FROM workflow_runs WHERE id=?`, detail.Run.ID).Scan(&mode); err != nil || mode != "full_access" {
		t.Fatalf("persisted permission mode = %q, %v", mode, err)
	}
}

func TestWorkflowRuntimeRecoveryPreservesValidApprovalCheckpoint(t *testing.T) {
	h := newWorkflowRuntimeHarness(t, runtimeFixtureTool{name: "fixture.recover.approval", risk: tool.RiskHigh, idempotent: true, permissions: []tool.PermissionRequirement{{Kind: tool.PermissionProcessExecute}}})
	saved := h.save(t, workflow.Definition{
		SchemaVersion: 1, Name: "Recover approval",
		Inputs:  []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}},
		Nodes:   []workflow.Node{{ID: "execute", Name: "Execute", Kind: workflow.NodeTool, ToolName: "fixture.recover.approval", Arguments: json.RawMessage(`{}`)}},
		Edges:   []workflow.Edge{{FromNode: "$input", FromPort: "topic", ToNode: "execute", ToPort: "query"}},
		Outputs: []workflow.Output{{Name: "answer", Type: workflow.TypeString, FromNode: "execute", FromPort: "structured.answer", Required: true}},
	})
	started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"topic":"approval"}`)})
	if err != nil {
		t.Fatal(err)
	}
	before := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunWaitingApproval)
	if len(before.PendingApprovals) != 1 || before.Steps[0].Status != workflow.StepWaitingApproval {
		t.Fatalf("approval checkpoint = %#v", before)
	}
	if recovered, err := h.runtime.Recover(context.Background()); err != nil || recovered != 1 {
		t.Fatalf("Recover() = %d, %v", recovered, err)
	}
	after, err := h.runtime.Get(context.Background(), h.project.ID, before.Run.ID)
	if err != nil || after.Run.Status != workflow.RunWaitingApproval || after.Steps[0].Status != workflow.StepWaitingApproval || after.Steps[0].Attempt != before.Steps[0].Attempt || len(after.PendingApprovals) != 1 || after.PendingApprovals[0].ID != before.PendingApprovals[0].ID {
		t.Fatalf("Recover() changed approval checkpoint: before=%#v after=%#v err=%v", before, after, err)
	}
}

func TestWorkflowRuntimeCancellationClosesApprovalAndToolCall(t *testing.T) {
	h := newWorkflowRuntimeHarness(t, runtimeFixtureTool{name: "fixture.cancel.approval", risk: tool.RiskHigh, idempotent: true, permissions: []tool.PermissionRequirement{{Kind: tool.PermissionProcessExecute}}})
	saved := h.save(t, workflow.Definition{
		SchemaVersion: 1, Name: "Cancel approval",
		Inputs:  []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}},
		Nodes:   []workflow.Node{{ID: "execute", Name: "Execute", Kind: workflow.NodeTool, ToolName: "fixture.cancel.approval", Arguments: json.RawMessage(`{}`)}},
		Edges:   []workflow.Edge{{FromNode: "$input", FromPort: "topic", ToNode: "execute", ToPort: "query"}},
		Outputs: []workflow.Output{{Name: "answer", Type: workflow.TypeString, FromNode: "execute", FromPort: "structured.answer", Required: true}},
	})
	started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"topic":"cancel"}`)})
	if err != nil {
		t.Fatal(err)
	}
	waiting := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunWaitingApproval)
	if len(waiting.PendingApprovals) != 1 || waiting.Steps[0].ToolCallID == "" {
		t.Fatalf("approval checkpoint = %#v", waiting)
	}
	cancelled, err := h.runtime.Cancel(context.Background(), h.project.ID, waiting.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Run.Status != workflow.RunCancelled || cancelled.Steps[0].Status != workflow.StepCancelled || len(cancelled.PendingApprovals) != 0 {
		t.Fatalf("cancelled Workflow retained actionable state: %#v", cancelled)
	}
	var approvalStatus, callStatus string
	if err := h.store.DB().QueryRow(`SELECT status FROM approvals WHERE id=?`, waiting.PendingApprovals[0].ID).Scan(&approvalStatus); err != nil {
		t.Fatal(err)
	}
	if err := h.store.DB().QueryRow(`SELECT status FROM tool_calls WHERE id=?`, waiting.Steps[0].ToolCallID).Scan(&callStatus); err != nil {
		t.Fatal(err)
	}
	if approvalStatus != "expired" || callStatus != "cancelled" {
		t.Fatalf("approval/tool status = %q/%q", approvalStatus, callStatus)
	}
	if _, err := h.runtime.ResolveApproval(context.Background(), permission.ResolveCommand{ApprovalID: waiting.PendingApprovals[0].ID, Allow: true, Scope: permission.ScopeCall}); err == nil {
		t.Fatal("cancelled approval was resolved")
	}
	assertWorkflowEventSequence(t, h.store.DB(), waiting.Run.ID)
}

func TestWorkflowRuntimePermissionDenialTerminatesCallAsDenied(t *testing.T) {
	h := newWorkflowRuntimeHarness(t, runtimeFixtureTool{name: "fixture.deny.approval", risk: tool.RiskHigh, idempotent: true, permissions: []tool.PermissionRequirement{{Kind: tool.PermissionProcessExecute}}})
	saved := h.save(t, workflow.Definition{
		SchemaVersion: 1, Name: "Deny approval",
		Inputs: []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}},
		Nodes:  []workflow.Node{{ID: "execute", Name: "Execute", Kind: workflow.NodeTool, ToolName: "fixture.deny.approval", Arguments: json.RawMessage(`{}`)}},
		Edges:  []workflow.Edge{{FromNode: "$input", FromPort: "topic", ToNode: "execute", ToPort: "query"}},
	})
	started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"topic":"deny"}`)})
	if err != nil {
		t.Fatal(err)
	}
	waiting := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunWaitingApproval)
	denied, err := h.runtime.ResolveApproval(context.Background(), permission.ResolveCommand{ApprovalID: waiting.PendingApprovals[0].ID, Allow: false, Scope: permission.ScopeCall})
	if err != nil || denied.Run.Status != workflow.RunFailed || denied.Run.ErrorCode != "TOOL_PERMISSION_DENIED" || len(denied.PendingApprovals) != 0 {
		t.Fatalf("denied Workflow = %#v, %v", denied, err)
	}
	var approvalStatus, callStatus string
	if err := h.store.DB().QueryRow(`SELECT status FROM approvals WHERE id=?`, waiting.PendingApprovals[0].ID).Scan(&approvalStatus); err != nil {
		t.Fatal(err)
	}
	if err := h.store.DB().QueryRow(`SELECT status FROM tool_calls WHERE id=?`, waiting.Steps[0].ToolCallID).Scan(&callStatus); err != nil {
		t.Fatal(err)
	}
	if approvalStatus != "denied" || callStatus != "denied" {
		t.Fatalf("denied approval/tool status = %q/%q", approvalStatus, callStatus)
	}
}

func TestWorkflowRuntimeRecoveryClosesApprovalPersistenceWindows(t *testing.T) {
	t.Run("pending approval before Workflow wait checkpoint", func(t *testing.T) {
		h := newWorkflowRuntimeHarness(t, runtimeFixtureTool{name: "fixture.recover.pending", risk: tool.RiskHigh, idempotent: true, permissions: []tool.PermissionRequirement{{Kind: tool.PermissionProcessExecute}}})
		saved := h.save(t, workflow.Definition{
			SchemaVersion: 1, Name: "Recover pending approval",
			Inputs:  []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}},
			Nodes:   []workflow.Node{{ID: "execute", Name: "Execute", Kind: workflow.NodeTool, ToolName: "fixture.recover.pending", Arguments: json.RawMessage(`{}`)}},
			Edges:   []workflow.Edge{{FromNode: "$input", FromPort: "topic", ToNode: "execute", ToPort: "query"}},
			Outputs: []workflow.Output{{Name: "answer", Type: workflow.TypeString, FromNode: "execute", FromPort: "structured.answer", Required: true}},
		})
		started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"topic":"pending"}`)})
		if err != nil {
			t.Fatal(err)
		}
		before := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunWaitingApproval)
		if len(before.PendingApprovals) != 1 {
			t.Fatalf("approval checkpoint = %#v", before)
		}
		if _, err := h.store.DB().Exec(`UPDATE workflow_runs SET status='running' WHERE id=?`, before.Run.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := h.store.DB().Exec(`UPDATE workflow_steps SET status='running' WHERE id=?`, before.Steps[0].ID); err != nil {
			t.Fatal(err)
		}
		if recovered, err := h.runtime.Recover(context.Background()); err != nil || recovered != 1 {
			t.Fatalf("Recover() = %d, %v", recovered, err)
		}
		after := waitWorkflowStatus(t, h.runtime, h.project.ID, before.Run.ID, workflow.RunWaitingApproval)
		if len(after.PendingApprovals) != 1 || after.PendingApprovals[0].ID != before.PendingApprovals[0].ID || after.Steps[0].Attempt != before.Steps[0].Attempt {
			t.Fatalf("pending approval was not recovered in place: before=%#v after=%#v", before, after)
		}
	})

	t.Run("completed tool before Workflow approval resume checkpoint", func(t *testing.T) {
		var invocations atomic.Int32
		h := newWorkflowRuntimeHarness(t, runtimeFixtureTool{
			name: "fixture.recover.completed", risk: tool.RiskHigh, idempotent: true,
			permissions: []tool.PermissionRequirement{{Kind: tool.PermissionProcessExecute}},
			invoke: func(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
				invocations.Add(1)
				return runtimeFixtureTool{name: "fixture.recover.completed"}.Invoke(ctx, invocation)
			},
		})
		saved := h.save(t, workflow.Definition{
			SchemaVersion: 1, Name: "Recover completed approval",
			Inputs:  []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}},
			Nodes:   []workflow.Node{{ID: "execute", Name: "Execute", Kind: workflow.NodeTool, ToolName: "fixture.recover.completed", Arguments: json.RawMessage(`{}`)}},
			Edges:   []workflow.Edge{{FromNode: "$input", FromPort: "topic", ToNode: "execute", ToPort: "query"}},
			Outputs: []workflow.Output{{Name: "answer", Type: workflow.TypeString, FromNode: "execute", FromPort: "structured.answer", Required: true}},
		})
		started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"topic":"completed"}`)})
		if err != nil {
			t.Fatal(err)
		}
		before := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunWaitingApproval)
		if len(before.PendingApprovals) != 1 {
			t.Fatalf("approval checkpoint = %#v", before)
		}
		at := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := h.store.DB().Exec(`UPDATE approvals SET status='granted',resolved_scope='call',resolved_at=? WHERE id=? AND status='pending'`, at, before.PendingApprovals[0].ID); err != nil {
			t.Fatal(err)
		}
		if _, err := h.store.DB().Exec(`UPDATE tool_calls SET status='running',started_at=?,updated_at=? WHERE id=? AND status='awaiting_approval'`, at, at, before.Steps[0].ToolCallID); err != nil {
			t.Fatal(err)
		}
		if _, err := h.executor.Execute(context.Background(), h.project.ID, before.Steps[0].ToolCallID); err != nil {
			t.Fatal(err)
		}
		if recovered, err := h.runtime.Recover(context.Background()); err != nil || recovered != 1 {
			t.Fatalf("Recover() = %d, %v", recovered, err)
		}
		after := waitWorkflowStatus(t, h.runtime, h.project.ID, before.Run.ID, workflow.RunCompleted)
		if invocations.Load() != 1 || string(after.Run.Outputs) != `{"answer":"result:completed"}` || after.Steps[0].Attempt != before.Steps[0].Attempt {
			t.Fatalf("completed approval checkpoint was not committed once: %#v; invocations=%d", after, invocations.Load())
		}
	})
}

func TestWorkflowRuntimeFailsClosedWhenApprovedInputSnapshotWasModified(t *testing.T) {
	var invocations atomic.Int32
	h := newWorkflowRuntimeHarness(t, runtimeFixtureTool{
		name: "fixture.snapshot.approval", risk: tool.RiskHigh, idempotent: true,
		permissions: []tool.PermissionRequirement{{Kind: tool.PermissionProcessExecute}},
		invoke: func(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
			invocations.Add(1)
			return runtimeFixtureTool{name: "fixture.snapshot.approval"}.Invoke(ctx, invocation)
		},
	})
	if err := os.WriteFile(filepath.Join(h.project.WorkspacePath, "source.csv"), []byte("value\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	saved := h.save(t, workflow.Definition{
		SchemaVersion: 1, Name: "Snapshot approval",
		Inputs: []workflow.Port{{Name: "data", Type: workflow.TypeString, FileKind: "delimited", Required: true}},
		Nodes:  []workflow.Node{{ID: "execute", Name: "Execute", Kind: workflow.NodeTool, ToolName: "fixture.snapshot.approval", Arguments: json.RawMessage(`{}`)}},
		Edges:  []workflow.Edge{{FromNode: "$input", FromPort: "data", ToNode: "execute", ToPort: "query"}},
	})
	started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"data":"source.csv"}`)})
	if err != nil {
		t.Fatal(err)
	}
	detail := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunWaitingApproval)
	if len(detail.PendingApprovals) != 1 || detail.Steps[0].ToolCallID == "" {
		t.Fatalf("approval checkpoint = %#v", detail)
	}
	var inputs map[string]string
	if err := json.Unmarshal(detail.Run.Inputs, &inputs); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.project.WorkspacePath, filepath.FromSlash(inputs["data"])), []byte("value\n9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := h.runtime.ResolveApproval(context.Background(), permission.ResolveCommand{ApprovalID: detail.PendingApprovals[0].ID, Allow: true, Scope: permission.ScopeCall})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Run.Status != workflow.RunFailed || resolved.Run.ErrorCode != "WORKFLOW_INPUT_SNAPSHOT_INVALID" || resolved.Steps[0].Status != workflow.StepFailed || len(resolved.PendingApprovals) != 0 || invocations.Load() != 0 {
		t.Fatalf("invalid approved snapshot did not fail closed: %#v; invocations=%d", resolved, invocations.Load())
	}
	var approvalStatus, callStatus string
	if err := h.store.DB().QueryRow(`SELECT status FROM approvals WHERE id=?`, detail.PendingApprovals[0].ID).Scan(&approvalStatus); err != nil {
		t.Fatal(err)
	}
	if err := h.store.DB().QueryRow(`SELECT status FROM tool_calls WHERE id=?`, detail.Steps[0].ToolCallID).Scan(&callStatus); err != nil {
		t.Fatal(err)
	}
	if approvalStatus != "expired" || callStatus != "interrupted" {
		t.Fatalf("approval/tool status = %q/%q", approvalStatus, callStatus)
	}
}

func TestWorkflowRuntimeRejectsLegacyMutableInputsAtContinuationBoundaries(t *testing.T) {
	newHarness := func(t *testing.T) (*workflowRuntimeHarness, workflow.SaveResult) {
		h := newWorkflowRuntimeHarness(t)
		if err := os.WriteFile(filepath.Join(h.project.WorkspacePath, "source.csv"), []byte("value\n1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		saved := h.save(t, workflow.Definition{
			SchemaVersion: 1, Name: "Legacy continuation",
			Inputs:  []workflow.Port{{Name: "data", Type: workflow.TypeString, FileKind: "delimited", Required: true}},
			Nodes:   []workflow.Node{{ID: "confirm", Name: "Confirm", Kind: workflow.NodeHumanConfirmation, Prompt: "Continue?", Arguments: json.RawMessage(`{}`)}},
			Edges:   []workflow.Edge{{FromNode: "$input", FromPort: "data", ToNode: "confirm", ToPort: "context"}},
			Outputs: []workflow.Output{{Name: "approved", Type: workflow.TypeBoolean, FromNode: "confirm", FromPort: "approved", Required: true}},
		})
		return h, saved
	}
	legacyInputs := `{"data":"source.csv"}`
	legacyHash := fmt.Sprintf("%x", sha256.Sum256([]byte(legacyInputs)))
	setLegacyInputs := func(t *testing.T, h *workflowRuntimeHarness, runID string) {
		t.Helper()
		if _, err := h.store.DB().Exec(`UPDATE workflow_runs SET inputs_json=?,inputs_sha256=? WHERE id=?`, legacyInputs, legacyHash, runID); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("human decision", func(t *testing.T) {
		h, saved := newHarness(t)
		started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(legacyInputs)})
		if err != nil {
			t.Fatal(err)
		}
		detail := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation)
		setLegacyInputs(t, h, detail.Run.ID)
		resolved, err := h.runtime.Decide(context.Background(), workflow.HumanDecisionCommand{ProjectID: h.project.ID, RunID: detail.Run.ID, StepID: detail.Steps[0].ID, Approved: true})
		if err != nil || resolved.Run.Status != workflow.RunFailed || resolved.Run.ErrorCode != "WORKFLOW_INPUT_SNAPSHOT_REQUIRED" || resolved.Steps[0].Status != workflow.StepFailed {
			t.Fatalf("Decide() = %#v, %v", resolved, err)
		}
	})

	t.Run("resume", func(t *testing.T) {
		h, saved := newHarness(t)
		started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(legacyInputs)})
		if err != nil {
			t.Fatal(err)
		}
		detail := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation)
		if _, err := h.runtime.Pause(context.Background(), h.project.ID, detail.Run.ID); err != nil {
			t.Fatal(err)
		}
		setLegacyInputs(t, h, detail.Run.ID)
		resumed, err := h.runtime.Resume(context.Background(), h.project.ID, detail.Run.ID)
		if err != nil || resumed.Run.Status != workflow.RunFailed || resumed.Run.ErrorCode != "WORKFLOW_INPUT_SNAPSHOT_REQUIRED" || resumed.Steps[0].Status != workflow.StepFailed {
			t.Fatalf("Resume() = %#v, %v", resumed, err)
		}
	})

	t.Run("retry", func(t *testing.T) {
		h, saved := newHarness(t)
		started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(legacyInputs)})
		if err != nil {
			t.Fatal(err)
		}
		detail := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation)
		failed, err := h.runtime.Decide(context.Background(), workflow.HumanDecisionCommand{ProjectID: h.project.ID, RunID: detail.Run.ID, StepID: detail.Steps[0].ID, Approved: false})
		if err != nil {
			t.Fatal(err)
		}
		setLegacyInputs(t, h, detail.Run.ID)
		if _, err := h.runtime.Retry(context.Background(), workflow.RetryCommand{ProjectID: h.project.ID, RunID: failed.Run.ID, StepID: failed.Steps[0].ID}); err == nil || !strings.Contains(err.Error(), "不可变内容快照") {
			t.Fatalf("Retry() error = %v", err)
		}
		unchanged, err := h.runtime.Get(context.Background(), h.project.ID, failed.Run.ID)
		if err != nil || unchanged.Run.Status != workflow.RunFailed || unchanged.Steps[0].Attempt != 1 {
			t.Fatalf("retry mutated legacy Run: %#v, %v", unchanged, err)
		}
	})

	t.Run("startup recovery", func(t *testing.T) {
		h, saved := newHarness(t)
		compilation, _ := json.Marshal(saved.Version.Compilation)
		now := time.Now().UTC().Format(time.RFC3339Nano)
		runID, stepID := "legacy-recovery-run", "legacy-recovery-step"
		if _, err := h.store.DB().Exec(`INSERT INTO workflow_runs(id,project_id,workflow_id,workflow_version_id,status,inputs_json,inputs_sha256,compilation_json,compilation_sha256,outputs_json,current_step_ordinal,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,'{}',0,?,?)`, runID, h.project.ID, saved.Workflow.ID, saved.Version.ID, workflow.RunQueued, legacyInputs, legacyHash, string(compilation), saved.Version.CompilationSHA256, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := h.store.DB().Exec(`INSERT INTO workflow_steps(id,workflow_run_id,node_id,ordinal,node_kind,status,attempt,input_json,output_json,updated_at) VALUES (?,?,'confirm',0,'human_confirmation','queued',0,'{}','{}',?)`, stepID, runID, now); err != nil {
			t.Fatal(err)
		}
		if recovered, err := h.runtime.Recover(context.Background()); err != nil || recovered != 1 {
			t.Fatalf("Recover() = %d, %v", recovered, err)
		}
		detail := waitWorkflowStatus(t, h.runtime, h.project.ID, runID, workflow.RunFailed)
		if detail.Run.ErrorCode != "WORKFLOW_INPUT_SNAPSHOT_REQUIRED" || detail.Steps[0].Status != workflow.StepFailed {
			t.Fatalf("recovered legacy Run = %#v", detail)
		}
	})
}

func TestWorkflowRuntimeHumanContextAndCitationSelectionAreCommittedData(t *testing.T) {
	ref := tool.CitationRef{ID: "attachment", Kind: "knowledge_chunk", Reference: "[K-0123456789AB]", ProjectID: "project", IndexVersionID: "index", DocumentID: "document", AttachmentID: "attachment", ChunkID: "chunk", SourceName: "paper.md", Locator: "section:1", Quote: "trusted evidence", QuoteSHA256: strings.Repeat("a", 64)}
	h := newWorkflowRuntimeHarness(t, runtimeFixtureTool{
		name: "fixture.citations", risk: tool.RiskLow, idempotent: true,
		invoke: func(context.Context, tool.Invocation) (tool.Result, error) {
			return tool.Result{Status: tool.ResultSuccess, Text: "evidence", Structured: json.RawMessage(`{"answer":"found"}`), Artifacts: []tool.ArtifactRef{}, Citations: []tool.CitationRef{ref}}, nil
		},
	})
	saved := h.save(t, workflow.Definition{
		SchemaVersion: 1, Name: "Human data flow",
		Inputs: []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}},
		Nodes: []workflow.Node{
			{ID: "search", Name: "Search", Kind: workflow.NodeTool, ToolName: "fixture.citations", Arguments: json.RawMessage(`{}`)},
			{ID: "confirm", Name: "Confirm candidates", Kind: workflow.NodeHumanConfirmation, Prompt: "Select candidates", Arguments: json.RawMessage(`{}`)},
			{ID: "select", Name: "Select citations", Kind: workflow.NodeCitationSelection, Arguments: json.RawMessage(`{}`)},
		},
		Edges: []workflow.Edge{
			{FromNode: "$input", FromPort: "topic", ToNode: "search", ToPort: "query"},
			{FromNode: "search", FromPort: "structured", ToNode: "confirm", ToPort: "context"},
			{FromNode: "$input", FromPort: "topic", ToNode: "select", ToPort: "query"},
			{FromNode: "search", FromPort: "citations", ToNode: "select", ToPort: "candidates"},
		},
		Outputs: []workflow.Output{
			{Name: "review", Type: workflow.TypeAny, FromNode: "confirm", FromPort: "context", Required: true},
			{Name: "citations", Type: workflow.TypeCitations, FromNode: "select", FromPort: "citations", Required: true},
		},
	})
	started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"topic":"alpha"}`)})
	if err != nil {
		t.Fatal(err)
	}
	detail := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation)
	if detail.Steps[1].NodeID != "confirm" {
		t.Fatalf("first human step = %#v", detail.Steps[1])
	}
	contextJSON := json.RawMessage(`{"selectedCandidateIds":["candidate-a"]}`)
	if _, err := h.runtime.Decide(context.Background(), workflow.HumanDecisionCommand{ProjectID: h.project.ID, RunID: detail.Run.ID, StepID: detail.Steps[1].ID, Approved: true, Context: contextJSON}); err != nil {
		t.Fatal(err)
	}
	detail = waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation)
	if detail.Steps[2].NodeID != "select" {
		t.Fatalf("citation step = %#v", detail.Steps[2])
	}
	tampered := ref
	tampered.Quote = "changed"
	tamperedJSON, _ := json.Marshal([]tool.CitationRef{tampered})
	if _, err := h.runtime.Decide(context.Background(), workflow.HumanDecisionCommand{ProjectID: h.project.ID, RunID: detail.Run.ID, StepID: detail.Steps[2].ID, Approved: true, Context: tamperedJSON}); err == nil {
		t.Fatal("citation selection accepted a snapshot that was not offered")
	}
	selectedJSON, _ := json.Marshal([]tool.CitationRef{ref})
	if _, err := h.runtime.Decide(context.Background(), workflow.HumanDecisionCommand{ProjectID: h.project.ID, RunID: detail.Run.ID, StepID: detail.Steps[2].ID, Approved: true, Context: selectedJSON}); err != nil {
		t.Fatal(err)
	}
	detail = waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunCompleted)
	var outputs struct {
		Review    map[string][]string `json:"review"`
		Citations []tool.CitationRef  `json:"citations"`
	}
	if json.Unmarshal(detail.Run.Outputs, &outputs) != nil || len(outputs.Citations) != 1 || outputs.Citations[0].Quote != ref.Quote || len(outputs.Review["selectedCandidateIds"]) != 1 {
		t.Fatalf("committed human outputs = %s", detail.Run.Outputs)
	}
}

func TestWorkflowRuntimeAllowsExplicitEmptyCitationRecoveryOnlyForDataOrDesignRoutes(t *testing.T) {
	for _, test := range []struct {
		name        string
		substantive workflow.Node
		allowed     bool
	}{
		{name: "design route", substantive: workflow.Node{ID: "research_design", Name: "Design", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), Prompt: "Form a research design.", PromptVersion: "test-v1", ReviewPolicy: workflow.AIReviewAuto, OutputSchema: json.RawMessage(`{"type":"object"}`)}, allowed: true},
		{name: "evidence only", substantive: workflow.Node{ID: "draft", Name: "Draft", Kind: workflow.NodeHumanConfirmation, Prompt: "Confirm the evidence draft.", Arguments: json.RawMessage(`{}`)}, allowed: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newWorkflowRuntimeHarness(t, runtimeFixtureTool{
				name: "fixture.empty-citations", risk: tool.RiskLow, idempotent: true,
				invoke: func(context.Context, tool.Invocation) (tool.Result, error) {
					return tool.Result{Status: tool.ResultSuccess, Structured: json.RawMessage(`{"answer":"no matches"}`), Artifacts: []tool.ArtifactRef{}, Citations: []tool.CitationRef{}}, nil
				},
			})
			nodes := []workflow.Node{
				{ID: "search", Name: "Search", Kind: workflow.NodeTool, ToolName: "fixture.empty-citations", Arguments: json.RawMessage(`{}`)},
				{ID: "select", Name: "Select citations", Kind: workflow.NodeCitationSelection, Arguments: json.RawMessage(`{}`)},
				test.substantive,
			}
			substantivePort := "context"
			if test.substantive.Kind == workflow.NodeAgentStage {
				substantivePort = "evidenceContext"
			}
			saved := h.save(t, workflow.Definition{
				SchemaVersion: 1, Name: "Empty evidence recovery", Inputs: []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}}, Nodes: nodes,
				Edges: []workflow.Edge{
					{FromNode: "$input", FromPort: "topic", ToNode: "search", ToPort: "query"},
					{FromNode: "$input", FromPort: "topic", ToNode: "select", ToPort: "query"},
					{FromNode: "search", FromPort: "citations", ToNode: "select", ToPort: "candidates"},
					{FromNode: "select", FromPort: "citations", ToNode: test.substantive.ID, ToPort: substantivePort},
				},
			})
			started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"topic":"alpha"}`), ModelProfileID: "profile", ModelID: "model"})
			if err != nil {
				t.Fatal(err)
			}
			detail := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation)
			if detail.Steps[1].NodeID != "select" {
				t.Fatalf("citation step = %#v", detail.Steps[1])
			}
			updated, decideErr := h.runtime.Decide(context.Background(), workflow.HumanDecisionCommand{
				ProjectID: h.project.ID, RunID: detail.Run.ID, StepID: detail.Steps[1].ID, Approved: true,
				ContinueWithoutCitations: true, Context: json.RawMessage(`[]`),
			})
			if test.allowed {
				if decideErr != nil {
					t.Fatalf("recoverable empty evidence was rejected: %v", decideErr)
				}
				if updated.Steps[1].Status != workflow.StepCompleted || !strings.Contains(string(updated.Steps[1].Output), `"evidenceStatus":"no_verified_citations"`) {
					t.Fatalf("empty evidence decision = %s", updated.Steps[1].Output)
				}
			} else if decideErr == nil || !strings.Contains(decideErr.Error(), "requires at least one verified citation") {
				t.Fatalf("evidence-only route accepted empty citations: %v", decideErr)
			}
		})
	}
}

func TestWorkflowRuntimeCandidateSelectionRejectsUnknownAndDuplicateIDs(t *testing.T) {
	h := newWorkflowRuntimeHarness(t, runtimeFixtureTool{
		name: "fixture.candidates", risk: tool.RiskLow, idempotent: true,
		invoke: func(context.Context, tool.Invocation) (tool.Result, error) {
			return tool.Result{Status: tool.ResultSuccess, Structured: json.RawMessage(`{"answer":"found","candidates":[{"id":"candidate-a","title":"A"},{"id":"candidate-b","title":"B"}]}`), Artifacts: []tool.ArtifactRef{}, Citations: []tool.CitationRef{}}, nil
		},
	})
	saved := h.save(t, workflow.Definition{
		SchemaVersion: 1, Name: "Candidate selection", Inputs: []workflow.Port{{Name: "query", Type: workflow.TypeString, Required: true}},
		Nodes: []workflow.Node{
			{ID: "search", Name: "Search", Kind: workflow.NodeTool, ToolName: "fixture.candidates", Arguments: json.RawMessage(`{}`)},
			{ID: "select", Name: "Select", Kind: workflow.NodeCandidateSelection, Prompt: "Select candidates", Arguments: json.RawMessage(`{}`)},
		},
		Edges: []workflow.Edge{
			{FromNode: "$input", FromPort: "query", ToNode: "search", ToPort: "query"},
			{FromNode: "$input", FromPort: "query", ToNode: "select", ToPort: "query"},
			{FromNode: "search", FromPort: "structured.candidates", ToNode: "select", ToPort: "candidates"},
		},
		Outputs: []workflow.Output{{Name: "selected", Type: workflow.TypeArray, FromNode: "select", FromPort: "selectedCandidateIds", Required: true}},
	})
	started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"query":"topic"}`)})
	if err != nil {
		t.Fatal(err)
	}
	detail := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation)
	step := detail.Steps[1]
	for _, invalid := range []json.RawMessage{
		json.RawMessage(`{"selectedCandidateIds":["candidate-a","candidate-a"]}`),
		json.RawMessage(`{"selectedCandidateIds":["candidate-outside"]}`),
	} {
		if _, err := h.runtime.Decide(context.Background(), workflow.HumanDecisionCommand{ProjectID: h.project.ID, RunID: detail.Run.ID, StepID: step.ID, Approved: true, Context: invalid}); err == nil {
			t.Fatalf("invalid candidate selection was accepted: %s", invalid)
		}
	}
	if _, err := h.runtime.Decide(context.Background(), workflow.HumanDecisionCommand{ProjectID: h.project.ID, RunID: detail.Run.ID, StepID: step.ID, Approved: true, Context: json.RawMessage(`{"selectedCandidateIds":["candidate-b"]}`)}); err != nil {
		t.Fatal(err)
	}
	detail = waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunCompleted)
	if string(detail.Run.Outputs) != `{"selected":["candidate-b"]}` {
		t.Fatalf("candidate output = %s", detail.Run.Outputs)
	}
}

func TestWorkflowRuntimeCancelAndRecoverUnknownNonIdempotentStep(t *testing.T) {
	startedSignal := make(chan struct{})
	h := newWorkflowRuntimeHarness(t, runtimeFixtureTool{name: "fixture.block", risk: tool.RiskLow, idempotent: false, invoke: func(ctx context.Context, _ tool.Invocation) (tool.Result, error) {
		close(startedSignal)
		<-ctx.Done()
		return tool.Result{}, ctx.Err()
	}})
	saved := h.save(t, workflow.Definition{SchemaVersion: 1, Name: "Cancel", Inputs: []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}}, Nodes: []workflow.Node{{ID: "block", Name: "Block", Kind: workflow.NodeTool, ToolName: "fixture.block", Arguments: json.RawMessage(`{}`)}}, Edges: []workflow.Edge{{FromNode: "$input", FromPort: "topic", ToNode: "block", ToPort: "query"}}})
	started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"topic":"gamma"}`)})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-startedSignal:
	case <-time.After(5 * time.Second):
		t.Fatal("tool did not start")
	}
	if _, err := h.runtime.Cancel(context.Background(), h.project.ID, started.Run.ID); err != nil {
		t.Fatal(err)
	}
	detail := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunCancelled)
	if detail.Steps[0].Status != workflow.StepCancelled {
		t.Fatalf("cancelled Workflow = %#v", detail)
	}

	// Build a restart fixture at the decisive crash boundary: a non-idempotent
	// ToolCall was started, but no committed result exists.
	recovery := h.save(t, workflow.Definition{SchemaVersion: 1, Name: "Recover", Inputs: []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}}, Nodes: []workflow.Node{{ID: "block", Name: "Block", Kind: workflow.NodeTool, ToolName: "fixture.block", Arguments: json.RawMessage(`{}`)}}, Edges: []workflow.Edge{{FromNode: "$input", FromPort: "topic", ToNode: "block", ToPort: "query"}}})
	compilation, _ := json.Marshal(recovery.Version.Compilation)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	runID, stepID, callID := "recovery-run", "recovery-step", "recovery-call"
	recoveryInputs := `{"topic":"delta"}`
	recoveryInputsHash := fmt.Sprintf("%x", sha256.Sum256([]byte(recoveryInputs)))
	_, err = h.store.DB().Exec(`INSERT INTO workflow_runs(id,project_id,workflow_id,workflow_version_id,status,inputs_json,inputs_sha256,compilation_json,compilation_sha256,outputs_json,current_step_ordinal,created_at,started_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,'{}',0,?,?,?)`, runID, h.project.ID, recovery.Workflow.ID, recovery.Version.ID, workflow.RunRunning, recoveryInputs, recoveryInputsHash, string(compilation), recovery.Version.CompilationSHA256, now, now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.store.DB().Exec(`INSERT INTO tool_calls(id,workflow_run_id,provider_call_id,tool_name,tool_version,arguments_json,status,risk,permissions_json,idempotent,idempotency_key,created_at,started_at,updated_at) VALUES (?,?,'workflow:block:1','fixture.block','1','{"query":"delta"}','interrupted','low','[]',0,'recovery-key',?,?,?)`, callID, runID, now, now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.store.DB().Exec(`INSERT INTO workflow_steps(id,workflow_run_id,node_id,ordinal,node_kind,status,attempt,input_json,input_sha256,output_json,tool_call_id,idempotency_key,updated_at) VALUES (?,?,'block',0,'tool','running',1,'{"query":"delta"}',?,'{}',?,'recovery-key',?)`, stepID, runID, fmt.Sprintf("%064d", 1), callID, now)
	if err != nil {
		t.Fatal(err)
	}
	if recovered, err := h.runtime.Recover(context.Background()); err != nil || recovered < 1 {
		t.Fatalf("Recover() = %d, %v", recovered, err)
	}
	recoveredDetail := waitWorkflowStatus(t, h.runtime, h.project.ID, runID, workflow.RunInterrupted)
	if recoveredDetail.Steps[0].Status != workflow.StepOutcomeUnknown {
		t.Fatalf("recovered Workflow = %#v", recoveredDetail)
	}
	if _, err := h.runtime.Retry(context.Background(), workflow.RetryCommand{ProjectID: h.project.ID, RunID: runID, StepID: stepID}); err == nil {
		t.Fatal("non-idempotent retry did not require confirmation")
	}
}

func TestWorkflowRuntimeCancellationRejectsLateSuccessfulToolResult(t *testing.T) {
	startedSignal := make(chan struct{})
	releaseSignal := make(chan struct{})
	returnedSignal := make(chan struct{})
	h := newWorkflowRuntimeHarness(t, runtimeFixtureTool{name: "fixture.ignore.cancel", risk: tool.RiskLow, idempotent: true, invoke: func(context.Context, tool.Invocation) (tool.Result, error) {
		close(startedSignal)
		<-releaseSignal
		structured := json.RawMessage(`{"answer":"late success"}`)
		close(returnedSignal)
		return tool.Result{Status: tool.ResultSuccess, Text: "late success", Structured: structured, Artifacts: []tool.ArtifactRef{}, Citations: []tool.CitationRef{}}, nil
	}})
	saved := h.save(t, workflow.Definition{
		SchemaVersion: 1, Name: "Late result cancellation",
		Inputs: []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}},
		Nodes:  []workflow.Node{{ID: "execute", Name: "Execute", Kind: workflow.NodeTool, ToolName: "fixture.ignore.cancel", Arguments: json.RawMessage(`{}`)}},
		Edges:  []workflow.Edge{{FromNode: "$input", FromPort: "topic", ToNode: "execute", ToPort: "query"}},
	})
	started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"topic":"cancel"}`)})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-startedSignal:
	case <-time.After(5 * time.Second):
		t.Fatal("tool did not start")
	}
	if _, err := h.runtime.Cancel(context.Background(), h.project.ID, started.Run.ID); err != nil {
		t.Fatal(err)
	}
	cancelled := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunCancelled)
	if cancelled.Steps[0].Status != workflow.StepCancelled || cancelled.Steps[0].ToolCallID == "" {
		t.Fatalf("cancelled Workflow = %#v", cancelled)
	}
	close(releaseSignal)
	select {
	case <-returnedSignal:
	case <-time.After(5 * time.Second):
		t.Fatal("tool did not return its late result")
	}
	h.runtime.Wait()

	after, err := h.runtime.Get(context.Background(), h.project.ID, started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Run.Status != workflow.RunCancelled || after.Steps[0].Status != workflow.StepCancelled || string(after.Run.Outputs) != `{}` {
		t.Fatalf("late result changed cancelled Workflow = %#v", after)
	}
	call, err := tool.NewService(NewToolRepository(h.store.DB()), tool.JSONSchemaValidator{}).Get(context.Background(), after.Steps[0].ToolCallID)
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != tool.CallCancelled || call.Result != nil {
		t.Fatalf("late result changed cancelled ToolCall = %#v", call)
	}
	var resultCount int
	if err := h.store.DB().QueryRow(`SELECT COUNT(*) FROM tool_results WHERE tool_call_id=?`, call.ID).Scan(&resultCount); err != nil || resultCount != 0 {
		t.Fatalf("late ToolResult count = %d, %v", resultCount, err)
	}
}

func TestWorkflowReviewRevisionAtomicallyResetsProducerReviewAndGate(t *testing.T) {
	h := newWorkflowRuntimeHarness(t, runtimeFixtureTool{name: "fixture.review.revision", risk: tool.RiskLow, idempotent: true})
	saved := h.save(t, workflow.Definition{
		SchemaVersion: 1, Name: "Review revision reset",
		Inputs: []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}},
		Nodes: []workflow.Node{
			{ID: "prepare", Name: "Prepare", Kind: workflow.NodeTool, ToolName: "fixture.review.revision", Arguments: json.RawMessage(`{}`)},
			{ID: "produce", Name: "Produce", Kind: workflow.NodeTool, ToolName: "fixture.review.revision", Arguments: json.RawMessage(`{}`)},
			{ID: "review", Name: "Review", Kind: workflow.NodeTool, ToolName: "fixture.review.revision", Arguments: json.RawMessage(`{}`)},
			{ID: "gate", Name: "Gate", Kind: workflow.NodeTool, ToolName: "fixture.review.revision", Arguments: json.RawMessage(`{}`)},
		},
		Edges: []workflow.Edge{
			{FromNode: "$input", FromPort: "topic", ToNode: "prepare", ToPort: "query"},
			{FromNode: "prepare", FromPort: "structured.answer", ToNode: "produce", ToPort: "query"},
			{FromNode: "produce", FromPort: "structured.answer", ToNode: "review", ToPort: "query"},
			{FromNode: "review", FromPort: "structured.answer", ToNode: "gate", ToPort: "query"},
		},
		Outputs: []workflow.Output{{Name: "answer", Type: workflow.TypeString, FromNode: "gate", FromPort: "structured.answer", Required: true}},
	})
	started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"topic":"revision"}`)})
	if err != nil {
		t.Fatal(err)
	}
	completed := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunCompleted)
	if len(completed.Steps) != 4 {
		t.Fatalf("steps = %#v", completed.Steps)
	}
	now := time.Now().UTC()
	if _, err := h.store.DB().Exec(`UPDATE workflow_steps SET status='failed',error_code='REVIEW_REJECTED',error_message='review rejected' WHERE id=?`, completed.Steps[3].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.DB().Exec(`UPDATE workflow_runs SET status='failed',current_step_ordinal=3,error_code='REVIEW_REJECTED',error_message='review rejected' WHERE id=?`, completed.Run.ID); err != nil {
		t.Fatal(err)
	}
	repository := NewWorkflowRuntimeRepository(h.store.DB())
	event := workflow.RuntimeEvent{ID: "review-revision-event", WorkflowRunID: completed.Run.ID, Type: "workflow.review_revision_queued", Payload: json.RawMessage(`{"producerStepId":"` + completed.Steps[1].ID + `"}`), CreatedAt: now}
	if err := repository.ResetStepsForReviewRevision(context.Background(), completed.Run.ID, completed.Steps[1].ID, completed.Steps[3].ID, now, event); err != nil {
		t.Fatal(err)
	}
	reset, err := repository.GetRun(context.Background(), h.project.ID, completed.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reset.Run.Status != workflow.RunQueued || reset.Run.CurrentStep != 1 || string(reset.Run.Outputs) != `{}` || reset.Steps[0].Status != workflow.StepCompleted {
		t.Fatalf("reset run = %#v", reset)
	}
	for _, step := range reset.Steps[1:] {
		if step.Status != workflow.StepQueued || string(step.Input) != `{}` || string(step.Output) != `{}` || step.ToolCallID != "" || step.ErrorCode != "" {
			t.Fatalf("reset step = %#v", step)
		}
	}
	if reset.Events[len(reset.Events)-1].Type != "workflow.review_revision_queued" {
		t.Fatalf("events = %#v", reset.Events)
	}
}

func TestWorkflowUpstreamRevisionResetsOnlyProducerThroughFailure(t *testing.T) {
	h := newWorkflowRuntimeHarness(t, runtimeFixtureTool{name: "fixture.upstream.revision", risk: tool.RiskLow, idempotent: true})
	saved := h.save(t, workflow.Definition{
		SchemaVersion: 1, Name: "Upstream revision reset",
		Inputs: []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}},
		Nodes: []workflow.Node{
			{ID: "keep", Name: "Keep", Kind: workflow.NodeTool, ToolName: "fixture.upstream.revision", Arguments: json.RawMessage(`{}`)},
			{ID: "produce", Name: "Produce", Kind: workflow.NodeTool, ToolName: "fixture.upstream.revision", Arguments: json.RawMessage(`{}`)},
			{ID: "prepare", Name: "Prepare", Kind: workflow.NodeTool, ToolName: "fixture.upstream.revision", Arguments: json.RawMessage(`{}`)},
			{ID: "consume", Name: "Consume", Kind: workflow.NodeTool, ToolName: "fixture.upstream.revision", Arguments: json.RawMessage(`{}`)},
		},
		Edges: []workflow.Edge{
			{FromNode: "$input", FromPort: "topic", ToNode: "keep", ToPort: "query"},
			{FromNode: "keep", FromPort: "structured.answer", ToNode: "produce", ToPort: "query"},
			{FromNode: "produce", FromPort: "structured.answer", ToNode: "prepare", ToPort: "query"},
			{FromNode: "prepare", FromPort: "structured.answer", ToNode: "consume", ToPort: "query"},
		},
		Outputs: []workflow.Output{{Name: "answer", Type: workflow.TypeString, FromNode: "consume", FromPort: "structured.answer", Required: true}},
	})
	started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"topic":"revision"}`)})
	if err != nil {
		t.Fatal(err)
	}
	completed := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunCompleted)
	if _, err := h.store.DB().Exec(`UPDATE workflow_steps SET status='failed',error_code='EXEC_FAILED',error_message='failed' WHERE id=?`, completed.Steps[3].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.DB().Exec(`UPDATE workflow_runs SET status='failed',current_step_ordinal=3,error_code='EXEC_FAILED',error_message='failed',outputs_json='{}' WHERE id=?`, completed.Run.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	repository := NewWorkflowRuntimeRepository(h.store.DB())
	event := workflow.RuntimeEvent{ID: "upstream-revision-event", WorkflowRunID: completed.Run.ID, Type: "workflow.upstream_revision_queued", Payload: json.RawMessage(`{"producerStepId":"` + completed.Steps[1].ID + `"}`), CreatedAt: now}
	if err := repository.ResetStepsForUpstreamRetry(context.Background(), completed.Run.ID, completed.Steps[1].ID, completed.Steps[3].ID, nil, now, event); err != nil {
		t.Fatal(err)
	}
	reset, err := repository.GetRun(context.Background(), h.project.ID, completed.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reset.Run.Status != workflow.RunQueued || reset.Run.CurrentStep != 1 || reset.Steps[0].Status != workflow.StepCompleted || reset.Steps[0].ToolCallID == "" {
		t.Fatalf("reset run = %#v", reset)
	}
	for _, step := range reset.Steps[1:] {
		if step.Status != workflow.StepQueued || string(step.Input) != `{}` || string(step.Output) != `{}` || step.ToolCallID != "" || step.ErrorCode != "" {
			t.Fatalf("reset step = %#v", step)
		}
	}
	var historicalCalls int
	if err := h.store.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE workflow_run_id=?`, completed.Run.ID).Scan(&historicalCalls); err != nil || historicalCalls != 4 {
		t.Fatalf("historical ToolCalls = %d, %v", historicalCalls, err)
	}
}

func TestQueueAutomaticPythonRepairIsAtomicAndKeepsRunQueued(t *testing.T) {
	h := newWorkflowRuntimeHarness(t, runtimeFixtureTool{name: "fixture.automatic.repair", risk: tool.RiskLow, idempotent: true})
	saved := h.save(t, workflow.Definition{
		SchemaVersion: 1, Name: "Automatic repair", Inputs: []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}},
		Nodes: []workflow.Node{
			{ID: "producer", Name: "Producer", Kind: workflow.NodeTool, ToolName: "fixture.automatic.repair", Arguments: json.RawMessage(`{}`)},
			{ID: "prepare", Name: "Prepare", Kind: workflow.NodeTool, ToolName: "fixture.automatic.repair", Arguments: json.RawMessage(`{}`)},
			{ID: "python", Name: "Python", Kind: workflow.NodeTool, ToolName: "fixture.automatic.repair", Arguments: json.RawMessage(`{}`)},
		},
		Edges: []workflow.Edge{
			{FromNode: "$input", FromPort: "topic", ToNode: "producer", ToPort: "query"},
			{FromNode: "producer", FromPort: "structured.answer", ToNode: "prepare", ToPort: "query"},
			{FromNode: "prepare", FromPort: "structured.answer", ToNode: "python", ToPort: "query"},
		},
		Outputs: []workflow.Output{{Name: "answer", Type: workflow.TypeString, FromNode: "python", FromPort: "structured.answer", Required: true}},
	})
	now := time.Now().UTC()
	runID := "automatic-repair-run"
	producerID, failedID := "automatic-producer", "automatic-failed"
	compilation := saved.Version.Compilation
	encodedCompilation, _ := json.Marshal(compilation)
	compilationHash := sha256.Sum256(encodedCompilation)
	// Insert the minimal durable state needed to exercise the atomic repository
	// transition without invoking a model or Python process.
	if _, err := h.store.DB().Exec(`INSERT INTO workflow_runs(id,project_id,workflow_id,workflow_version_id,workflow_name,workflow_purpose,status,permission_mode,inputs_json,inputs_sha256,compilation_json,compilation_sha256,outputs_json,current_step_ordinal,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, runID, h.project.ID, saved.Workflow.ID, saved.Version.ID, "Automatic repair", "research_starter", workflow.RunFailed, "full_access", `{}`, strings.Repeat("0", 64), string(encodedCompilation), fmt.Sprintf("%x", compilationHash), `{}`, 2, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		id, node string
		ordinal  int
		status   workflow.StepStatus
	}{{producerID, "producer", 0, workflow.StepCompleted}, {"automatic-prepare", "prepare", 1, workflow.StepCompleted}, {failedID, "python", 2, workflow.StepRunning}} {
		if _, err := h.store.DB().Exec(`INSERT INTO workflow_steps(id,workflow_run_id,node_id,ordinal,node_kind,status,attempt,input_json,input_sha256,output_json,idempotency_key,error_code,error_message,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, step.id, runID, step.node, step.ordinal, string(compilation.Nodes[step.ordinal].Kind), step.status, 1, `{}`, strings.Repeat("0", 64), `{}`, "", "", "", formatTime(now)); err != nil {
			t.Fatal(err)
		}
	}
	event := workflow.RuntimeEvent{ID: "automatic-repair-event", WorkflowRunID: runID, Type: "workflow.upstream_revision_queued", Payload: json.RawMessage(`{"automatic":true}`), CreatedAt: now}
	repository := NewWorkflowRuntimeRepository(h.store.DB())
	if err := repository.QueueAutomaticPythonRepair(context.Background(), runID, producerID, failedID, now, event); err != nil {
		t.Fatal(err)
	}
	reset, err := repository.GetRun(context.Background(), h.project.ID, runID)
	if err != nil {
		t.Fatal(err)
	}
	if reset.Run.Status != workflow.RunQueued || reset.Run.CurrentStep != 0 || reset.Run.ErrorCode != "" || reset.Run.ErrorMessage != "" {
		t.Fatalf("automatic repair run = %#v", reset.Run)
	}
	for _, step := range reset.Steps {
		if step.Status != workflow.StepQueued || step.ToolCallID != "" || step.ErrorCode != "" {
			t.Fatalf("automatic repair step = %#v", step)
		}
	}
}

func TestQueueAutomaticAIOutputRepairKeepsFailedAttemptAndRequeuesStep(t *testing.T) {
	h := newWorkflowRuntimeHarness(t)
	saved := h.save(t, workflow.Definition{
		SchemaVersion: 1, Name: "AI output repair", Inputs: []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}},
		Nodes:   []workflow.Node{{ID: "plan", Name: "Plan", Kind: workflow.NodeAIAnalysis, Prompt: "Return JSON", PromptVersion: "plan-v1", Arguments: json.RawMessage(`{}`), OutputSchema: json.RawMessage(`{"type":"object"}`)}},
		Edges:   []workflow.Edge{{FromNode: "$input", FromPort: "topic", ToNode: "plan", ToPort: "context"}},
		Outputs: []workflow.Output{{Name: "plan", Type: workflow.TypeObject, FromNode: "plan", FromPort: "analysis", Required: true}},
	})
	now := time.Now().UTC()
	runID, stepID := "automatic-ai-output-repair-run", "automatic-ai-output-repair-step"
	encodedCompilation, _ := json.Marshal(saved.Version.Compilation)
	compilationHash := sha256.Sum256(encodedCompilation)
	if _, err := h.store.DB().Exec(`INSERT INTO workflow_runs(id,project_id,workflow_id,workflow_version_id,workflow_name,workflow_purpose,status,permission_mode,inputs_json,inputs_sha256,compilation_json,compilation_sha256,outputs_json,current_step_ordinal,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, runID, h.project.ID, saved.Workflow.ID, saved.Version.ID, "AI output repair", "user_plan", workflow.RunRunning, "full_access", `{"topic":"x"}`, strings.Repeat("0", 64), string(encodedCompilation), fmt.Sprintf("%x", compilationHash), `{}`, 0, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.DB().Exec(`INSERT INTO workflow_steps(id,workflow_run_id,node_id,ordinal,node_kind,status,attempt,input_json,input_sha256,output_json,idempotency_key,error_code,error_message,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, stepID, runID, "plan", 0, string(workflow.NodeAIAnalysis), workflow.StepRunning, 1, `{"context":"x"}`, strings.Repeat("1", 64), `{}`, "key", "", "", formatTime(now)); err != nil {
		t.Fatal(err)
	}
	event := workflow.RuntimeEvent{ID: "automatic-ai-output-repair-event", WorkflowRunID: runID, Type: "workflow.ai_output_repair_queued", Payload: json.RawMessage(`{"automatic":true}`), CreatedAt: now}
	repository := NewWorkflowRuntimeRepository(h.store.DB())
	if err := repository.QueueAutomaticAIOutputRepair(context.Background(), runID, stepID, 1, now, event); err != nil {
		t.Fatal(err)
	}
	reset, err := repository.GetRun(context.Background(), h.project.ID, runID)
	if err != nil {
		t.Fatal(err)
	}
	if reset.Run.Status != workflow.RunQueued || reset.Run.CurrentStep != 0 || reset.Steps[0].Status != workflow.StepQueued || reset.Steps[0].Attempt != 1 || string(reset.Steps[0].Input) != `{}` || reset.Steps[0].ErrorCode != "" {
		t.Fatalf("automatic AI output repair state = %#v / %#v", reset.Run, reset.Steps[0])
	}
}

func TestWorkflowRuntimeConcurrentHumanDecisionAndPauseCancelConverge(t *testing.T) {
	h := newWorkflowRuntimeHarness(t)
	saved := h.save(t, workflow.Definition{
		SchemaVersion: 1, Name: "Concurrent human decision",
		Inputs:  []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}},
		Nodes:   []workflow.Node{{ID: "confirm", Name: "Confirm", Kind: workflow.NodeHumanConfirmation, Prompt: "Continue?", Arguments: json.RawMessage(`{}`)}},
		Edges:   []workflow.Edge{{FromNode: "$input", FromPort: "topic", ToNode: "confirm", ToPort: "context"}},
		Outputs: []workflow.Output{{Name: "approved", Type: workflow.TypeBoolean, FromNode: "confirm", FromPort: "approved", Required: true}},
	})
	started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"topic":"alpha"}`)})
	if err != nil {
		t.Fatal(err)
	}
	detail := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation)

	const attempts = 12
	start := make(chan struct{})
	errorsByAttempt := make(chan error, attempts)
	var group sync.WaitGroup
	for range attempts {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, decideErr := h.runtime.Decide(context.Background(), workflow.HumanDecisionCommand{ProjectID: h.project.ID, RunID: detail.Run.ID, StepID: detail.Steps[0].ID, Approved: true, Note: "confirmed"})
			errorsByAttempt <- decideErr
		}()
	}
	close(start)
	group.Wait()
	close(errorsByAttempt)
	successes := 0
	for decideErr := range errorsByAttempt {
		if decideErr == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent human decisions succeeded %d times, want 1", successes)
	}
	detail = waitWorkflowStatus(t, h.runtime, h.project.ID, detail.Run.ID, workflow.RunCompleted)
	var decisions int
	if err := h.store.DB().QueryRow(`SELECT COUNT(*) FROM workflow_human_decisions WHERE workflow_run_id=?`, detail.Run.ID).Scan(&decisions); err != nil || decisions != 1 {
		t.Fatalf("persisted human decisions = %d, %v", decisions, err)
	}
	assertWorkflowEventSequence(t, h.store.DB(), detail.Run.ID)

	second, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"topic":"beta"}`)})
	if err != nil {
		t.Fatal(err)
	}
	secondDetail := waitWorkflowStatus(t, h.runtime, h.project.ID, second.Run.ID, workflow.RunWaitingHumanConfirmation)
	start = make(chan struct{})
	resultErrors := make(chan error, 2)
	group.Add(2)
	go func() {
		defer group.Done()
		<-start
		_, pauseErr := h.runtime.Pause(context.Background(), h.project.ID, secondDetail.Run.ID)
		resultErrors <- pauseErr
	}()
	go func() {
		defer group.Done()
		<-start
		_, cancelErr := h.runtime.Cancel(context.Background(), h.project.ID, secondDetail.Run.ID)
		resultErrors <- cancelErr
	}()
	close(start)
	group.Wait()
	close(resultErrors)
	for operationErr := range resultErrors {
		if operationErr != nil && !strings.Contains(operationErr.Error(), "transition conflict") {
			t.Fatalf("pause/cancel race returned unexpected error: %v", operationErr)
		}
	}
	secondDetail = waitWorkflowStatus(t, h.runtime, h.project.ID, secondDetail.Run.ID, workflow.RunCancelled)
	if secondDetail.Steps[0].Status != workflow.StepCancelled {
		t.Fatalf("pause/cancel race did not converge to cancelled: %#v", secondDetail)
	}
	assertWorkflowEventSequence(t, h.store.DB(), secondDetail.Run.ID)
}

func TestWorkflowRuntimeConcurrentApprovalResolutionHasSingleWinner(t *testing.T) {
	h := newWorkflowRuntimeHarness(t, runtimeFixtureTool{name: "fixture.concurrent.approval", risk: tool.RiskHigh, idempotent: true, permissions: []tool.PermissionRequirement{{Kind: tool.PermissionProcessExecute}}})
	saved := h.save(t, workflow.Definition{
		SchemaVersion: 1, Name: "Concurrent approval",
		Inputs:  []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}},
		Nodes:   []workflow.Node{{ID: "execute", Name: "Execute", Kind: workflow.NodeTool, ToolName: "fixture.concurrent.approval", Arguments: json.RawMessage(`{}`)}},
		Edges:   []workflow.Edge{{FromNode: "$input", FromPort: "topic", ToNode: "execute", ToPort: "query"}},
		Outputs: []workflow.Output{{Name: "answer", Type: workflow.TypeString, FromNode: "execute", FromPort: "structured.answer", Required: true}},
	})
	started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"topic":"approval"}`)})
	if err != nil {
		t.Fatal(err)
	}
	detail := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunWaitingApproval)
	if len(detail.PendingApprovals) != 1 {
		t.Fatalf("pending approvals = %#v", detail.PendingApprovals)
	}
	approvalID := detail.PendingApprovals[0].ID
	const attempts = 10
	start := make(chan struct{})
	errorsByAttempt := make(chan error, attempts)
	var group sync.WaitGroup
	for range attempts {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, resolveErr := h.runtime.ResolveApproval(context.Background(), permission.ResolveCommand{ApprovalID: approvalID, Allow: true, Scope: permission.ScopeCall})
			errorsByAttempt <- resolveErr
		}()
	}
	close(start)
	group.Wait()
	close(errorsByAttempt)
	successes := 0
	for resolveErr := range errorsByAttempt {
		if resolveErr == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent approval resolutions succeeded %d times, want 1", successes)
	}
	detail = waitWorkflowStatus(t, h.runtime, h.project.ID, detail.Run.ID, workflow.RunWaitingApproval, workflow.RunCompleted)
	for detail.Run.Status == workflow.RunWaitingApproval {
		if len(detail.PendingApprovals) != 1 {
			t.Fatalf("next pending approval = %#v", detail.PendingApprovals)
		}
		detail, err = h.runtime.ResolveApproval(context.Background(), permission.ResolveCommand{ApprovalID: detail.PendingApprovals[0].ID, Allow: true, Scope: permission.ScopeCall})
		if err != nil {
			t.Fatal(err)
		}
		detail = waitWorkflowStatus(t, h.runtime, h.project.ID, detail.Run.ID, workflow.RunWaitingApproval, workflow.RunCompleted)
	}
	if detail.Run.Status != workflow.RunCompleted {
		t.Fatalf("Workflow did not complete after approval race: %#v", detail)
	}
	var firstApprovalCount int
	if err := h.store.DB().QueryRow(`SELECT COUNT(*) FROM approvals WHERE id=? AND status='granted'`, approvalID).Scan(&firstApprovalCount); err != nil || firstApprovalCount != 1 {
		t.Fatalf("resolved first approval count = %d, %v", firstApprovalCount, err)
	}
	assertWorkflowEventSequence(t, h.store.DB(), detail.Run.ID)
}

func TestWorkflowRuntimeReopensSQLiteAndRetriesOnlyUncommittedIdempotentStep(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(dataRoot, "workflow-restart.db")
	workspaceRoot, trashRoot := filepath.Join(dataRoot, "workspaces"), filepath.Join(root, "trash")
	var firstCalls, secondCalls atomic.Int32
	secondStarted := make(chan struct{}, 1)
	fixtures := []runtimeFixtureTool{
		{name: "fixture.restart.first", risk: tool.RiskLow, idempotent: true, invoke: func(_ context.Context, invocation tool.Invocation) (tool.Result, error) {
			firstCalls.Add(1)
			var arguments struct {
				Query string `json:"query"`
			}
			_ = json.Unmarshal(invocation.Arguments, &arguments)
			structured, _ := json.Marshal(map[string]string{"answer": "first:" + arguments.Query})
			return tool.Result{Status: tool.ResultSuccess, Structured: structured, Artifacts: []tool.ArtifactRef{}, Citations: []tool.CitationRef{}}, nil
		}},
		{name: "fixture.restart.second", risk: tool.RiskLow, idempotent: true, invoke: func(callCtx context.Context, invocation tool.Invocation) (tool.Result, error) {
			attempt := secondCalls.Add(1)
			if attempt == 1 {
				secondStarted <- struct{}{}
				<-callCtx.Done()
				return tool.Result{}, callCtx.Err()
			}
			var arguments struct {
				Query string `json:"query"`
			}
			_ = json.Unmarshal(invocation.Arguments, &arguments)
			structured, _ := json.Marshal(map[string]string{"answer": "second:" + arguments.Query})
			return tool.Result{Status: tool.ResultSuccess, Structured: structured, Artifacts: []tool.ArtifactRef{}, Citations: []tool.CitationRef{}}, nil
		}},
	}

	firstStore, firstProjects, firstDefinitions, firstRuntime, _ := openWorkflowRuntimeStack(t, databasePath, workspaceRoot, trashRoot, fixtures...)
	selected, err := firstProjects.Create(ctx, "Restart Workflow", "")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := firstDefinitions.Save(ctx, workflow.SaveCommand{ProjectID: selected.ID, Definition: workflow.Definition{
		SchemaVersion: 1, Name: "Restart checkpoint",
		Inputs: []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}},
		Nodes: []workflow.Node{
			{ID: "first", Name: "First", Kind: workflow.NodeTool, ToolName: "fixture.restart.first", Arguments: json.RawMessage(`{}`)},
			{ID: "second", Name: "Second", Kind: workflow.NodeTool, ToolName: "fixture.restart.second", Arguments: json.RawMessage(`{}`)},
		},
		Edges: []workflow.Edge{
			{FromNode: "$input", FromPort: "topic", ToNode: "first", ToPort: "query"},
			{FromNode: "first", FromPort: "structured.answer", ToNode: "second", ToPort: "query"},
		},
		Outputs: []workflow.Output{{Name: "answer", Type: workflow.TypeString, FromNode: "second", FromPort: "structured.answer", Required: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	started, err := firstRuntime.Start(ctx, workflow.StartCommand{ProjectID: selected.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"topic":"restart"}`)})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("second Workflow step did not start")
	}
	if err := firstRuntime.Close(); err != nil {
		t.Fatal(err)
	}
	beforeClose, err := NewWorkflowRuntimeRepository(firstStore.DB()).GetRun(ctx, selected.ID, started.Run.ID)
	if err != nil || len(beforeClose.Steps) != 2 || beforeClose.Steps[0].Status != workflow.StepCompleted || beforeClose.Steps[1].Status != workflow.StepRunning {
		t.Fatalf("durable checkpoint before reopen = %#v, %v", beforeClose, err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	secondStore, _, _, secondRuntime, secondTools := openWorkflowRuntimeStack(t, databasePath, workspaceRoot, trashRoot, fixtures...)
	defer secondRuntime.Close()
	defer secondStore.Close()
	if interrupted, err := NewToolRepository(secondStore.DB()).InterruptActive(ctx, time.Now().UTC()); err != nil || interrupted != 0 {
		// The orderly Runtime close persisted the first attempt as cancelled. A
		// real crash would instead convert a running call to interrupted here;
		// both statuses are safe to retry because the Tool snapshot is idempotent.
		t.Fatalf("interrupt active Workflow tools = %d, %v", interrupted, err)
	}
	if recovered, err := secondRuntime.Recover(ctx); err != nil || recovered != 1 {
		t.Fatalf("Recover() after SQLite reopen = %d, %v", recovered, err)
	}
	completed := waitWorkflowStatus(t, secondRuntime, selected.ID, started.Run.ID, workflow.RunCompleted)
	if firstCalls.Load() != 1 || secondCalls.Load() != 2 {
		t.Fatalf("tool replay counts first=%d second=%d", firstCalls.Load(), secondCalls.Load())
	}
	if len(completed.Steps) != 2 || completed.Steps[0].Attempt != 1 || completed.Steps[1].Attempt != 2 || completed.Steps[0].Status != workflow.StepCompleted || completed.Steps[1].Status != workflow.StepCompleted {
		t.Fatalf("recovered Workflow checkpoint = %#v", completed)
	}
	calls, err := secondTools.ListBySubject(ctx, tool.SubjectWorkflowRun, completed.Run.ID)
	if err != nil || len(calls) != 3 {
		t.Fatalf("recovered Workflow ToolCalls = %#v, %v", calls, err)
	}
	assertWorkflowEventSequence(t, secondStore.DB(), completed.Run.ID)
	var foreignKeys int
	if err := secondStore.DB().QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignKeys); err != nil || foreignKeys != 0 {
		t.Fatalf("foreign key check after Workflow restart = %d, %v", foreignKeys, err)
	}
}

func openWorkflowRuntimeStack(t *testing.T, databasePath, workspaceRoot, trashRoot string, fixtures ...runtimeFixtureTool) (*Store, *project.Service, *workflow.Service, *workflow.RuntimeService, *tool.Service) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	projects := project.NewService(NewProjectRepository(store.DB()), workspaceRoot, trashRoot)
	registry := tool.NewRegistry()
	for _, fixture := range fixtures {
		if err := registry.Register(ctx, fixture); err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
	}
	definitionRepo := NewWorkflowRepository(store.DB())
	definitions, err := workflow.NewService(definitionRepo, projects, workflow.NewCompiler(registry))
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	toolService := tool.NewService(NewToolRepository(store.DB()), tool.JSONSchemaValidator{})
	runtimeRepo := NewWorkflowRuntimeRepository(store.DB())
	executor := tool.NewExecutor(registry, toolService, tool.CompositeProjectResolver{Runs: NewRunRepository(store.DB()), Workflows: runtimeRepo}, tool.ExecutorOptions{})
	if err := executor.SetArtifactRegistrar(artifact.NewService(NewArtifactRepository(store.DB()), projects)); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	runtimeService, err := workflow.NewRuntimeService(runtimeRepo, definitionRepo, projects, registry, toolService, permission.NewEngine(NewPermissionRepository(store.DB())), executor)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	return store, projects, definitions, runtimeService, toolService
}

func assertWorkflowEventSequence(t *testing.T, db *sql.DB, runID string) {
	t.Helper()
	rows, err := db.Query(`SELECT sequence FROM workflow_events WHERE workflow_run_id=? ORDER BY sequence`, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	expected := 1
	for rows.Next() {
		var sequence int
		if err := rows.Scan(&sequence); err != nil {
			t.Fatal(err)
		}
		if sequence != expected {
			t.Fatalf("Workflow event sequence = %d at ordinal %d", sequence, expected)
		}
		expected++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if expected == 1 {
		t.Fatal("Workflow emitted no events")
	}
}

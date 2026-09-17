package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

type workflowFixtureTool struct{ version string }

func (f workflowFixtureTool) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{
		QualifiedName: "fixture.workflow",
		Description:   "Workflow fixture",
		Version:       f.version,
		Risk:          tool.RiskLow,
		Idempotent:    true,
		Permissions:   []tool.PermissionRequirement{},
		InputSchema:   json.RawMessage(`{"type":"object","required":["query"],"properties":{"query":{"type":"string"}}}`),
		OutputSchema:  json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}}}`),
	}, nil
}

func (f workflowFixtureTool) Invoke(context.Context, tool.Invocation) (tool.Result, error) {
	return tool.Result{Status: tool.ResultSuccess}, nil
}

func TestWorkflowRepositoryVersionsAreImmutableIdempotentAndProjectScoped(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	firstProject, err := projects.Create(ctx, "Workflow project", "")
	if err != nil {
		t.Fatal(err)
	}
	secondProject, err := projects.Create(ctx, "Other project", "")
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	if err := registry.Register(ctx, workflowFixtureTool{version: "1"}); err != nil {
		t.Fatal(err)
	}
	service, err := workflow.NewService(NewWorkflowRepository(store.DB()), projects, workflow.NewCompiler(registry))
	if err != nil {
		t.Fatal(err)
	}
	definition := workflow.Definition{
		SchemaVersion: 1,
		Name:          "Literature review",
		Inputs:        []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}},
		Nodes: []workflow.Node{{
			ID: "search", Name: "Search", Kind: workflow.NodeTool, ToolName: "fixture.workflow", Arguments: json.RawMessage(`{}`),
		}},
		Edges:   []workflow.Edge{{FromNode: "$input", FromPort: "topic", ToNode: "search", ToPort: "query"}},
		Outputs: []workflow.Output{{Name: "answer", Type: workflow.TypeString, FromNode: "search", FromPort: "structured.answer", Required: true}},
	}
	created, err := service.Save(ctx, workflow.SaveCommand{ProjectID: firstProject.ID, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	if !created.Created || created.Workflow.Version != 1 || created.Version.Version != 1 || created.Workflow.CurrentVersionID != created.Version.ID {
		t.Fatalf("created Workflow = %#v", created)
	}

	reused, err := service.Save(ctx, workflow.SaveCommand{ProjectID: firstProject.ID, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	if reused.Created || reused.Workflow.ID != created.Workflow.ID || reused.Version.ID != created.Version.ID {
		t.Fatalf("duplicate template save = %#v, want existing Workflow %s", reused, created.Workflow.ID)
	}
	var workflowCount, versionCount int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows WHERE project_id=?`, firstProject.ID).Scan(&workflowCount); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_versions WHERE workflow_id=?`, created.Workflow.ID).Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if workflowCount != 1 || versionCount != 1 {
		t.Fatalf("duplicate template persisted rows: workflows=%d versions=%d", workflowCount, versionCount)
	}

	distinctDefinition := definition
	distinctDefinition.Description = "A separately customized research plan"
	distinct, err := service.Save(ctx, workflow.SaveCommand{ProjectID: firstProject.ID, Definition: distinctDefinition})
	if err != nil {
		t.Fatal(err)
	}
	if !distinct.Created || distinct.Workflow.ID == created.Workflow.ID || distinct.Version.DefinitionSHA256 == created.Version.DefinitionSHA256 {
		t.Fatalf("edited template was incorrectly reused: %#v", distinct)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows WHERE project_id=?`, firstProject.ID).Scan(&workflowCount); err != nil {
		t.Fatal(err)
	}
	if workflowCount != 2 {
		t.Fatalf("edited template workflow count = %d, want 2", workflowCount)
	}

	otherProjectCopy, err := service.Save(ctx, workflow.SaveCommand{ProjectID: secondProject.ID, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	if !otherProjectCopy.Created || otherProjectCopy.Workflow.ID == created.Workflow.ID {
		t.Fatalf("cross-project template save was incorrectly reused: %#v", otherProjectCopy)
	}

	idempotent, err := service.Save(ctx, workflow.SaveCommand{
		ProjectID: firstProject.ID, WorkflowID: created.Workflow.ID, ExpectedCurrentVersionID: created.Version.ID, Definition: definition,
	})
	if err != nil {
		t.Fatal(err)
	}
	if idempotent.Version.ID != created.Version.ID || idempotent.Workflow.Version != 1 {
		t.Fatalf("idempotent save created a version: %#v", idempotent)
	}

	definition.Description = "Updated deterministic workflow"
	updated, err := service.Save(ctx, workflow.SaveCommand{
		ProjectID: firstProject.ID, WorkflowID: created.Workflow.ID, ExpectedCurrentVersionID: created.Version.ID, Definition: definition,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version.Version != 2 || updated.Workflow.CurrentVersionID != updated.Version.ID || updated.Version.ID == created.Version.ID {
		t.Fatalf("updated Workflow = %#v", updated)
	}
	if _, err := service.Save(ctx, workflow.SaveCommand{
		ProjectID: firstProject.ID, WorkflowID: created.Workflow.ID, ExpectedCurrentVersionID: created.Version.ID, Definition: definition,
	}); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("stale edit error = %v", err)
	}
	if _, err := service.Save(ctx, workflow.SaveCommand{
		ProjectID: secondProject.ID, WorkflowID: created.Workflow.ID, ExpectedCurrentVersionID: updated.Version.ID, Definition: definition,
	}); err == nil || !strings.Contains(err.Error(), "current project") {
		t.Fatalf("cross-project edit error = %v", err)
	}

	detail, err := service.Get(ctx, firstProject.ID, created.Workflow.ID)
	if err != nil || len(detail.Versions) != 2 || detail.Versions[0].Version != 2 || detail.Versions[1].Version != 1 {
		t.Fatalf("Workflow detail = %#v, %v", detail, err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE workflow_versions SET definition_json='{}' WHERE id=?`, created.Version.ID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("immutable version update error = %v", err)
	}
	if _, err := projects.Remove(ctx, firstProject.ID); err != nil {
		t.Fatalf("project deletion was blocked by Workflow references: %v", err)
	}
	var count int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows WHERE project_id=?`, firstProject.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("Workflow cascade count = %d, %v", count, err)
	}
}

func TestWorkflowRepositoryDeletesLegacyEquivalentPlansAtomically(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workflow-equivalent-delete.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(t.TempDir(), "workspaces"), filepath.Join(t.TempDir(), "trash"))
	selected, err := projects.Create(ctx, "Equivalent plans", "")
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	if err := registry.Register(ctx, workflowFixtureTool{version: "1"}); err != nil {
		t.Fatal(err)
	}
	compiler := workflow.NewCompiler(registry)
	service, err := workflow.NewService(NewWorkflowRepository(store.DB()), projects, compiler)
	if err != nil {
		t.Fatal(err)
	}
	definition := workflow.Definition{SchemaVersion: 1, Name: "Legacy duplicate", Nodes: []workflow.Node{{ID: "search", Name: "Search", Kind: workflow.NodeTool, ToolName: "fixture.workflow", Arguments: json.RawMessage(`{"query":"fixed"}`)}}}
	first, err := service.Save(ctx, workflow.SaveCommand{ProjectID: selected.ID, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(ctx, definition)
	if err != nil {
		t.Fatal(err)
	}
	now := formatTime(time.Now().UTC())
	secondWorkflowID, secondVersionID := "legacy-duplicate-workflow", "legacy-duplicate-version"
	definitionJSON, _ := json.Marshal(definition)
	compilationJSON, _ := json.Marshal(compiled)
	tx, err := store.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflows(id,project_id,name,description,current_version_id,version,created_at,updated_at) VALUES (?,?,?,? ,NULL,1,?,?)`, secondWorkflowID, selected.ID, definition.Name, definition.Description, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_versions(id,workflow_id,version_number,definition_json,definition_sha256,compilation_json,compilation_sha256,created_at) VALUES (?,?,?,?,?,?,?,?)`, secondVersionID, secondWorkflowID, 1, string(definitionJSON), compiled.DefinitionSHA256, string(compilationJSON), compiled.CompilationSHA256, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflows SET current_version_id=? WHERE id=?`, secondVersionID, secondWorkflowID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO workflow_runs(id,project_id,workflow_id,workflow_version_id,status,permission_mode,inputs_json,inputs_sha256,compilation_json,compilation_sha256,outputs_json,created_at,updated_at) VALUES ('duplicate-active',?,?,?,?,?,'{}',?, ?,?,'{}',?,?)`, selected.ID, secondWorkflowID, secondVersionID, "paused", "plan", strings.Repeat("a", 64), string(compilationJSON), compiled.CompilationSHA256, now, now); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, selected.ID, first.Workflow.ID); err == nil || !strings.Contains(err.Error(), "未结束") {
		t.Fatalf("equivalent active Workflow deletion error = %v", err)
	}
	var workflowCount int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows WHERE project_id=?`, selected.ID).Scan(&workflowCount); err != nil {
		t.Fatal(err)
	}
	if workflowCount != 2 {
		t.Fatalf("atomic rejection left %d Workflows, want 2", workflowCount)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE workflow_runs SET status='cancelled',completed_at=updated_at WHERE id='duplicate-active'`); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, selected.ID, first.Workflow.ID); err != nil {
		t.Fatal(err)
	}
	var versionCount int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows WHERE project_id=?`, selected.ID).Scan(&workflowCount); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_versions WHERE workflow_id IN (?,?)`, first.Workflow.ID, secondWorkflowID).Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if workflowCount != 0 || versionCount != 0 {
		t.Fatalf("equivalent Workflow rows remain: workflows=%d versions=%d", workflowCount, versionCount)
	}
}

func TestWorkflowRepositoryRejectsTamperedSnapshot(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workflow-tamper.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := NewWorkflowRepository(store.DB())
	now := time.Now().UTC()
	record := workflow.SaveRecord{
		Workflow: workflow.Workflow{ID: "workflow", ProjectID: "project", Name: "Tamper", CreatedAt: now, UpdatedAt: now},
		Version: workflow.Version{
			ID: "version", WorkflowID: "workflow", Definition: workflow.Definition{SchemaVersion: 1, Name: "Tamper"},
			DefinitionSHA256: strings.Repeat("a", 64), CompilationSHA256: strings.Repeat("b", 64), CreatedAt: now,
		},
	}
	if _, err := repository.SaveVersion(ctx, record); err == nil || !strings.Contains(err.Error(), "snapshot hash") {
		t.Fatalf("tampered snapshot error = %v", err)
	}
}

func TestWorkflowServiceRejectsInvalidDefinitionBeforePersistence(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workflow-invalid.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(t.TempDir(), "workspaces"), filepath.Join(t.TempDir(), "trash"))
	selected, err := projects.Create(ctx, "Invalid Workflow project", "")
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	if err := registry.Register(ctx, workflowFixtureTool{version: "1"}); err != nil {
		t.Fatal(err)
	}
	service, err := workflow.NewService(NewWorkflowRepository(store.DB()), projects, workflow.NewCompiler(registry))
	if err != nil {
		t.Fatal(err)
	}
	invalid := workflow.Definition{
		SchemaVersion: 1,
		Name:          "Cyclic Workflow",
		Nodes: []workflow.Node{
			{ID: "first", Name: "First", Kind: workflow.NodeTool, ToolName: "fixture.workflow", Arguments: json.RawMessage(`{"query":"one"}`)},
			{ID: "second", Name: "Second", Kind: workflow.NodeTool, ToolName: "fixture.workflow", Arguments: json.RawMessage(`{"query":"two"}`)},
		},
		Edges: []workflow.Edge{
			{FromNode: "first", FromPort: "structured.answer", ToNode: "second", ToPort: "query"},
			{FromNode: "second", FromPort: "structured.answer", ToNode: "first", ToPort: "query"},
		},
	}
	if _, err := service.Save(ctx, workflow.SaveCommand{ProjectID: selected.ID, Definition: invalid}); err == nil || !strings.Contains(err.Error(), "static validation") {
		t.Fatalf("invalid Workflow save error = %v", err)
	}
	var workflows, versions int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows WHERE project_id=?`, selected.ID).Scan(&workflows); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_versions`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if workflows != 0 || versions != 0 {
		t.Fatalf("invalid Workflow persisted rows: workflows=%d versions=%d", workflows, versions)
	}
}

func TestWorkflowDeleteRemovesTerminalHistoryAndRejectsActiveRun(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workflow-delete.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(t.TempDir(), "workspaces"), filepath.Join(t.TempDir(), "trash"))
	selected, err := projects.Create(ctx, "Delete Workflow", "")
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	if err := registry.Register(ctx, workflowFixtureTool{version: "1"}); err != nil {
		t.Fatal(err)
	}
	service, err := workflow.NewService(NewWorkflowRepository(store.DB()), projects, workflow.NewCompiler(registry))
	if err != nil {
		t.Fatal(err)
	}
	definition := workflow.Definition{SchemaVersion: 1, Name: "Disposable", Nodes: []workflow.Node{{ID: "search", Name: "Search", Kind: workflow.NodeTool, ToolName: "fixture.workflow", Arguments: json.RawMessage(`{"query":"fixed"}`)}}}
	active, err := service.Save(ctx, workflow.SaveCommand{ProjectID: selected.ID, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	now := formatTime(time.Now().UTC())
	compilation, _ := json.Marshal(active.Version.Compilation)
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO workflow_runs(id,project_id,workflow_id,workflow_version_id,status,permission_mode,inputs_json,inputs_sha256,compilation_json,compilation_sha256,outputs_json,created_at,updated_at) VALUES ('active',?,?,?,?,?,'{}',?, ?,?,'{}',?,?)`, selected.ID, active.Workflow.ID, active.Version.ID, "paused", "plan", strings.Repeat("a", 64), string(compilation), active.Version.CompilationSHA256, now, now); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, selected.ID, active.Workflow.ID); err == nil || !strings.Contains(err.Error(), "未结束") {
		t.Fatalf("active Workflow deletion error = %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE workflow_runs SET status='cancelled',completed_at=updated_at WHERE id='active'`); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, selected.ID, active.Workflow.ID); err != nil {
		t.Fatal(err)
	}
	var workflows, versions, runs int
	_ = store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows WHERE id=?`, active.Workflow.ID).Scan(&workflows)
	_ = store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_versions WHERE workflow_id=?`, active.Workflow.ID).Scan(&versions)
	_ = store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_runs WHERE workflow_id=?`, active.Workflow.ID).Scan(&runs)
	if workflows != 0 || versions != 0 || runs != 0 {
		t.Fatalf("deleted Workflow rows remain: workflows=%d versions=%d runs=%d", workflows, versions, runs)
	}
}

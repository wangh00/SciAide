package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

type starterUpgradeDefinitionTool struct{ definition tool.Definition }

func (t starterUpgradeDefinitionTool) Definition(context.Context) (tool.Definition, error) {
	return t.definition, nil
}

func (starterUpgradeDefinitionTool) Invoke(context.Context, tool.Invocation) (tool.Result, error) {
	return tool.Result{}, fmt.Errorf("compile-only tool must not execute")
}

func TestResearchStarterRefreshesPersistedToolContractsAfterUpgrade(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	application, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if application != nil {
			_ = application.Close()
		}
	})
	application.Startup(ctx)
	server := newResearchStarterSkillTestServer(t, nil)
	profile := saveWorkflowAITestProfile(t, application, server.URL)
	owner, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Persisted starter upgrade"})
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := application.ToolFacade.ListTools()
	if err != nil {
		t.Fatal(err)
	}
	oldRegistry, currentRegistry := tool.NewRegistry(), tool.NewRegistry()
	downgraded := false
	for _, definition := range definitions {
		if err := currentRegistry.Register(ctx, starterUpgradeDefinitionTool{definition}); err != nil {
			t.Fatal(err)
		}
		if definition.QualifiedName == "builtin.knowledge.search" {
			definition.Version = "3"
			var schema map[string]any
			if err := json.Unmarshal(definition.InputSchema, &schema); err != nil {
				t.Fatal(err)
			}
			schema["properties"].(map[string]any)["documentIds"].(map[string]any)["maxItems"] = 20
			definition.InputSchema, err = json.Marshal(schema)
			if err != nil {
				t.Fatal(err)
			}
			downgraded = true
		}
		if err := oldRegistry.Register(ctx, starterUpgradeDefinitionTool{definition}); err != nil {
			t.Fatal(err)
		}
	}
	if !downgraded {
		t.Fatal("knowledge search was not registered")
	}
	definition := workflow.ResearchStarterTemplate().Definition
	oldCompilation, err := workflow.NewCompiler(oldRegistry).Compile(ctx, definition)
	if err != nil {
		t.Fatal(err)
	}
	currentCompilation, err := workflow.NewCompiler(currentRegistry).Compile(ctx, definition)
	if err != nil {
		t.Fatal(err)
	}
	if oldCompilation.DefinitionSHA256 != currentCompilation.DefinitionSHA256 || oldCompilation.CompilationSHA256 == currentCompilation.CompilationSHA256 {
		t.Fatal("fixture must reproduce unchanged template with different tool contracts")
	}
	now := time.Now().UTC()
	old, err := sqlite.NewWorkflowRepository(application.store.DB()).SaveVersion(ctx, workflow.SaveRecord{
		Workflow: workflow.Workflow{ID: "prior-starter", ProjectID: owner.ID, Purpose: workflow.PurposeResearchStarter, Name: definition.Name, Description: definition.Description, CreatedAt: now, UpdatedAt: now},
		Version:  workflow.Version{ID: "prior-starter-version", WorkflowID: "prior-starter", Definition: definition, DefinitionSHA256: oldCompilation.DefinitionSHA256, Compilation: oldCompilation, CompilationSHA256: oldCompilation.CompilationSHA256, CreatedAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce the reported runtime guard, without weakening it or rewriting
	// a frozen run when the next task refreshes the shared starter version.
	oldRun, err := application.workflow.Start(ctx, workflow.StartCommand{
		ProjectID: owner.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: old.Workflow.ID, WorkflowVersionID: old.Version.ID,
		Inputs: json.RawMessage(`{"starter_context":{"researchIdea":"Test prior tool contract"}}`), PermissionMode: conversation.PermissionFullAccess, ModelProfileID: profile.ID, ModelID: workflowAITestModelID,
	})
	if err != nil {
		t.Fatal(err)
	}
	failed := waitForWorkflowState(t, application, owner.ID, oldRun.Run.ID, workflow.RunFailed, 10*time.Second)
	if failed.Run.ErrorCode != "WORKFLOW_AI_TOOL_DEFINITION_CHANGED" || len(failed.AIExecutions) != 0 {
		t.Fatalf("old contract must be rejected before AI execution: code=%s executions=%d", failed.Run.ErrorCode, len(failed.AIExecutions))
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	application, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	application.Startup(ctx)
	var refreshedVersionID string
	for i := 0; i < 2; i++ {
		started, err := application.WorkflowFacade.StartResearch(workflow.StartResearchCommand{
			ProjectID: owner.ID, ResearchIdea: "研究夜间使用手机与睡眠的关系", ModelProfileID: profile.ID, ModelID: workflowAITestModelID,
		})
		if err != nil {
			t.Fatal(err)
		}
		completed := waitForWorkflowState(t, application, owner.ID, started.Run.ID, workflow.RunCompleted, 20*time.Second)
		if completed.Run.WorkflowID != old.Workflow.ID || completed.Run.WorkflowVersionID == old.Version.ID || !reflect.DeepEqual(completed.Run.Compilation, currentCompilation) {
			t.Fatal("new research task did not freeze all current tool contracts")
		}
		if len(completed.AIExecutions) == 0 {
			t.Fatal("new starter did not reach the model")
		}
		if i == 0 {
			refreshedVersionID = completed.Run.WorkflowVersionID
		} else if completed.Run.WorkflowVersionID != refreshedVersionID {
			t.Fatal("unchanged starter created another version")
		}
	}
	detail, err := sqlite.NewWorkflowRepository(application.store.DB()).Get(ctx, owner.ID, old.Workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Versions) != 2 || detail.Versions[0].ID != refreshedVersionID || !reflect.DeepEqual(detail.Versions[1].Compilation, oldCompilation) {
		t.Fatal("upgrade rewrote the old version or did not publish exactly one new version")
	}
	preserved, err := application.WorkflowFacade.GetRun(owner.ID, failed.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preserved.Run.WorkflowVersionID != old.Version.ID || !reflect.DeepEqual(preserved.Run.Compilation, failed.Run.Compilation) || preserved.Run.ErrorCode != failed.Run.ErrorCode {
		t.Fatal("new task silently migrated the frozen old run")
	}
}

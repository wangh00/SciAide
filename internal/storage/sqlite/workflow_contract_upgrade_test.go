package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

type workflowContractFixtureTool struct {
	workflowFixtureTool
	definition tool.Definition
}

func (f workflowContractFixtureTool) Definition(context.Context) (tool.Definition, error) {
	return f.definition, nil
}

func TestWorkflowSaveRefreshesChangedToolContracts(t *testing.T) {
	changes := []struct {
		name   string
		mutate func(*tool.Definition)
	}{
		{"version", func(d *tool.Definition) { d.Version = "2" }},
		{"input_schema_without_version_bump", func(d *tool.Definition) {
			d.InputSchema = json.RawMessage(`{"type":"object","required":["query"],"properties":{"query":{"type":"string","maxLength":100}}}`)
		}},
		{"output_schema_without_version_bump", func(d *tool.Definition) {
			d.OutputSchema = json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"},"count":{"type":"integer"}}}`)
		}},
		{"permissions", func(d *tool.Definition) {
			d.Permissions = []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead, Resource: "."}}
		}},
		{"risk", func(d *tool.Definition) { d.Risk = tool.RiskModerate }},
		{"idempotence", func(d *tool.Definition) { d.Idempotent = false }},
	}
	for _, kind := range []workflow.NodeKind{workflow.NodeTool, workflow.NodeAgentStage} {
		for _, mode := range []string{"template", "explicit_save"} {
			t.Run(string(kind)+"/"+mode, func(t *testing.T) {
				ctx := context.Background()
				store, err := Open(ctx, filepath.Join(t.TempDir(), "upgrade.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(t.TempDir(), "workspaces"), filepath.Join(t.TempDir(), "trash"))
				owner, err := projects.Create(ctx, "Tool contract upgrade", "")
				if err != nil {
					t.Fatal(err)
				}
				registry := tool.NewRegistry()
				contract, _ := (workflowFixtureTool{version: "1"}).Definition(ctx)
				register := func() {
					t.Helper()
					if err := registry.ReplaceNamespace(ctx, "fixture.", []tool.Tool{workflowContractFixtureTool{definition: contract}}); err != nil {
						t.Fatal(err)
					}
				}
				register()
				service, err := workflow.NewService(NewWorkflowRepository(store.DB()), projects, workflow.NewCompiler(registry))
				if err != nil {
					t.Fatal(err)
				}
				node := workflow.Node{ID: "search", Name: "Search", Kind: kind, ToolName: contract.QualifiedName, Arguments: json.RawMessage(`{"query":"fixed"}`)}
				if kind == workflow.NodeAgentStage {
					node = workflow.Node{ID: "search", Name: "Search", Kind: kind, AllowedTools: []string{contract.QualifiedName}, Prompt: "Search the evidence", PromptVersion: "test-v1"}
				}
				command := workflow.SaveCommand{ProjectID: owner.ID, Definition: workflow.Definition{SchemaVersion: 1, Name: "Unchanged plan", Nodes: []workflow.Node{node}}}
				original, err := service.Save(ctx, command)
				if err != nil {
					t.Fatal(err)
				}
				previous := original
				for _, change := range changes {
					change.mutate(&contract)
					register()
					if mode == "explicit_save" {
						command.WorkflowID = previous.Workflow.ID
						command.ExpectedCurrentVersionID = previous.Version.ID
					}
					current, err := service.Save(ctx, command)
					if kind == workflow.NodeAgentStage && !contract.Idempotent {
						if err == nil {
							t.Fatal("unsafe agent tool was silently refreshed")
						}
						break
					}
					if err != nil {
						t.Fatalf("%s: %v", change.name, err)
					}
					if current.Created || current.Workflow.ID != original.Workflow.ID || current.Version.ID == previous.Version.ID || current.Version.Version != previous.Version.Version+1 {
						t.Fatalf("%s: changed contract did not create a new version of the same plan", change.name)
					}
					if current.Version.DefinitionSHA256 != original.Version.DefinitionSHA256 || current.Version.CompilationSHA256 == previous.Version.CompilationSHA256 {
						t.Fatalf("%s: definition/compilation hashes do not track the contract change", change.name)
					}
					if mode == "explicit_save" {
						if _, err := service.Save(ctx, command); err == nil {
							t.Fatal("stale version edit was accepted")
						}
						command.ExpectedCurrentVersionID = current.Version.ID
					}
					reused, err := service.Save(ctx, command)
					if err != nil || reused.Version.ID != current.Version.ID {
						t.Fatalf("%s: unchanged contract was not reused: %v", change.name, err)
					}
					previous = current
				}
				detail, err := NewWorkflowRepository(store.DB()).Get(ctx, owner.ID, original.Workflow.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(detail.Versions) != previous.Version.Version || !reflect.DeepEqual(detail.Versions[len(detail.Versions)-1].Compilation, original.Version.Compilation) {
					t.Fatal("upgrade changed the old snapshot or persisted extra versions")
				}
				var count int
				if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows WHERE project_id=?`, owner.ID).Scan(&count); err != nil || count != 1 {
					t.Fatalf("upgrade duplicated the plan: count=%d err=%v", count, err)
				}
			})
		}
	}
}

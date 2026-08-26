package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/opensciskill"
)

type resourceLoaderFixture struct {
	runID, skillID, path string
}

type resourceMaterializerFixture struct {
	value opensciskill.MaterializedResource
	runID string
}

func (f *resourceMaterializerFixture) MaterializeResource(_ context.Context, runID, name, path string) (opensciskill.MaterializedResource, error) {
	f.runID = runID
	value := f.value
	value.Name, value.SourcePath = name, path
	return value, nil
}

func (f *resourceLoaderFixture) ReadResource(_ context.Context, runID, skillID, resourcePath string, offset, maxBytes int) (opensciskill.Resource, error) {
	f.runID, f.skillID, f.path = runID, skillID, resourcePath
	return opensciskill.Resource{Name: skillID, Path: resourcePath, Content: "evidence", BytesRead: 8, OriginalBytes: 8}, nil
}

func TestReadSkillResourceUsesInvocationRunBoundary(t *testing.T) {
	loader := &resourceLoaderFixture{}
	implementation := NewReadSkillResource(loader)
	definition, err := implementation.Definition(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if definition.QualifiedName != ReadSkillResourceName || len(definition.Permissions) != 0 || definition.Risk != tool.RiskLow {
		t.Fatalf("definition = %#v", definition)
	}
	result, err := implementation.Invoke(context.Background(), tool.Invocation{RunID: "run-snapshot", ProjectID: "project", Arguments: json.RawMessage(`{"name":"review-skill","path":"references/paper.md"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if loader.runID != "run-snapshot" || loader.skillID != "review-skill" || loader.path != "references/paper.md" || result.Status != tool.ResultSuccess || result.Text != "evidence" {
		t.Fatalf("resource invocation = loader:%#v result:%#v", loader, result)
	}
}

func TestSkillCapabilityDiagnosticsIntersectRegistryWithoutGrantingPermission(t *testing.T) {
	definitions := []tool.Definition{
		{QualifiedName: "builtin.workspace.read_text", Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead}}},
		{QualifiedName: "mcp.python.execute", Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionProcessExecute}}},
	}
	read := capabilityMatches("Read", definitions)
	bash := capabilityMatches("Bash", definitions)
	write := capabilityMatches("Write", definitions)
	if len(read) != 1 || read[0] != "builtin.workspace.read_text" || len(bash) != 1 || bash[0] != "mcp.python.execute" || len(write) != 0 {
		t.Fatalf("capability matches: read=%#v bash=%#v write=%#v", read, bash, write)
	}
}

func TestMaterializeSkillResourcePublishesImmutableBinaryWithoutArtifactPromotion(t *testing.T) {
	workspace := t.TempDir()
	if err := project.PrepareRestoredWorkspace(workspace, "project"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	contents := []byte{0, 1, 2, 3, 255}
	loader := &resourceMaterializerFixture{value: opensciskill.MaterializedResource{PackageHash: strings.Repeat("a", 64), Contents: contents, SHA256: strings.Repeat("b", 64), Size: int64(len(contents))}}
	implementation := NewMaterializeSkillResource(loader, projectFixture{value: project.Project{ID: "project", WorkspacePath: workspace}})
	definition, err := implementation.Definition(context.Background())
	if err != nil || definition.Risk != tool.RiskModerate || len(definition.Permissions) != 1 || definition.Permissions[0].Kind != tool.PermissionWorkspaceWrite {
		t.Fatalf("definition = %#v, %v", definition, err)
	}
	result, err := implementation.Invoke(context.Background(), tool.Invocation{RunID: "run", ProjectID: "project", Arguments: json.RawMessage(`{"name":"fixture","path":"scripts/run.bin","destination":"scripts/run.bin"}`)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(workspace, "scripts", "run.bin"))
	if err != nil || !bytes.Equal(got, contents) {
		t.Fatalf("materialized bytes = %v, %v", got, err)
	}
	if loader.runID != "run" || len(result.Artifacts) != 0 || !strings.Contains(string(result.Structured), `"destination":"scripts/run.bin"`) {
		t.Fatalf("materialized result = %#v loader=%#v", result, loader)
	}
	if _, err := implementation.Invoke(context.Background(), tool.Invocation{RunID: "run", ProjectID: "project", Arguments: json.RawMessage(`{"name":"fixture","path":"scripts/run.bin","destination":"scripts/run.bin"}`)}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing destination error = %v", err)
	}
	if _, err := implementation.Invoke(context.Background(), tool.Invocation{RunID: "run", ProjectID: "project", Arguments: json.RawMessage(`{"name":"fixture","path":"scripts/run.bin","destination":"../escape.bin"}`)}); err == nil {
		t.Fatal("path escape was accepted")
	}
}

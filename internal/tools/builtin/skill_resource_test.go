package builtin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
)

type resourceLoaderFixture struct {
	runID, skillID, path string
}

func (f *resourceLoaderFixture) ReadRunResource(_ context.Context, runID, skillID, resourcePath string, maxBytes int) (skill.ResourceContent, error) {
	f.runID, f.skillID, f.path = runID, skillID, resourcePath
	return skill.ResourceContent{Path: resourcePath, Content: []byte("evidence"), OriginalBytes: 8}, nil
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
	result, err := implementation.Invoke(context.Background(), tool.Invocation{RunID: "run-snapshot", ProjectID: "project", Arguments: json.RawMessage(`{"skillId":"review-skill","path":"references/paper.md"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if loader.runID != "run-snapshot" || loader.skillID != "review-skill" || loader.path != "references/paper.md" || result.Status != tool.ResultSuccess || result.Text != "evidence" {
		t.Fatalf("resource invocation = loader:%#v result:%#v", loader, result)
	}
}

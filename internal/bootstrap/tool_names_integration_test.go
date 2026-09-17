package bootstrap

import (
	"context"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/modelutil"
)

func TestRegisteredBuiltinToolNamesAreShortUniqueAndExact(t *testing.T) {
	app, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.Startup(context.Background())
	definitions, err := app.ToolFacade.ListTools()
	if err != nil {
		t.Fatal(err)
	}
	var tools []model.ToolDefinition
	for _, def := range definitions {
		if !strings.HasPrefix(def.QualifiedName, "builtin.") {
			continue
		}
		expected := strings.ReplaceAll(strings.TrimPrefix(def.QualifiedName, "builtin."), ".", "_")
		actual := modelutil.ProviderToolName(def.QualifiedName)
		if actual != expected || len(actual) > 64 || actual == "" {
			t.Fatalf("invalid short name: %s -> %s", def.QualifiedName, actual)
		}
		tools = append(tools, model.ToolDefinition{Name: def.QualifiedName, InputSchema: def.InputSchema})
	}
	if len(tools) == 0 {
		t.Fatal("registry has no builtin tools")
	}
	forward, reverse, err := modelutil.BuildToolAliases(tools)
	if err != nil {
		t.Fatal(err)
	}
	for name, alias := range forward {
		if modelutil.ResolveProviderToolName(alias, reverse) != name {
			t.Fatal(name)
		}
	}
	t.Logf("verified %d registered builtin tools", len(tools))
}

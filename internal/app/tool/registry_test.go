package tool

import (
	"context"
	"encoding/json"
	"testing"
)

type registryFixture struct{ name string }

type fixtureTool struct{ definition Definition }

func (f fixtureTool) Definition(context.Context) (Definition, error) { return f.definition, nil }
func (fixtureTool) Invoke(context.Context, Invocation) (Result, error) {
	return Result{Status: ResultSuccess}, nil
}

func (f registryFixture) Definition(context.Context) (Definition, error) {
	return Definition{QualifiedName: f.name, Description: "fixture", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: RiskLow, Permissions: []PermissionRequirement{}, Idempotent: true, Version: "1"}, nil
}
func (registryFixture) Invoke(context.Context, Invocation) (Result, error) {
	return Result{Status: ResultSuccess}, nil
}

func TestRegistryRejectsDuplicatesAndReturnsImmutableDefinitions(t *testing.T) {
	ctx := context.Background()
	registry := NewRegistry()
	definition := Definition{QualifiedName: "builtin.workspace.list", Description: "List files", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: RiskLow, Permissions: []PermissionRequirement{{Kind: PermissionWorkspaceRead, Resource: "."}}, Idempotent: true, Version: "1"}
	if err := registry.Register(ctx, fixtureTool{definition: definition}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(ctx, fixtureTool{definition: definition}); err == nil {
		t.Fatal("duplicate tool was accepted")
	}
	loaded, err := registry.Definition(ctx, definition.QualifiedName)
	if err != nil {
		t.Fatal(err)
	}
	loaded.InputSchema[0] = '['
	loaded.Permissions[0].Resource = "changed"
	again, err := registry.Definition(ctx, definition.QualifiedName)
	if err != nil {
		t.Fatal(err)
	}
	if string(again.InputSchema) != string(definition.InputSchema) || again.Permissions[0].Resource != "." {
		t.Fatalf("registry definition was mutated: %#v", again)
	}
	if _, err := registry.Resolve(ctx, "unknown.tool"); err == nil {
		t.Fatal("unknown tool resolved")
	}
}

func TestRegistryDefinitionsAreStableAndSorted(t *testing.T) {
	registry := NewRegistry()
	ctx := context.Background()
	for _, name := range []string{"z.tool", "a.tool"} {
		if err := registry.Register(ctx, fixtureTool{definition: Definition{QualifiedName: name, Description: name, InputSchema: json.RawMessage(`{}`), Risk: RiskLow, Version: "1"}}); err != nil {
			t.Fatal(err)
		}
	}
	values, err := registry.Definitions(ctx)
	if err != nil || len(values) != 2 || values[0].QualifiedName != "a.tool" {
		t.Fatalf("Definitions() = %#v, %v", values, err)
	}
}

func TestReplaceNamespaceIsAtomicAndKeepsOtherTools(t *testing.T) {
	ctx := context.Background()
	registry := NewRegistry()
	if err := registry.Register(ctx, registryFixture{name: "builtin.keep"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplaceNamespace(ctx, "mcp.lab.", []Tool{registryFixture{name: "mcp.lab.one"}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplaceNamespace(ctx, "mcp.lab.", []Tool{registryFixture{name: "outside.tool"}}); err == nil {
		t.Fatal("outside replacement was accepted")
	}
	if _, err := registry.Definition(ctx, "mcp.lab.one"); err != nil {
		t.Fatal("failed replacement destroyed previous namespace")
	}
	if err := registry.ReplaceNamespace(ctx, "mcp.lab.", []Tool{registryFixture{name: "mcp.lab.two"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Definition(ctx, "mcp.lab.one"); err == nil {
		t.Fatal("stale MCP tool remained registered")
	}
	if _, err := registry.Definition(ctx, "builtin.keep"); err != nil {
		t.Fatal("builtin tool was removed")
	}
}

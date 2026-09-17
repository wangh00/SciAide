package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

type fixtureTool struct{ definition tool.Definition }

func (f fixtureTool) Definition(context.Context) (tool.Definition, error) { return f.definition, nil }
func (f fixtureTool) Invoke(context.Context, tool.Invocation) (tool.Result, error) {
	return tool.Result{Status: tool.ResultSuccess}, nil
}

func fixtureRegistry(t *testing.T, version string) *tool.MemoryRegistry {
	t.Helper()
	registry := tool.NewRegistry()
	definitions := []tool.Definition{
		{
			QualifiedName: "fixture.analyze",
			Description:   "Analyze a fixture",
			Version:       version,
			Risk:          tool.RiskLow,
			Idempotent:    true,
			Permissions:   []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead}},
			InputSchema:   json.RawMessage(`{"type":"object","additionalProperties":false,"required":["query","count"],"properties":{"query":{"type":"string"},"count":{"type":"integer"},"inputPath":{"type":"string"},"options":{"type":"object"}}}`),
			OutputSchema:  json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"},"score":{"type":"number"}}}`),
		},
		{
			QualifiedName: "builtin.shell.execute",
			Description:   "Execute shell",
			Version:       "1",
			Risk:          tool.RiskHigh,
			Permissions:   []tool.PermissionRequirement{{Kind: tool.PermissionProcessExecute}},
			InputSchema:   json.RawMessage(`{"type":"object","additionalProperties":false,"required":["command"],"properties":{"command":{"type":"string"},"workdir":{"type":"string"}}}`),
			OutputSchema:  json.RawMessage(`{"type":"object","properties":{"exitCode":{"type":"integer"}}}`),
		},
		{
			QualifiedName: "builtin.python.kernel.execute",
			Description:   "Execute Python",
			Version:       "1",
			Risk:          tool.RiskHigh,
			Permissions:   []tool.PermissionRequirement{{Kind: tool.PermissionProcessExecute}},
			InputSchema:   json.RawMessage(`{"type":"object","additionalProperties":false,"required":["code"],"properties":{"code":{"type":"string"}}}`),
			OutputSchema:  json.RawMessage(`{"type":"object","properties":{"stdout":{"type":"string"}}}`),
		},
		{QualifiedName: "builtin.skill.load", Description: "Load Skill", Version: "1", Risk: tool.RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`)},
		{QualifiedName: "builtin.skill.resource.list", Description: "List Skill resources", Version: "1", Risk: tool.RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`)},
		{QualifiedName: "builtin.skill.resource.read_text", Description: "Read Skill resource", Version: "1", Risk: tool.RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	for _, definition := range definitions {
		if err := registry.Register(context.Background(), fixtureTool{definition: definition}); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}

func TestCompilerFreezesAgentAutonomyAndSkillRoutingDefaults(t *testing.T) {
	base := Node{ID: "review", Name: "Review", Kind: NodeAgentStage, Arguments: json.RawMessage(`{}`), Prompt: "Review the evidence.", PromptVersion: "review-v1", OutputSchema: json.RawMessage(`{"type":"object"}`)}
	defaultNode, err := NewCompiler(fixtureRegistry(t, "1")).Compile(context.Background(), Definition{SchemaVersion: SchemaVersion, Name: "Default review", Nodes: []Node{base}})
	if err != nil {
		t.Fatal(err)
	}
	if defaultNode.Nodes[0].ReviewPolicy != AIReviewHuman || defaultNode.Nodes[0].SkillRouting || len(defaultNode.Nodes[0].AllowedTools) != 0 {
		t.Fatalf("default Agent Stage policy changed: %#v", defaultNode.Nodes[0])
	}
	base.ReviewPolicy, base.SkillRouting = AIReviewAuto, true
	autonomous, err := NewCompiler(fixtureRegistry(t, "1")).Compile(context.Background(), Definition{SchemaVersion: SchemaVersion, Name: "Autonomous review", Nodes: []Node{base}})
	if err != nil {
		t.Fatal(err)
	}
	node := autonomous.Nodes[0]
	if node.ReviewPolicy != AIReviewAuto || !node.SkillRouting || !hasFrozenTool(node.AllowedTools, "builtin.skill.load") || len(node.AllowedTools) != len(workflowSkillReadTools) {
		t.Fatalf("autonomous Agent Stage snapshot = %#v", node)
	}
	base.AllowedTools = []string{"builtin.skill.load"}
	preview := NewCompiler(fixtureRegistry(t, "1")).Preview(context.Background(), Definition{SchemaVersion: SchemaVersion, Name: "Invalid Skill declaration", Nodes: []Node{base}})
	assertDiagnostic(t, preview, "forbidden_tool")
	base.SkillRouting = false
	base.AllowedTools = []string{"builtin.shell.execute"}
	preview = NewCompiler(fixtureRegistry(t, "1")).Preview(context.Background(), Definition{SchemaVersion: SchemaVersion, Name: "Unsafe autonomous execution", Nodes: []Node{base}})
	assertDiagnostic(t, preview, "unsafe_agent_tool")
}

func TestCompilerValidatesOutputContractWithoutDummyInstance(t *testing.T) {
	for _, schema := range []string{`{"type":"array"}`, `{"type":"string"}`, `{"type":"object","required":["x","x"]}`, `{"type":"object","properties":{"nested":{"required":["x","x"]}}}`} {
		node := Node{ID: "analysis", Kind: NodeAIAnalysis, Name: "Analysis", Prompt: "Analyze", PromptVersion: "analysis-v1", OutputSchema: raw(schema)}
		preview := NewCompiler(fixtureRegistry(t, "1")).Preview(context.Background(), Definition{SchemaVersion: SchemaVersion, Name: "Invalid contract", Nodes: []Node{node}})
		assertDiagnostic(t, preview, "invalid_output_schema")
	}
	node := Node{ID: "analysis", Kind: NodeAIAnalysis, Name: "Analysis", Prompt: "Analyze", PromptVersion: "analysis-v1", OutputSchema: raw(`{"type":"object","required":["x"],"properties":{"x":{"type":"string"}}}`)}
	if _, err := NewCompiler(fixtureRegistry(t, "1")).Compile(context.Background(), Definition{SchemaVersion: SchemaVersion, Name: "Valid contract", Nodes: []Node{node}}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentStageToolSafetyAllowsOnlyIdempotentObservation(t *testing.T) {
	allowed := []tool.Definition{
		{QualifiedName: "workspace", Version: "1", Risk: tool.RiskLow, Idempotent: true, Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead}}},
		{QualifiedName: "search", Version: "1", Risk: tool.RiskModerate, Idempotent: true, Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionNetworkDomain}}},
	}
	for _, definition := range allowed {
		if !agentStageToolSafe(definition) {
			t.Fatalf("observation tool was rejected: %#v", definition)
		}
	}
	blocked := []tool.Definition{
		{QualifiedName: "write", Version: "1", Risk: tool.RiskModerate, Idempotent: true, Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceWrite}}},
		{QualifiedName: "python", Version: "1", Risk: tool.RiskHigh, Idempotent: true, Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionProcessExecute}}},
		{QualifiedName: "external", Version: "1", Risk: tool.RiskLow, Idempotent: true, Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionFilesystemExternal}}},
		{QualifiedName: "unstable", Version: "1", Risk: tool.RiskLow, Idempotent: false},
	}
	for _, definition := range blocked {
		if agentStageToolSafe(definition) {
			t.Fatalf("unsafe Agent Stage tool was accepted: %#v", definition)
		}
	}
}

func validDefinition() Definition {
	return Definition{
		SchemaVersion: SchemaVersion,
		Name:          "Evidence analysis",
		Inputs:        []Port{{Name: "topic", Type: TypeString, Required: true}},
		Nodes: []Node{
			{ID: "select", Name: "Select citations", Kind: NodeCitationSelection, Arguments: json.RawMessage(`{}`)},
			{ID: "analyze", Name: "Analyze", Kind: NodeTool, ToolName: "fixture.analyze", Arguments: json.RawMessage(`{"count":2}`)},
		},
		Edges: []Edge{
			{FromNode: "$input", FromPort: "topic", ToNode: "select", ToPort: "query"},
			{FromNode: "analyze", FromPort: "citations", ToNode: "select", ToPort: "candidates"},
			{FromNode: "$input", FromPort: "topic", ToNode: "analyze", ToPort: "query"},
		},
		Outputs: []Output{{Name: "result", Type: TypeString, FromNode: "analyze", FromPort: "structured.value", Required: true}},
	}
}

func TestCompilerCompilesValidDAGAndFreezesToolDefinition(t *testing.T) {
	compiled, err := NewCompiler(fixtureRegistry(t, "7")).Compile(context.Background(), validDefinition())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(compiled.Order, ",") != "analyze,select" || len(compiled.Nodes) != 2 {
		t.Fatalf("order/nodes = %#v / %#v", compiled.Order, compiled.Nodes)
	}
	if compiled.Nodes[0].Tool == nil || compiled.Nodes[0].Tool.Version != "7" || compiled.Nodes[0].Tool.Risk != string(tool.RiskLow) {
		t.Fatalf("tool snapshot = %#v", compiled.Nodes[0].Tool)
	}
	if len(compiled.DefinitionSHA256) != 64 || len(compiled.CompilationSHA256) != 64 {
		t.Fatalf("hashes = %q / %q", compiled.DefinitionSHA256, compiled.CompilationSHA256)
	}
	upgraded, err := NewCompiler(fixtureRegistry(t, "8")).Compile(context.Background(), validDefinition())
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.DefinitionSHA256 != compiled.DefinitionSHA256 || upgraded.CompilationSHA256 == compiled.CompilationSHA256 {
		t.Fatalf("version snapshot hashes = before %#v after %#v", compiled, upgraded)
	}
}

func TestCompilerMapsShellAndPythonNodes(t *testing.T) {
	definition := Definition{SchemaVersion: 1, Name: "Execution", Nodes: []Node{
		{ID: "shell", Name: "Shell", Kind: NodeShell, Arguments: json.RawMessage(`{"command":"Get-Date"}`)},
		{ID: "python", Name: "Python", Kind: NodePython, Arguments: json.RawMessage(`{"code":"print(1)"}`)},
	}}
	compiled, err := NewCompiler(fixtureRegistry(t, "1")).Compile(context.Background(), definition)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Nodes[0].Tool.QualifiedName != "builtin.python.kernel.execute" || compiled.Nodes[1].Tool.QualifiedName != "builtin.shell.execute" {
		t.Fatalf("mapped nodes = %#v", compiled.Nodes)
	}
	definition.Nodes[0].ToolName = "fixture.analyze"
	assertDiagnostic(t, NewCompiler(fixtureRegistry(t, "1")).Preview(context.Background(), definition), "fixed_tool")
}

func TestCompilerCanOrderHumanConfirmationAfterTool(t *testing.T) {
	definition := validDefinition()
	definition.Nodes = append(definition.Nodes, Node{ID: "confirm", Name: "Confirm", Kind: NodeHumanConfirmation, Prompt: "Continue with the selected evidence?"})
	definition.Edges = append(definition.Edges, Edge{FromNode: "analyze", FromPort: "structured", ToNode: "confirm", ToPort: "context"})
	compiled, err := NewCompiler(fixtureRegistry(t, "1")).Compile(context.Background(), definition)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(compiled.Order, ",") != "analyze,confirm,select" {
		t.Fatalf("confirmation order = %#v", compiled.Order)
	}
}

func TestCompilerRejectsInvalidGraphsAndInputs(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Definition)
		code string
	}{
		{"cycle", func(value *Definition) {
			value.Nodes = []Node{
				{ID: "first", Name: "First", Kind: NodeTool, ToolName: "fixture.analyze", Arguments: json.RawMessage(`{"count":1}`)},
				{ID: "second", Name: "Second", Kind: NodeTool, ToolName: "fixture.analyze", Arguments: json.RawMessage(`{"count":1}`)},
			}
			value.Edges = []Edge{
				{FromNode: "first", FromPort: "text", ToNode: "second", ToPort: "query"},
				{FromNode: "second", FromPort: "text", ToNode: "first", ToPort: "query"},
			}
			value.Outputs = nil
		}, "cycle"},
		{"unknown tool", func(value *Definition) { value.Nodes[1].ToolName = "missing.tool" }, "unknown_tool"},
		{"type mismatch", func(value *Definition) { value.Inputs[0].Type = TypeBoolean }, "type_mismatch"},
		{"dangling target", func(value *Definition) { value.Edges[0].ToNode = "missing" }, "dangling_target"},
		{"missing required", func(value *Definition) { value.Edges = value.Edges[:1] }, "missing_required_input"},
		{"duplicate node", func(value *Definition) { value.Nodes = append(value.Nodes, value.Nodes[1]) }, "invalid_node_id"},
		{"blank arguments", func(value *Definition) { value.Nodes[1].Arguments = json.RawMessage("   ") }, "missing_required_input"},
		{"literal number", func(value *Definition) {
			value.Nodes[1].Arguments = json.RawMessage(`{"count":1.5,"query":"x"}`)
			value.Edges = value.Edges[:1]
		}, "argument_type"},
		{"secret nested", func(value *Definition) {
			value.Nodes[1].Arguments = json.RawMessage(`{"count":2,"query":"x","options":{"apiToken":"secret"}}`)
			value.Edges = value.Edges[:1]
		}, "secret_argument"},
		{"path escape", func(value *Definition) {
			value.Nodes[1].Arguments = json.RawMessage(`{"count":2,"query":"x","inputPath":"data/.sciaide/private"}`)
			value.Edges = value.Edges[:1]
		}, "path_escape"},
		{"absolute default", func(value *Definition) {
			value.Inputs = append(value.Inputs, Port{Name: "input_path", Type: TypeString, Default: json.RawMessage(`"C:\\\\private"`)})
		}, "path_escape"},
		{"invalid file kind", func(value *Definition) {
			value.Inputs = append(value.Inputs, Port{Name: "data", Type: TypeArray, FileKind: "image"})
		}, "file_input_kind"},
		{"invalid control", func(value *Definition) {
			value.Inputs = append(value.Inputs, Port{Name: "request", Type: TypeObject, Control: "unknown"})
		}, "input_control"},
		{"control type", func(value *Definition) {
			value.Inputs = append(value.Inputs, Port{Name: "request", Type: TypeString, Control: "analysis_request"})
		}, "input_control_type"},
		{"item bounds", func(value *Definition) {
			value.Inputs = append(value.Inputs, Port{Name: "items", Type: TypeArray, MinItems: 2, MaxItems: 1})
		}, "input_item_bounds"},
		{"default item bounds", func(value *Definition) {
			value.Inputs = append(value.Inputs, Port{Name: "items", Type: TypeArray, MinItems: 2, Default: json.RawMessage(`["one"]`)})
		}, "default_constraint"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := validDefinition()
			test.edit(&definition)
			preview := NewCompiler(fixtureRegistry(t, "1")).Preview(context.Background(), definition)
			assertDiagnostic(t, preview, test.code)
			if preview.Valid {
				t.Fatal("invalid Workflow was reported valid")
			}
		})
	}
}

func TestCompilerRejectsOversizedGraphAndStillReturnsPreview(t *testing.T) {
	definition := validDefinition()
	definition.Nodes = make([]Node, maxNodes+1)
	for index := range definition.Nodes {
		definition.Nodes[index] = Node{ID: "node" + strings.Repeat("x", index%60), Name: "Node", Kind: NodeHumanConfirmation, Prompt: "Continue?"}
	}
	preview := NewCompiler(fixtureRegistry(t, "1")).Preview(context.Background(), definition)
	assertDiagnostic(t, preview, "node_count")
	if len(preview.Nodes) == 0 {
		t.Fatal("invalid Workflow preview did not retain parseable nodes")
	}
}

func assertDiagnostic(t *testing.T, preview Preview, code string) {
	t.Helper()
	for _, diagnostic := range preview.Diagnostics {
		if diagnostic.Code == code {
			return
		}
	}
	t.Fatalf("missing diagnostic %q in %#v", code, preview.Diagnostics)
}

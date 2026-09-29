package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/wangh00/SciAide/internal/app/tool"
)

const (
	CompilerVersion    = "p7.4-v3"
	maxDefinitionBytes = 1 << 20
	maxNodes           = 128
	maxEdges           = 512
	maxPorts           = 64
)

var identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var schemaFieldPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

var workflowSkillReadTools = []string{
	"builtin.skill.load",
	"builtin.skill.resource.list",
	"builtin.skill.resource.read_text",
}

type Compiler struct{ registry tool.Registry }

func NewCompiler(registry tool.Registry) *Compiler { return &Compiler{registry: registry} }

func (c *Compiler) Compile(ctx context.Context, definition Definition) (Compilation, error) {
	diagnostics := c.validateShape(definition)
	encoded, encodeErr := canonicalJSON(definition)
	if encodeErr != nil {
		diagnostics = append(diagnostics, diagnostic("error", "definition_json", "$", encodeErr.Error()))
	}
	if len(encoded) > maxDefinitionBytes {
		diagnostics = append(diagnostics, diagnostic("error", "definition_too_large", "$", "Workflow definition exceeds 1 MiB"))
	}
	definitionHash := hashBytes(encoded)
	inputTypes := map[string]DataType{}
	for _, input := range definition.Inputs {
		inputTypes[input.Name] = input.Type
	}

	type compiledDraft struct {
		node     CompiledNode
		inputs   map[string]DataType
		outputs  map[string]DataType
		required map[string]bool
	}
	drafts := map[string]*compiledDraft{}
	for index, node := range definition.Nodes {
		path := fmt.Sprintf("nodes[%d]", index)
		if _, exists := drafts[node.ID]; exists {
			continue
		}
		draft := &compiledDraft{node: CompiledNode{ID: node.ID, Name: node.Name, Kind: node.Kind, Arguments: nonEmptyObject(node.Arguments), Prompt: node.Prompt, PromptVersion: node.PromptVersion, SkillRouting: node.SkillRouting, ReviewPolicy: node.ReviewPolicy, Dependencies: []string{}}, inputs: map[string]DataType{}, outputs: map[string]DataType{}, required: map[string]bool{}}
		switch node.Kind {
		case NodeTool, NodeShell, NodePython:
			name := strings.TrimSpace(node.ToolName)
			if node.Kind == NodeShell {
				if name != "" && name != "builtin.shell.execute" {
					diagnostics = append(diagnostics, diagnostic("error", "fixed_tool", path+".toolName", "Shell nodes always use builtin.shell.execute"))
				}
				name = "builtin.shell.execute"
			}
			if node.Kind == NodePython {
				if name != "" && name != "builtin.python.kernel.execute" {
					diagnostics = append(diagnostics, diagnostic("error", "fixed_tool", path+".toolName", "Python nodes always use builtin.python.kernel.execute"))
				}
				name = "builtin.python.kernel.execute"
			}
			if strings.HasPrefix(name, "builtin.skill.") {
				diagnostics = append(diagnostics, diagnostic("error", "forbidden_tool", path+".toolName", "Workflow cannot implicitly load or manage Skills"))
				break
			}
			if c.registry == nil {
				diagnostics = append(diagnostics, diagnostic("error", "registry_unavailable", path+".toolName", "Tool registry is unavailable"))
				break
			}
			definition, err := c.registry.Definition(ctx, name)
			if err != nil {
				diagnostics = append(diagnostics, diagnostic("error", "unknown_tool", path+".toolName", fmt.Sprintf("Tool %q is not currently registered", name)))
				break
			}
			permissions, _ := json.Marshal(definition.Permissions)
			draft.node.Tool = &ToolSnapshot{QualifiedName: definition.QualifiedName, Version: definition.Version, Risk: string(definition.Risk), Permissions: permissions, Idempotent: definition.Idempotent, InputSchema: cloneRaw(definition.InputSchema), OutputSchema: cloneRaw(definition.OutputSchema)}
			draft.node.SideEffect = !definition.Idempotent
			draft.inputs, draft.required, diagnostics = schemaPorts(definition.InputSchema, path+".arguments", diagnostics)
			draft.outputs = resultPorts(definition.OutputSchema)
			diagnostics = validateArguments(node.Arguments, draft.inputs, path+".arguments", diagnostics)
			diagnostics = validatePathArguments(node.Arguments, path+".arguments", diagnostics)
		case NodeHumanConfirmation:
			draft.inputs["context"] = TypeAny
			draft.outputs["approved"] = TypeBoolean
			draft.outputs["context"] = TypeAny
			if len(decodeObject(node.Arguments)) != 0 {
				diagnostics = append(diagnostics, diagnostic("error", "unexpected_arguments", path+".arguments", "Human confirmation nodes do not accept arguments"))
			}
			if strings.TrimSpace(node.Prompt) == "" || len([]rune(node.Prompt)) > 2_000 {
				diagnostics = append(diagnostics, diagnostic("error", "invalid_prompt", path+".prompt", "Human confirmation prompt must contain 1-2000 characters"))
			}
		case NodeCandidateSelection:
			draft.inputs["query"] = TypeString
			draft.required["query"] = true
			draft.inputs["candidates"] = TypeArray
			draft.required["candidates"] = true
			draft.inputs["screening"] = TypeObject
			draft.inputs["referenceMaterials"] = TypeArray
			draft.inputs["reuseSelectedMaterials"] = TypeBoolean
			draft.outputs["selectedCandidateIds"] = TypeArray
			draft.outputs["selectedAttachmentIds"] = TypeArray
			draft.outputs["selectionAudit"] = TypeObject
			diagnostics = validateArguments(node.Arguments, draft.inputs, path+".arguments", diagnostics)
			if strings.TrimSpace(node.Prompt) == "" || len([]rune(node.Prompt)) > 2_000 {
				diagnostics = append(diagnostics, diagnostic("error", "invalid_prompt", path+".prompt", "Candidate selection prompt must contain 1-2000 characters"))
			}
		case NodeCitationSelection:
			draft.inputs["query"] = TypeString
			draft.required["query"] = true
			draft.inputs["candidates"] = TypeCitations
			draft.required["candidates"] = true
			draft.inputs["screening"] = TypeObject
			draft.inputs["selectedMaterialOnly"] = TypeBoolean
			draft.outputs["citations"] = TypeCitations
			draft.outputs["selectionAudit"] = TypeObject
			diagnostics = validateArguments(node.Arguments, draft.inputs, path+".arguments", diagnostics)
		case NodeAIAnalysis, NodeAgentStage:
			draft.inputs["context"] = TypeAny
			draft.inputs["researchContext"] = TypeAny
			draft.inputs["originalRequest"] = TypeString
			draft.inputs["researchContract"] = TypeObject
			draft.inputs["computedResults"] = TypeObject
			draft.inputs["sourceArtifacts"] = TypeArtifacts
			draft.inputs["dataPreflight"] = TypeObject
			draft.inputs["routeContext"] = TypeAny
			draft.inputs["methodContext"] = TypeAny
			draft.inputs["implementationContext"] = TypeObject
			draft.inputs["dataContext"] = TypeAny
			draft.inputs["designContext"] = TypeAny
			draft.inputs["evidenceContext"] = TypeAny
			draft.inputs["evidenceScreening"] = TypeAny
			draft.inputs["evidenceSelectionAudit"] = TypeAny
			draft.inputs["candidates"] = TypeAny
			draft.inputs["selectedCandidateIds"] = TypeAny
			draft.inputs["documentIds"] = TypeAny
			draft.inputs["documentCoverage"] = TypeAny
			draft.inputs["importedMaterials"] = TypeAny
			draft.inputs["partialDiscovery"] = TypeAny
			draft.outputs["analysis"] = TypeObject
			draft.outputs["text"] = TypeString
			if len(decodeObject(node.Arguments)) != 0 {
				diagnostics = append(diagnostics, diagnostic("error", "unexpected_arguments", path+".arguments", "AI nodes receive context through Workflow edges and do not accept literal arguments"))
			}
			prompt := strings.TrimSpace(node.Prompt)
			if prompt == "" || len([]rune(prompt)) > 20_000 {
				diagnostics = append(diagnostics, diagnostic("error", "invalid_prompt", path+".prompt", "AI node prompt must contain 1-20000 characters"))
			}
			if !identifierPattern.MatchString(strings.TrimSpace(node.PromptVersion)) {
				diagnostics = append(diagnostics, diagnostic("error", "invalid_prompt_version", path+".promptVersion", "AI node promptVersion must be a lower-case version identifier"))
			}
			schema := bytes.TrimSpace(node.OutputSchema)
			if len(schema) == 0 {
				schema = []byte(`{"type":"object","additionalProperties":true}`)
			}
			if len(schema) > 64*1024 || !json.Valid(schema) {
				diagnostics = append(diagnostics, diagnostic("error", "invalid_output_schema", path+".outputSchema", "AI output Schema must be valid JSON and at most 64 KiB"))
			} else if err := ValidateAIStageSchema(schema); err != nil {
				diagnostics = append(diagnostics, diagnostic("error", "invalid_output_schema", path+".outputSchema", err.Error()))
			}
			draft.node.OutputSchema = cloneRaw(schema)
			draft.node.OutputSchemaSHA256 = hashBytes(schema)
			// Expose only top-level fields declared by the frozen AI output
			// Schema. This lets a later deterministic Tool consume a specific
			// structured field (for example analysis.code) without treating an
			// arbitrary model-generated property as a Workflow port.
			for name, dataType := range aiAnalysisPorts(schema) {
				draft.outputs["analysis."+name] = dataType
			}
			if node.Kind == NodeAIAnalysis {
				if node.ReviewPolicy != "" && node.ReviewPolicy != AIReviewAuto {
					diagnostics = append(diagnostics, diagnostic("error", "invalid_review_policy", path+".reviewPolicy", "AI Analysis nodes always submit validated output automatically"))
				}
				draft.node.ReviewPolicy = AIReviewAuto
			} else {
				if node.ReviewPolicy == "" {
					draft.node.ReviewPolicy = AIReviewHuman
				} else if node.ReviewPolicy != AIReviewHuman && node.ReviewPolicy != AIReviewAuto {
					diagnostics = append(diagnostics, diagnostic("error", "invalid_review_policy", path+".reviewPolicy", "Agent Stage reviewPolicy must be human or auto"))
				}
			}
			if node.Kind == NodeAIAnalysis && len(node.AllowedTools) > 0 {
				diagnostics = append(diagnostics, diagnostic("error", "analysis_tools", path+".allowedTools", "AI Analysis nodes cannot call tools; use Agent Stage for tool-assisted work"))
			}
			if node.Kind == NodeAIAnalysis && node.SkillRouting {
				diagnostics = append(diagnostics, diagnostic("error", "analysis_skill_routing", path+".skillRouting", "AI Analysis nodes cannot load Skills; use Agent Stage for Skill-assisted work"))
			}
			seenTools := map[string]bool{}
			allowedToolNames := append([]string(nil), node.AllowedTools...)
			if node.Kind == NodeAgentStage && node.SkillRouting {
				allowedToolNames = append(allowedToolNames, workflowSkillReadTools...)
			}
			for toolIndex, toolName := range allowedToolNames {
				toolName = strings.TrimSpace(toolName)
				toolPath := fmt.Sprintf("%s.allowedTools[%d]", path, toolIndex)
				fromSkillRouting := toolIndex >= len(node.AllowedTools)
				if toolName == "" || seenTools[toolName] {
					if !fromSkillRouting {
						diagnostics = append(diagnostics, diagnostic("error", "invalid_allowed_tool", toolPath, "Agent Stage tools must be non-empty and unique"))
					}
					continue
				}
				seenTools[toolName] = true
				if strings.HasPrefix(toolName, "builtin.skill.") && !fromSkillRouting {
					diagnostics = append(diagnostics, diagnostic("error", "forbidden_tool", toolPath, "Skill tools are controlled by skillRouting and cannot be listed directly"))
					continue
				}
				definition, definitionErr := c.registry.Definition(ctx, toolName)
				if definitionErr != nil {
					diagnostics = append(diagnostics, diagnostic("error", "unknown_tool", toolPath, fmt.Sprintf("Tool %q is not currently registered", toolName)))
					continue
				}
				if node.Kind == NodeAgentStage && !agentStageToolSafe(definition) {
					diagnostics = append(diagnostics, diagnostic("error", "unsafe_agent_tool", toolPath, "Agent Stage tools must be idempotent observation tools or the exact host-defined selected-task material capability; use explicit Workflow nodes for other writes, execution, dependency changes, secrets or external paths"))
					continue
				}
				permissions, _ := json.Marshal(definition.Permissions)
				draft.node.AllowedTools = append(draft.node.AllowedTools, ToolSnapshot{QualifiedName: definition.QualifiedName, Version: definition.Version, Risk: string(definition.Risk), Permissions: permissions, Idempotent: definition.Idempotent, InputSchema: cloneRaw(definition.InputSchema), OutputSchema: cloneRaw(definition.OutputSchema)})
			}
			sort.Slice(draft.node.AllowedTools, func(i, j int) bool {
				return draft.node.AllowedTools[i].QualifiedName < draft.node.AllowedTools[j].QualifiedName
			})
		default:
			diagnostics = append(diagnostics, diagnostic("error", "unknown_node_kind", path+".kind", "Unsupported Workflow node kind"))
		}
		drafts[node.ID] = draft
	}

	targets := map[string]bool{}
	dependencies := map[string]map[string]bool{}
	for index, edge := range definition.Edges {
		path := fmt.Sprintf("edges[%d]", index)
		to := drafts[edge.ToNode]
		if to == nil {
			diagnostics = append(diagnostics, diagnostic("error", "dangling_target", path+".toNode", "Edge target node does not exist"))
			continue
		}
		toType, ok := to.inputs[edge.ToPort]
		if !ok {
			diagnostics = append(diagnostics, diagnostic("error", "unknown_input_port", path+".toPort", "Target input port is not declared by the Tool schema"))
			continue
		}
		key := edge.ToNode + "\x00" + edge.ToPort
		if targets[key] {
			diagnostics = append(diagnostics, diagnostic("error", "duplicate_input", path, "A node input can have only one incoming edge"))
			continue
		}
		targets[key] = true
		var fromType DataType
		if edge.FromNode == "$input" {
			fromType, ok = inputTypes[edge.FromPort]
		} else if from := drafts[edge.FromNode]; from != nil {
			fromType, ok = resolveOutputPort(from.outputs, edge.FromPort)
			if ok {
				if dependencies[edge.ToNode] == nil {
					dependencies[edge.ToNode] = map[string]bool{}
				}
				dependencies[edge.ToNode][edge.FromNode] = true
			}
		}
		if !ok {
			diagnostics = append(diagnostics, diagnostic("error", "unknown_output_port", path, "Edge source or output port does not exist"))
			continue
		}
		if !assignable(fromType, toType) {
			diagnostics = append(diagnostics, diagnostic("error", "type_mismatch", path, fmt.Sprintf("Cannot connect %s to %s", fromType, toType)))
		}
	}
	for _, node := range definition.Nodes {
		draft := drafts[node.ID]
		if draft == nil {
			continue
		}
		arguments := decodeObject(node.Arguments)
		for name := range draft.required {
			if _, literal := arguments[name]; !literal && !targets[node.ID+"\x00"+name] {
				diagnostics = append(diagnostics, diagnostic("error", "missing_required_input", "nodes."+node.ID+".arguments."+name, "Required Tool input has no literal value or incoming edge"))
			}
		}
	}

	order, cycle := topological(definition.Nodes, dependencies)
	if cycle {
		diagnostics = append(diagnostics, diagnostic("error", "cycle", "edges", "Workflow graph contains a cycle"))
	}
	for index, output := range definition.Outputs {
		from := drafts[output.FromNode]
		actual, ok := TypeAny, false
		if from != nil {
			actual, ok = resolveOutputPort(from.outputs, output.FromPort)
		}
		if !ok {
			diagnostics = append(diagnostics, diagnostic("error", "unknown_workflow_output", fmt.Sprintf("outputs[%d]", index), "Workflow output source does not exist"))
		} else if !assignable(actual, output.Type) {
			diagnostics = append(diagnostics, diagnostic("error", "type_mismatch", fmt.Sprintf("outputs[%d]", index), fmt.Sprintf("Cannot expose %s as %s", actual, output.Type)))
		}
	}

	compiled := Compilation{SchemaVersion: SchemaVersion, CompilerVersion: CompilerVersion, DefinitionSHA256: definitionHash, Order: order, Nodes: []CompiledNode{}, Edges: nonNilEdges(definition.Edges), Inputs: nonNilPorts(definition.Inputs), Outputs: nonNilOutputs(definition.Outputs), Diagnostics: nonNilDiagnostics(diagnostics)}
	compiledOrder := append([]string(nil), order...)
	if hasErrors(diagnostics) {
		seen := make(map[string]bool, len(compiledOrder))
		for _, id := range compiledOrder {
			seen[id] = true
		}
		for _, node := range definition.Nodes {
			if !seen[node.ID] {
				compiledOrder = append(compiledOrder, node.ID)
				seen[node.ID] = true
			}
		}
	}
	for _, id := range compiledOrder {
		if draft := drafts[id]; draft != nil {
			for dependency := range dependencies[id] {
				draft.node.Dependencies = append(draft.node.Dependencies, dependency)
			}
			sort.Strings(draft.node.Dependencies)
			compiled.Nodes = append(compiled.Nodes, draft.node)
		}
	}
	withoutHash := compiled
	withoutHash.CompilationSHA256 = ""
	compiledJSON, _ := canonicalJSON(withoutHash)
	compiled.CompilationSHA256 = hashBytes(compiledJSON)
	if hasErrors(diagnostics) {
		return compiled, fmt.Errorf("Workflow static validation failed")
	}
	return compiled, nil
}

func agentStageToolSafe(definition tool.Definition) bool {
	// A single content-addressed task-material capability is explicitly allowed;
	// this is not a blanket workspace.write exception or a name-only allowlist.
	if tool.IsResearchMaterialDefinition(definition) {
		return true
	}

	if !definition.Idempotent || (definition.Risk != tool.RiskLow && definition.Risk != tool.RiskModerate) {
		return false
	}
	for _, permission := range definition.Permissions {
		if permission.Kind != tool.PermissionWorkspaceRead && permission.Kind != tool.PermissionNetworkDomain {
			return false
		}
	}
	return true
}

func (c *Compiler) Preview(ctx context.Context, definition Definition) Preview {
	compiled, err := c.Compile(ctx, definition)
	result := Preview{Valid: err == nil, DefinitionSHA256: compiled.DefinitionSHA256, CompilationSHA256: compiled.CompilationSHA256, Diagnostics: nonNilDiagnostics(compiled.Diagnostics), Nodes: []PreviewNode{}, EdgeCount: len(definition.Edges), InputCount: len(definition.Inputs), OutputCount: len(definition.Outputs)}
	for index, node := range compiled.Nodes {
		value := PreviewNode{Ordinal: index + 1, ID: node.ID, Name: node.Name, Kind: node.Kind, Permissions: json.RawMessage(`[]`), SideEffect: node.SideEffect, Summary: nodeSummary(node)}
		if node.Tool != nil {
			value.ToolName, value.ToolVersion, value.Risk, value.Permissions, value.Idempotent = node.Tool.QualifiedName, node.Tool.Version, node.Tool.Risk, cloneRaw(node.Tool.Permissions), node.Tool.Idempotent
		}
		result.Nodes = append(result.Nodes, value)
	}
	return result
}

func (c *Compiler) validateShape(value Definition) []Diagnostic {
	result := []Diagnostic{}
	if value.SchemaVersion != SchemaVersion {
		result = append(result, diagnostic("error", "schema_version", "schemaVersion", "Unsupported Workflow schema version"))
	}
	if strings.TrimSpace(value.Name) == "" || len([]rune(value.Name)) > 120 {
		result = append(result, diagnostic("error", "invalid_name", "name", "Workflow name must contain 1-120 characters"))
	}
	if len([]rune(value.Description)) > 2_000 {
		result = append(result, diagnostic("error", "description_too_long", "description", "Workflow description exceeds 2000 characters"))
	}
	if len(value.Nodes) == 0 || len(value.Nodes) > maxNodes {
		result = append(result, diagnostic("error", "node_count", "nodes", "Workflow must contain 1-128 nodes"))
	}
	if len(value.Edges) > maxEdges {
		result = append(result, diagnostic("error", "edge_count", "edges", "Workflow exceeds 512 edges"))
	}
	if len(value.Inputs) > maxPorts || len(value.Outputs) > maxPorts {
		result = append(result, diagnostic("error", "port_count", "$", "Workflow exceeds 64 inputs or outputs"))
	}
	seen := map[string]bool{}
	for index, input := range value.Inputs {
		path := fmt.Sprintf("inputs[%d]", index)
		if !identifierPattern.MatchString(input.Name) || seen["input:"+input.Name] {
			result = append(result, diagnostic("error", "invalid_input", path+".name", "Input names must be unique lower-case identifiers"))
		}
		seen["input:"+input.Name] = true
		if sensitiveName(input.Name) {
			result = append(result, diagnostic("error", "secret_input", path+".name", "Workflow definitions cannot carry secrets or credentials"))
		}
		if !validDataType(input.Type) {
			result = append(result, diagnostic("error", "input_type", path+".type", "Unsupported input type"))
		}
		if input.FileKind != "" {
			if input.Type != TypeString && input.Type != TypeArray {
				result = append(result, diagnostic("error", "file_input_type", path+".fileKind", "File inputs must use string or array type"))
			}
			switch input.FileKind {
			case "delimited", "xlsx", "tabular":
			default:
				result = append(result, diagnostic("error", "file_input_kind", path+".fileKind", "Unsupported Workflow file input kind"))
			}
		}
		if input.Control != "" {
			switch input.Control {
			case "analysis_request":
				if input.Type != TypeObject {
					result = append(result, diagnostic("error", "input_control_type", path+".control", "Analysis request controls must use object type"))
				}
			default:
				result = append(result, diagnostic("error", "input_control", path+".control", "Unsupported Workflow input control"))
			}
		}
		if input.MinItems < 0 || input.MaxItems < 0 || input.MaxItems > 0 && input.MinItems > input.MaxItems {
			result = append(result, diagnostic("error", "input_item_bounds", path, "Workflow input item bounds are invalid"))
		}
		if (input.MinItems > 0 || input.MaxItems > 0) && input.Type != TypeArray && input.Type != TypeArtifacts && input.Type != TypeCitations {
			result = append(result, diagnostic("error", "input_item_type", path, "Item bounds require an array-like input type"))
		}
	}
	for index, node := range value.Nodes {
		path := fmt.Sprintf("nodes[%d]", index)
		if !identifierPattern.MatchString(node.ID) || seen["node:"+node.ID] {
			result = append(result, diagnostic("error", "invalid_node_id", path+".id", "Node IDs must be unique lower-case identifiers"))
		}
		seen["node:"+node.ID] = true
		if strings.TrimSpace(node.Name) == "" || len([]rune(node.Name)) > 120 {
			result = append(result, diagnostic("error", "invalid_node_name", path+".name", "Node name must contain 1-120 characters"))
		}
		if len(node.Arguments) > 256*1024 {
			result = append(result, diagnostic("error", "arguments_too_large", path+".arguments", "Node arguments exceed 256 KiB"))
		}
		trimmed := bytes.TrimSpace(node.Arguments)
		if len(trimmed) > 0 && (!json.Valid(trimmed) || trimmed[0] != '{') {
			result = append(result, diagnostic("error", "invalid_arguments", path+".arguments", "Node arguments must be a JSON object"))
		}
		if len(trimmed) > 0 {
			result = validateSensitiveValues(trimmed, path+".arguments", result)
		}
		if node.Kind != NodeAIAnalysis && node.Kind != NodeAgentStage && (strings.TrimSpace(node.PromptVersion) != "" || len(node.AllowedTools) > 0 || node.SkillRouting || node.ReviewPolicy != "" || len(bytes.TrimSpace(node.OutputSchema)) > 0) {
			result = append(result, diagnostic("error", "unexpected_ai_fields", path, "promptVersion, allowedTools, skillRouting, reviewPolicy and outputSchema are only valid for AI nodes"))
		}
	}
	for index, output := range value.Outputs {
		path := fmt.Sprintf("outputs[%d]", index)
		if !identifierPattern.MatchString(output.Name) || seen["output:"+output.Name] {
			result = append(result, diagnostic("error", "invalid_output", path+".name", "Output names must be unique lower-case identifiers"))
		}
		seen["output:"+output.Name] = true
		if !validDataType(output.Type) {
			result = append(result, diagnostic("error", "output_type", path+".type", "Unsupported output type"))
		}
	}
	for index, input := range value.Inputs {
		if len(bytes.TrimSpace(input.Default)) == 0 {
			continue
		}
		path := fmt.Sprintf("inputs[%d].default", index)
		actual, err := rawJSONType(input.Default)
		if err != nil {
			result = append(result, diagnostic("error", "invalid_default", path, "Input default must be valid JSON"))
			continue
		}
		if !assignable(actual, input.Type) {
			result = append(result, diagnostic("error", "default_type", path, fmt.Sprintf("Default %s is not assignable to %s", actual, input.Type)))
		} else if err := validateRuntimePortConstraints(input, input.Default); err != nil {
			result = append(result, diagnostic("error", "default_constraint", path, err.Error()))
		}
		result = validateSensitiveValues(input.Default, path, result)
		if isPathName(input.Name) {
			result = validatePathValue(input.Default, path, result)
		}
	}
	return result
}

func schemaPorts(schema json.RawMessage, path string, diagnostics []Diagnostic) (map[string]DataType, map[string]bool, []Diagnostic) {
	ports, required := map[string]DataType{}, map[string]bool{}
	var root map[string]any
	if json.Unmarshal(schema, &root) != nil {
		return ports, required, append(diagnostics, diagnostic("error", "tool_schema", path, "Tool input schema is not a JSON object"))
	}
	properties, _ := root["properties"].(map[string]any)
	for name, raw := range properties {
		if item, ok := raw.(map[string]any); ok {
			ports[name] = schemaType(item)
			if sensitiveName(name) {
				diagnostics = append(diagnostics, diagnostic("error", "secret_tool_input", path+"."+name, "Workflow tools cannot expose secret-bearing input fields"))
			}
		}
	}
	if values, ok := root["required"].([]any); ok {
		for _, value := range values {
			if name, ok := value.(string); ok {
				required[name] = true
			}
		}
	}
	return ports, required, diagnostics
}

func resultPorts(schema json.RawMessage) map[string]DataType {
	result := map[string]DataType{"status": TypeString, "text": TypeString, "structured": TypeObject, "artifacts": TypeArtifacts, "citations": TypeCitations}
	var root map[string]any
	if json.Unmarshal(schema, &root) == nil {
		if properties, ok := root["properties"].(map[string]any); ok {
			for name, raw := range properties {
				if item, ok := raw.(map[string]any); ok {
					result["structured."+name] = schemaType(item)
				}
			}
		}
	}
	return result
}

func aiAnalysisPorts(schema json.RawMessage) map[string]DataType {
	result := map[string]DataType{}
	var root map[string]any
	if json.Unmarshal(schema, &root) != nil {
		return result
	}
	properties, _ := root["properties"].(map[string]any)
	for name, raw := range properties {
		if !schemaFieldPattern.MatchString(name) || sensitiveName(name) {
			continue
		}
		if item, ok := raw.(map[string]any); ok {
			result[name] = schemaType(item)
		}
	}
	return result
}

func validateArguments(raw json.RawMessage, ports map[string]DataType, path string, diagnostics []Diagnostic) []Diagnostic {
	for name, value := range decodeObject(raw) {
		if sensitiveName(name) {
			diagnostics = append(diagnostics, diagnostic("error", "secret_argument", path+"."+name, "Workflow definitions cannot contain secret-bearing arguments"))
			continue
		}
		typeName, ok := ports[name]
		if !ok {
			diagnostics = append(diagnostics, diagnostic("error", "unknown_argument", path+"."+name, "Argument is not declared by the Tool schema"))
			continue
		}
		if actual := jsonType(value); !assignable(actual, typeName) {
			diagnostics = append(diagnostics, diagnostic("error", "argument_type", path+"."+name, fmt.Sprintf("Literal %s is not assignable to %s", actual, typeName)))
		}
	}
	return diagnostics
}

func validateSensitiveValues(raw json.RawMessage, path string, diagnostics []Diagnostic) []Diagnostic {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return diagnostics
	}
	var walk func(any, string)
	walk = func(current any, currentPath string) {
		switch typed := current.(type) {
		case map[string]any:
			for name, child := range typed {
				next := currentPath + "." + name
				if sensitiveName(name) {
					diagnostics = append(diagnostics, diagnostic("error", "secret_argument", next, "Workflow definitions cannot contain secret-bearing fields"))
				}
				walk(child, next)
			}
		case []any:
			for index, child := range typed {
				walk(child, fmt.Sprintf("%s[%d]", currentPath, index))
			}
		}
	}
	walk(value, path)
	return diagnostics
}

func validatePathArguments(raw json.RawMessage, path string, diagnostics []Diagnostic) []Diagnostic {
	for name, value := range decodeObject(raw) {
		if !isPathName(name) {
			continue
		}
		encoded, err := json.Marshal(value)
		if err == nil {
			diagnostics = validatePathValue(encoded, path+"."+name, diagnostics)
		}
	}
	return diagnostics
}

func validatePathValue(raw json.RawMessage, path string, diagnostics []Diagnostic) []Diagnostic {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return diagnostics
	}
	values := []string{}
	if text, ok := value.(string); ok {
		values = append(values, text)
	}
	if list, ok := value.([]any); ok {
		for _, item := range list {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
	}
	for _, text := range values {
		text = strings.ReplaceAll(strings.ReplaceAll(text, "{{runId}}", "workflow-run"), "{{attempt}}", "1")
		clean := filepath.Clean(strings.TrimSpace(text))
		segments := strings.FieldsFunc(filepath.ToSlash(clean), func(r rune) bool { return r == '/' })
		private := false
		for _, segment := range segments {
			if strings.EqualFold(segment, ".sciaide") {
				private = true
				break
			}
		}
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || private {
			diagnostics = append(diagnostics, diagnostic("error", "path_escape", path, "Workflow paths must stay inside the Workspace and outside .sciaide"))
		}
	}
	return diagnostics
}

func runtimePathError(raw json.RawMessage, path string) error {
	diagnostics := validatePathValue(raw, path, nil)
	if len(diagnostics) > 0 {
		return fmt.Errorf("%s: %s", path, diagnostics[0].Message)
	}
	return nil
}

func isPathName(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "path") || lower == "workdir"
}

func topological(nodes []Node, deps map[string]map[string]bool) ([]string, bool) {
	indegree, outgoing := map[string]int{}, map[string][]string{}
	for _, node := range nodes {
		indegree[node.ID] = 0
	}
	for target, sources := range deps {
		for source := range sources {
			indegree[target]++
			outgoing[source] = append(outgoing[source], target)
		}
	}
	ready := []string{}
	for id, count := range indegree {
		if count == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	order := []string{}
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)
		sort.Strings(outgoing[id])
		for _, target := range outgoing[id] {
			indegree[target]--
			if indegree[target] == 0 {
				ready = append(ready, target)
				sort.Strings(ready)
			}
		}
	}
	return order, len(order) != len(nodes)
}

func resolveOutputPort(values map[string]DataType, path string) (DataType, bool) {
	value, ok := values[path]
	return value, ok
}
func schemaType(value map[string]any) DataType {
	if values, ok := value["type"].([]any); ok {
		for _, raw := range values {
			if text, ok := raw.(string); ok && text != "null" {
				return DataType(text)
			}
		}
	}
	if text, ok := value["type"].(string); ok {
		return DataType(text)
	}
	return TypeAny
}
func jsonType(value any) DataType {
	switch typed := value.(type) {
	case string:
		return TypeString
	case bool:
		return TypeBoolean
	case json.Number:
		if _, err := typed.Int64(); err == nil {
			return TypeInteger
		}
		return TypeNumber
	case float64:
		if !math.IsNaN(typed) && !math.IsInf(typed, 0) && math.Trunc(typed) == typed {
			return TypeInteger
		}
		return TypeNumber
	case []any:
		return TypeArray
	case map[string]any:
		return TypeObject
	case nil:
		return TypeAny
	default:
		return TypeAny
	}
}

func rawJSONType(raw json.RawMessage) (DataType, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return TypeAny, err
	}
	return jsonType(value), nil
}
func assignable(from, to DataType) bool {
	return from == TypeAny || to == TypeAny || from == to || from == TypeInteger && to == TypeNumber || from == TypeArtifacts && to == TypeArray || from == TypeCitations && to == TypeArray
}
func validDataType(value DataType) bool {
	switch value {
	case TypeAny, TypeString, TypeNumber, TypeInteger, TypeBoolean, TypeObject, TypeArray, TypeArtifacts, TypeCitations:
		return true
	}
	return false
}
func sensitiveName(name string) bool {
	lower := strings.ToLower(name)
	for _, marker := range []string{"secret", "password", "credential", "apikey", "api_key", "token", "authorization", "cookie"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
func diagnostic(severity, code, path, message string) Diagnostic {
	return Diagnostic{Severity: severity, Code: code, Path: path, Message: message}
}
func hasErrors(values []Diagnostic) bool {
	for _, value := range values {
		if value.Severity == "error" {
			return true
		}
	}
	return false
}
func canonicalJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var normalized any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

// VerifyVersionSnapshot protects Repository implementations from persisting a
// caller-supplied definition or compilation whose immutable hashes disagree.
func VerifyVersionSnapshot(value Version) error {
	definitionJSON, err := canonicalJSON(value.Definition)
	if err != nil {
		return fmt.Errorf("encode Workflow definition: %w", err)
	}
	if hashBytes(definitionJSON) != value.DefinitionSHA256 || value.Compilation.DefinitionSHA256 != value.DefinitionSHA256 {
		return fmt.Errorf("Workflow definition snapshot hash does not match")
	}
	compilation := value.Compilation
	compilation.CompilationSHA256 = ""
	compilationJSON, err := canonicalJSON(compilation)
	if err != nil {
		return fmt.Errorf("encode Workflow compilation: %w", err)
	}
	if hashBytes(compilationJSON) != value.CompilationSHA256 || value.Compilation.CompilationSHA256 != value.CompilationSHA256 {
		return fmt.Errorf("Workflow compilation snapshot hash does not match")
	}
	if hasErrors(value.Compilation.Diagnostics) {
		return fmt.Errorf("invalid Workflow compilation cannot be persisted")
	}
	return nil
}
func hashBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
func cloneRaw(value json.RawMessage) json.RawMessage { return append(json.RawMessage(nil), value...) }
func nonEmptyObject(value json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(value)) == 0 {
		return json.RawMessage(`{}`)
	}
	return cloneRaw(value)
}
func decodeObject(value json.RawMessage) map[string]any {
	result := map[string]any{}
	decoder := json.NewDecoder(bytes.NewReader(nonEmptyObject(value)))
	decoder.UseNumber()
	_ = decoder.Decode(&result)
	return result
}
func nonNilEdges(value []Edge) []Edge       { return append(make([]Edge, 0, len(value)), value...) }
func nonNilPorts(value []Port) []Port       { return append(make([]Port, 0, len(value)), value...) }
func nonNilOutputs(value []Output) []Output { return append(make([]Output, 0, len(value)), value...) }
func nonNilDiagnostics(value []Diagnostic) []Diagnostic {
	return append(make([]Diagnostic, 0, len(value)), value...)
}
func nodeSummary(value CompiledNode) string {
	if value.Kind == NodeHumanConfirmation {
		return "等待人工确认"
	}
	if value.Kind == NodeCandidateSelection {
		return "从冻结的公共数据库候选中人工筛选"
	}
	if value.Kind == NodeCitationSelection {
		return "从可信本地证据中选择引用"
	}
	if value.Kind == NodeAIAnalysis {
		return "AI 结构化分析（不调用工具）"
	}
	if value.Kind == NodeAgentStage {
		review := "自动校验并继续"
		if value.ReviewPolicy != AIReviewAuto {
			review = "等待人工复核"
		}
		skill := ""
		if value.SkillRouting {
			skill = " · 动态 Skill"
		}
		return fmt.Sprintf("阶段内 AI 协作 · %d 个限定工具 · %s%s", len(value.AllowedTools), review, skill)
	}
	if value.Tool == nil {
		return string(value.Kind)
	}
	object := decodeObject(value.Arguments)
	for _, key := range []string{"command", "code", "scriptPath", "query"} {
		if text, ok := object[key].(string); ok {
			runes := []rune(strings.TrimSpace(text))
			if len(runes) > 120 {
				text = string(runes[:120]) + "..."
			}
			return text
		}
	}
	return value.Tool.QualifiedName
}

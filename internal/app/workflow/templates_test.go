package workflow

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestReferenceTemplatesCompileAgainstResearchToolContracts(t *testing.T) {
	registry := tool.NewRegistry()
	definitions := []tool.Definition{
		{QualifiedName: "builtin.research.workflow.search", Version: "1", Risk: tool.RiskModerate, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","required":["query"],"properties":{"query":{"type":"string"},"limit":{"type":"integer"}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"queryText":{"type":"string"},"candidates":{"type":"array"}}}`)},
		{QualifiedName: "builtin.research.workflow.import", Version: "1", Risk: tool.RiskHigh, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","required":["selectedCandidateIds"],"properties":{"selectedCandidateIds":{"type":"array"},"mode":{"type":"string"}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"attachmentIds":{"type":"array"}}}`)},
		{QualifiedName: "builtin.research.workflow.sync", Version: "1", Risk: tool.RiskModerate, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","required":["attachmentIds"],"properties":{"attachmentIds":{"type":"array"}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"documentIds":{"type":"array"}}}`)},
		{QualifiedName: "builtin.knowledge.search", Version: "3", Risk: tool.RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","required":["query"],"properties":{"query":{"type":"string"},"limit":{"type":"integer"},"documentIds":{"type":"array"}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"matches":{"type":"array"}}}`)},
		{QualifiedName: "builtin.research.workflow.python.ensure", Version: "1", Risk: tool.RiskHigh, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","properties":{"context":{}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"environmentFingerprint":{"type":"string"}}}`)},
		{QualifiedName: "builtin.python.kernel.execute", Version: "1", Risk: tool.RiskHigh, Idempotent: false, InputSchema: json.RawMessage(`{"type":"object","required":["code"],"properties":{"code":{"type":"string"},"inputPaths":{"type":"array"},"inputData":{},"expectedEnvironmentFingerprint":{"type":"string"},"outputPaths":{"type":"array"},"timeoutSeconds":{"type":"integer"}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"reproductionSha256":{"type":"string"}}}`)},
		{QualifiedName: "builtin.research.workflow.report", Version: "1", Risk: tool.RiskHigh, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","required":["name","markdown","citations"],"properties":{"name":{"type":"string"},"markdown":{"type":"string"},"citations":{"type":"array"},"analysis":{"type":"object"},"sourceArtifacts":{"type":"array"}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"artifact":{"type":"object"}}}`)},
	}
	for index := range definitions {
		definitions[index].Description = "Workflow template fixture tool"
	}
	for _, definition := range definitions {
		if err := registry.Register(context.Background(), fixtureTool{definition: definition}); err != nil {
			t.Fatal(err)
		}
	}
	for _, template := range ReferenceTemplates() {
		t.Run(template.ID, func(t *testing.T) {
			compiled, err := NewCompiler(registry).Compile(context.Background(), template.Definition)
			if err != nil {
				preview := NewCompiler(registry).Preview(context.Background(), template.Definition)
				t.Fatalf("template does not compile: %v; diagnostics=%#v", err, preview.Diagnostics)
			}
			if len(compiled.Order) != len(template.Definition.Nodes) {
				t.Fatalf("compiled order=%#v", compiled.Order)
			}
		})
	}
}

func TestXLSXAnalysisTemplateBindsReadOnlyInputAndDeclaresReproducibleOutputs(t *testing.T) {
	var selected *Template
	for _, value := range ReferenceTemplates() {
		if value.ID == "xlsx-descriptive-analysis" {
			copy := value
			selected = &copy
			break
		}
	}
	if selected == nil {
		t.Fatal("XLSX analysis template is missing")
	}
	if len(selected.Definition.Inputs) != 1 || selected.Definition.Inputs[0].Name != "input_paths" || selected.Definition.Inputs[0].Type != TypeArray || selected.Definition.Inputs[0].FileKind != "xlsx" || selected.Definition.Inputs[0].MinItems != 1 || selected.Definition.Inputs[0].MaxItems != 1 || !selected.Definition.Inputs[0].Required {
		t.Fatalf("XLSX template input = %#v", selected.Definition.Inputs)
	}
	var analysis Node
	for _, node := range selected.Definition.Nodes {
		if node.ID == "analysis" {
			analysis = node
		}
	}
	var arguments struct {
		Code        string   `json:"code"`
		OutputPaths []string `json:"outputPaths"`
	}
	if json.Unmarshal(analysis.Arguments, &arguments) != nil || !strings.Contains(arguments.Code, "zipfile.ZipFile") || len(arguments.OutputPaths) != 4 {
		t.Fatalf("XLSX analysis arguments = %#v", analysis.Arguments)
	}
	if !slices.Contains(arguments.OutputPaths, "analysis-output/xlsx-{{runId}}-{{attempt}}-analysis.py") {
		t.Fatalf("XLSX reproducible script output is missing: %#v", arguments.OutputPaths)
	}
	foundInputEdge := false
	for _, edge := range selected.Definition.Edges {
		foundInputEdge = foundInputEdge || edge.FromNode == "$input" && edge.FromPort == "input_paths" && edge.ToNode == "analysis" && edge.ToPort == "inputPaths"
	}
	if !foundInputEdge {
		t.Fatal("XLSX input is not bound to the Kernel inputPaths contract")
	}
}

func TestReferenceTemplatesReturnIndependentDefinitions(t *testing.T) {
	first := ReferenceTemplates()
	first[0].Definition.Name = "changed"
	first[0].Definition.Nodes[0].Name = "changed"
	second := ReferenceTemplates()
	if second[0].Definition.Name == "changed" || second[0].Definition.Nodes[0].Name == "changed" {
		t.Fatal("reference templates share mutable state")
	}
}

func TestDelimitedAnalysisTemplateUsesStructuredGoalAndDeclaresReplayEvidence(t *testing.T) {
	var selected *Template
	for _, value := range ReferenceTemplates() {
		if value.ID == "python-analysis" {
			copy := value
			selected = &copy
			break
		}
	}
	if selected == nil || selected.Name != "数据探索与可复现分析" {
		t.Fatalf("delimited analysis template = %#v", selected)
	}
	if len(selected.Definition.Inputs) != 2 || selected.Definition.Inputs[0].Name != "input_paths" || selected.Definition.Inputs[0].FileKind != "delimited" || selected.Definition.Inputs[0].MinItems != 1 || selected.Definition.Inputs[0].MaxItems != 1 || selected.Definition.Inputs[1].Name != "analysis_request" || selected.Definition.Inputs[1].Control != "analysis_request" {
		t.Fatalf("delimited inputs = %#v", selected.Definition.Inputs)
	}
	var analysis Node
	for _, node := range selected.Definition.Nodes {
		if node.ID == "analysis" {
			analysis = node
		}
	}
	var arguments struct {
		Code        string   `json:"code"`
		OutputPaths []string `json:"outputPaths"`
	}
	if json.Unmarshal(analysis.Arguments, &arguments) != nil || !strings.Contains(arguments.Code, "SCIAIDE_DATA") || !strings.Contains(arguments.Code, "MAX_ROWS = 200000") || len(arguments.OutputPaths) != 5 {
		t.Fatalf("delimited analysis arguments = %#v", analysis.Arguments)
	}
	if !slices.Contains(arguments.OutputPaths, "analysis-output/data-{{runId}}-{{attempt}}-analysis.py") || !slices.Contains(arguments.OutputPaths, "analysis-output/data-{{runId}}-{{attempt}}-methods.md") {
		t.Fatalf("delimited replay outputs = %#v", arguments.OutputPaths)
	}
	foundData := false
	for _, edge := range selected.Definition.Edges {
		foundData = foundData || edge.FromNode == "$input" && edge.FromPort == "analysis_request" && edge.ToNode == "analysis" && edge.ToPort == "inputData"
	}
	if !foundData {
		t.Fatal("analysis request is not bound as structured Kernel input")
	}
}

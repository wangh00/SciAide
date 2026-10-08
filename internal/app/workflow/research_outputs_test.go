package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResearchOutputPromptSupportsDeclaredFilesThroughoutNewRoute(t *testing.T) {
	template, _, _, err := dynamicResearchRouteTemplate(starterDataRoute(), ResearchStarterContext{ResearchIdea: "compare results and export tables and figures", StageCatalog: dynamicResearchStageCatalog()}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range template.Definition.Nodes {
		if node.ID != "method_implementation" {
			continue
		}
		prompt := buildAIStagePrompt(RunDetail{}, Step{Input: raw(`{}`)}, CompiledNode{ID: node.ID, Prompt: node.Prompt, OutputSchema: node.OutputSchema})
		for _, forbidden := range []string{"是两个暂存输出路径", "只写入 SCIAIDE_OUTPUTS[0]", "Do not call savefig", "do not write any file other than SCIAIDE_OUTPUTS[0] and SCIAIDE_OUTPUTS[1]"} {
			if strings.Contains(prompt, forbidden) {
				t.Fatalf("conflicting instruction: %s", forbidden)
			}
		}
		for _, required := range []string{"SCIAIDE_OUTPUTS[2+i]", "outputDeclarations", "matching declared SCIAIDE_OUTPUTS path", "automatically captures open Matplotlib figures"} {
			if !strings.Contains(prompt, required) {
				t.Fatalf("missing output guidance: %s", required)
			}
		}
		return
	}
	t.Fatal("missing implementation stage")
}

func TestResearchOutputDeclarationsBindOnlyHostPaths(t *testing.T) {
	for _, value := range []string{`{"outputDeclarations":[{"name":"../escape","format":"csv"}]}`, `{"outputDeclarations":[{"name":"clean","format":"exe"}]}`, `{"outputDeclarations":[{"name":"clean","format":"csv"},{"name":"clean","format":"png"}]}`} {
		if _, err := declaredResearchOutputPaths(raw(value)); err == nil {
			t.Fatal(value)
		}
	}
	analysis := raw(`{"outputDeclarations":[{"name":"clean","format":"csv"},{"name":"plot","format":"svg"}]}`)
	detail := RunDetail{Run: Run{Inputs: raw(`{}`)}, Steps: []Step{{NodeID: "method_implementation", Status: StepCompleted, Output: raw(`{"analysis":` + string(analysis) + `}`)}}}
	input, err := bindNodeInput(detail, CompiledNode{ID: "python_analysis", Kind: NodePython, Arguments: raw(`{"outputPaths":["results.json","methods.md"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Paths []string `json:"outputPaths"`
	}
	if json.Unmarshal(input, &decoded) != nil || len(decoded.Paths) != 4 || !strings.HasSuffix(decoded.Paths[2], "-clean.csv") || !strings.HasSuffix(decoded.Paths[3], "-plot.svg") {
		t.Fatal(string(input))
	}
}

func TestResearchOutputDeclarationsPreserveLegacyAndMultipleOutputs(t *testing.T) {
	for _, declarations := range []string{`{}`, `{"outputDeclarations":[]}`, `{"outputDeclarations":[{"name":"analysis_source","format":"py"},{"name":"readme","format":"txt"}]}`, `{"outputDeclarations":[{"name":"cleaned","format":"csv"},{"name":"comparison","format":"xlsx"},{"name":"figure","format":"png"}]}`} {
		detail := RunDetail{Run: Run{Inputs: raw(`{}`)}, Steps: []Step{{NodeID: "method_implementation", Status: StepCompleted, Output: raw(`{"analysis":` + declarations + `}`)}}}
		input, err := bindNodeInput(detail, CompiledNode{ID: "python_analysis", Kind: NodePython, Arguments: raw(`{"outputPaths":["results.json","methods.md"]}`)})
		if err != nil {
			t.Fatal(err)
		}
		var decoded struct {
			Paths []string `json:"outputPaths"`
		}
		if err := json.Unmarshal(input, &decoded); err != nil {
			t.Fatal(err)
		}
		extra, err := declaredResearchOutputPaths(raw(declarations))
		if err != nil {
			t.Fatal(err)
		}
		want := append([]string{"results.json", "methods.md"}, extra...)
		if strings.Join(decoded.Paths, "\n") != strings.Join(want, "\n") {
			t.Fatalf("paths = %v, want %v", decoded.Paths, want)
		}
		// Rebinding must not append declarations to previously bound state.
		again, err := bindNodeInput(detail, CompiledNode{ID: "python_analysis", Kind: NodePython, Arguments: raw(`{"outputPaths":["results.json","methods.md"]}`)})
		if err != nil || string(again) != string(input) {
			t.Fatalf("non-repeatable binding: %s, %v", again, err)
		}
	}
}

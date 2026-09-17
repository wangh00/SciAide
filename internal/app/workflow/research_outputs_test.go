package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

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

package agent

import (
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

func TestExecutionTaskReadProjectionPreservesFrozenContract(t *testing.T) {
	d := tool.Definition{QualifiedName: workflow.ResearchTaskReadTool, Description: "original", InputSchema: []byte(`{"type":"object","properties":{"section":{"type":"string","enum":["inputs","planning_discussion","stage_input","supplemental_materials","messages"]}}}`)}
	fingerprint := tool.DefinitionFingerprint(d)
	projected := executionTaskReadDefinitions([]tool.Definition{d}, true)
	if strings.Contains(string(projected[0].InputSchema), "supplemental_materials") || strings.Contains(string(projected[0].InputSchema), `"messages"`) || !strings.Contains(projected[0].Description, "CURRENT stage") || !strings.Contains(projected[0].Description, "ONLY") {
		t.Fatal(projected)
	}
	if tool.DefinitionFingerprint(d) != fingerprint || executionTaskReadDefinitions([]tool.Definition{d}, false)[0].Description != "original" {
		t.Fatal("projection changed immutable contract or discussion")
	}
	if strings.Contains(string(projected[0].InputSchema), `"revision_inputs"`) || strings.Contains(string(projected[0].InputSchema), `"stage_output"`) {
		t.Fatal("projection advertised sections absent from its original contract")
	}
	if !strings.Contains(string(projected[0].InputSchema), `"planning_discussion"`) || !strings.Contains(projected[0].Description, "immutable requirements") {
		t.Fatal("lost frozen negotiation reader")
	}
	for _, text := range []string{"concrete gap", "literal values in the current stage_input are already supplied", "finish necessary pagination", "independently verify disputed claims"} {
		if !strings.Contains(projected[0].Description, text) {
			t.Fatal("lost targeted read guidance", text)
		}
	}
	legacy := executionTaskReadDefinitions([]tool.Definition{d}, true, []byte(`{"type":"object","properties":{"section":{"type":"string","enum":["inputs","stage_input","messages"]}}}`))
	if strings.Contains(string(legacy[0].InputSchema), "planning_discussion") || strings.Contains(string(legacy[0].InputSchema), `"messages"`) || !strings.Contains(string(legacy[0].InputSchema), "stage_input") {
		t.Fatal("projection expanded the legacy frozen route")
	}
}

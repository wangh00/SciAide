package agent

import (
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

func TestExecutionTaskReadProjectionPreservesFrozenContract(t *testing.T) {
	d := tool.Definition{QualifiedName: workflow.ResearchTaskReadTool, Description: "original", InputSchema: []byte(`{"type":"object","properties":{"section":{"type":"string","enum":["inputs","stage_input","supplemental_materials","messages"]}}}`)}
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
}

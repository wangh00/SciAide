package agent

import (
	"encoding/json"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

// Wire-only narrowing: preserve the immutable registered/frozen tool contract
// and keep executor enforcement. Never advertise discussion-private sections
// to an execution that cannot read them.
func executionTaskReadDefinitions(values []tool.Definition, execution bool, frozenSchemas ...json.RawMessage) []tool.Definition {
	if !execution {
		return values
	}
	result := append([]tool.Definition(nil), values...)
	for i, d := range result {
		if d.QualifiedName != workflow.ResearchTaskReadTool {
			continue
		}
		if len(frozenSchemas) > 0 && len(frozenSchemas[0]) > 0 {
			d.InputSchema = frozenSchemas[0]
		}
		var schema map[string]any
		if json.Unmarshal(d.InputSchema, &schema) != nil {
			continue
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			continue
		}
		section, ok := props["section"].(map[string]any)
		if !ok {
			continue
		}
		visible := map[string]bool{}
		for _, name := range []string{"overview", "inputs", "planning_discussion", "effective_inputs", "data_inputs", "source_inputs", "source_content", "outputs", "artifacts", "stage_input", "stage_output", "revision_index", "revision_stage_input", "revision_stage_output", "revision_inputs", "revision_source_inputs", "revision_artifacts"} {
			visible[name] = true
		}
		original, ok := section["enum"].([]any)
		if !ok {
			continue
		}
		narrowed := []string{}
		for _, value := range original {
			if name, ok := value.(string); ok && visible[name] {
				narrowed = append(narrowed, name)
			}
		}
		section["enum"] = narrowed
		d.InputSchema, _ = json.Marshal(schema)
		d.Description = "Read frozen records of this executing research task. stage_input without nodeId reads the CURRENT stage; for another stage or stage_output use its exact nodeId from overview. Page using nextOffset until the required record is complete. source_content ONLY reads the confirmed Python method reference, never a paper/PDF/TXT. Read selected papers through the resource menu. Unadopted uploads and live private messages are unavailable here."
		d.Description += " Use this reader for a concrete gap, source/provenance check or version comparison; literal values in the current stage_input are already supplied; $read entries instead locate complete saved values that must be read when relevant. Reuse identical complete results still in view instead of routinely fetching a second copy; finish necessary pagination and independently verify disputed claims."
		if props["pointer"] != nil {
			d.Description += " " + stageMaterialInstruction
		}
		for _, name := range narrowed {
			if name == "planning_discussion" {
				d.Description += " planning_discussion reads only the immutable requirements negotiation frozen at planning, not live private messages or uploaded file contents."
			}
		}
		result[i] = d
	}
	return result
}

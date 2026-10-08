package agent

import (
	"encoding/json"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

// Wire-only narrowing: preserve the immutable registered/frozen tool contract
// and keep executor enforcement. Never advertise discussion-private sections
// to an execution that cannot read them.
func executionTaskReadDefinitions(values []tool.Definition, execution bool) []tool.Definition {
	if !execution {
		return values
	}
	result := append([]tool.Definition(nil), values...)
	for i, d := range result {
		if d.QualifiedName != workflow.ResearchTaskReadTool {
			continue
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
		for _, name := range []string{"overview", "inputs", "effective_inputs", "data_inputs", "source_inputs", "source_content", "outputs", "artifacts", "stage_input", "stage_output", "revision_index", "revision_stage_input", "revision_stage_output", "revision_inputs", "revision_source_inputs", "revision_artifacts"} {
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
		d.Description = "Read confirmed records of this executing research task. stage_input without nodeId reads the CURRENT stage; for another stage or stage_output use its exact nodeId from overview. Page using nextOffset until the required record is complete. source_content ONLY reads the confirmed Python method reference, never a paper/PDF/TXT. Read selected papers through the resource menu. Private uploads, messages and discussion history are unavailable here."
		result[i] = d
	}
	return result
}

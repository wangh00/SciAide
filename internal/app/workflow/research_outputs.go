package workflow

import (
	"encoding/json"
	"fmt"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func withResearchOutputs(schema json.RawMessage) json.RawMessage {
	var root map[string]any
	_ = json.Unmarshal(schema, &root)
	root["properties"].(map[string]any)["outputDeclarations"] = map[string]any{
		"type": "array", "maxItems": 14, "items": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"name", "format"},
			"properties": map[string]any{
				"name":   map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9_-]{0,47}$"},
				"format": map[string]any{"type": "string", "enum": []string{"csv", "tsv", "xlsx", "json", "md", "png", "svg", "pdf"}},
			},
		},
	}
	value, _ := json.Marshal(root)
	return value
}

func declaredResearchOutputPaths(analysis json.RawMessage) ([]string, error) {
	var value struct {
		Outputs []struct {
			Name   string `json:"name"`
			Format string `json:"format"`
		} `json:"outputDeclarations"`
	}
	if err := json.Unmarshal(analysis, &value); err != nil {
		return nil, err
	}
	if len(value.Outputs) > 14 {
		return nil, fmt.Errorf("too many declared research outputs")
	}
	paths := []string{}
	seen := map[string]bool{}
	for _, output := range value.Outputs {
		if output.Name == "results" || output.Name == "methods" {
			return nil, fmt.Errorf("output name %q is reserved for mandatory outputs", output.Name)
		}
		// Validate through the same schema before constructing any path.
		candidate, _ := json.Marshal(map[string]any{"outputDeclarations": []any{map[string]any{"name": output.Name, "format": output.Format}}})
		schema := withResearchOutputs(raw(`{"type":"object","properties":{},"additionalProperties":false}`))
		if err := (tool.JSONSchemaValidator{}).Validate(schema, candidate); err != nil {
			return nil, err
		}
		if seen[output.Name] {
			return nil, fmt.Errorf("duplicate output name %q", output.Name)
		}
		seen[output.Name] = true
		paths = append(paths, "analysis-output/dynamic-{{runId}}-{{attempt}}-"+output.Name+"."+output.Format)
	}
	return paths, nil
}

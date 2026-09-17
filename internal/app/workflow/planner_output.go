package workflow

import "encoding/json"

func plannerSchemaUsesLegacyRoutes(schema json.RawMessage) bool {
	var root struct {
		Properties map[string]struct {
			Items struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"items"`
		} `json:"properties"`
	}
	if json.Unmarshal(schema, &root) != nil {
		return false
	}
	_, ok := root.Properties["routes"].Items.Properties["stageIds"]
	return ok
}

// Only the explicit no-clarification decision permits a missing questions
// array to mean []. Preserve unknown fields, nulls, and all other missing
// fields so the frozen Schema can reject them. OutputText remains untouched
// in the execution audit; this is only the parser's working copy.
func normalizePlannerClarificationText(text string) string {
	value, err := extractStageJSON(text, json.RawMessage(`{"type":"object","additionalProperties":true}`))
	if err != nil {
		return text
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(value, &root) != nil {
		return text
	}
	var clarification map[string]json.RawMessage
	if json.Unmarshal(root["clarification"], &clarification) != nil || clarification == nil {
		return text
	}
	if string(clarification["needsUserInput"]) != "false" {
		return text
	}
	if _, exists := clarification["questions"]; exists {
		return text
	}
	clarification["questions"] = json.RawMessage(`[]`)
	root["clarification"], _ = json.Marshal(clarification)
	normalized, err := json.Marshal(root)
	if err != nil {
		return text
	}
	return string(normalized)
}

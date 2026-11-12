package tool

import (
	"bytes"
	"encoding/json"
)

// The only v9 delta is one optional pointer property. Preserve every other
// schema, permission and execution field for exact legacy fingerprint checks.
func ResearchTaskReadVersion8(current Definition) (Definition, bool) {
	if current.QualifiedName != "builtin.research.task.read" {
		return Definition{}, false
	}
	if current.Version == "8" {
		return current, true
	}
	if current.Version != "9" {
		return Definition{}, false
	}
	var schema map[string]json.RawMessage
	var props map[string]json.RawMessage
	if json.Unmarshal(current.InputSchema, &schema) != nil || json.Unmarshal(schema["properties"], &props) != nil || !bytes.Equal(canonicalJSON(props["pointer"]), canonicalJSON(json.RawMessage(`{"type":"string","maxLength":2048}`))) {
		return Definition{}, false
	}
	delete(props, "pointer")
	schema["properties"], _ = json.Marshal(props)
	current.InputSchema, _ = json.Marshal(schema)
	current.Version = "8"
	return current, true
}

// Version 8 only adds the frozen planning-discussion selector to version 7.
// Derive that exact old contract, not a general schema-subset exemption.
func ResearchTaskReadVersion7(current Definition) (Definition, bool) {
	var ok bool
	current, ok = ResearchTaskReadVersion8(current)
	if !ok {
		return Definition{}, false
	}
	var schema map[string]any
	if json.Unmarshal(current.InputSchema, &schema) != nil {
		return Definition{}, false
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return Definition{}, false
	}
	section, ok := properties["section"].(map[string]any)
	if !ok {
		return Definition{}, false
	}
	values, ok := section["enum"].([]any)
	if !ok {
		return Definition{}, false
	}
	prior, found := []any{}, false
	for _, v := range values {
		if v == "planning_discussion" {
			found = true
		} else {
			prior = append(prior, v)
		}
	}
	if !found {
		return Definition{}, false
	}
	section["enum"] = prior
	current.InputSchema, _ = json.Marshal(schema)
	current.Version = "7"
	return current, true
}

func ResearchTaskReadUpgradeCompatible(frozen, current Definition) bool {
	var prior Definition
	var ok bool
	switch frozen.Version {
	case "7":
		prior, ok = ResearchTaskReadVersion7(current)
	case "8":
		if current.Version == "9" {
			prior, ok = ResearchTaskReadVersion8(current)
		}
	}
	return ok && DefinitionFingerprint(frozen) == DefinitionFingerprint(prior)
}

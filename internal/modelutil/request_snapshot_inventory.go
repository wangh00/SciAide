package modelutil

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/wangh00/SciAide/internal/modelcap"
)

// A shape inventory is not a validator for provider request semantics. Only
// host-known labels are emitted; arbitrary field names/values remain private.
func snapshotObject(data []byte) (map[string]json.RawMessage, bool) {
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	values := map[string]json.RawMessage{}
	for d.More() {
		token, err = d.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return nil, false
		}
		if _, duplicate := values[name]; duplicate {
			return nil, false
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, false
		}
		values[name] = value
	}
	if _, err = d.Token(); err != nil {
		return nil, false
	}
	var extra json.RawMessage
	if d.Decode(&extra) != io.EOF {
		return nil, false
	}
	return values, true
}

func snapshotLabel(raw json.RawMessage, allowed ...string) string {
	if len(raw) == 0 {
		return "absent"
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "other"
	}
	for _, name := range allowed {
		if name == value {
			return name
		}
	}
	return "other"
}

func snapshotBool(raw json.RawMessage) *bool {
	var value bool
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

func snapshotOutputLimit(raw json.RawMessage) *int64 {
	var value int64
	if json.Unmarshal(raw, &value) != nil || value <= 0 || value > 10_000_000 {
		return nil
	}
	return &value
}

func (s *RequestSnapshotObserver) inventory(body []byte, v *DebugRequestSnapshot) {
	v.BodyBytes = len(body)
	v.WireFingerprint = s.fingerprint(append([]byte("wire:"), body...))
	root, ok := snapshotObject(body)
	if !ok {
		v.Inspection = "invalid_or_ambiguous_json"
		return
	}
	inputKey := "messages"
	if v.Protocol == modelcap.ProtocolOpenAIResponses {
		inputKey = "input"
	}
	v.InputBytes = len(root[inputKey])
	v.ToolBytes = len(root["tools"])
	v.InstructionBytes = len(root["instructions"]) + len(root["system"])
	v.Inputs, v.InputCount = s.items(root[inputKey], false)
	v.Tools, v.ToolCount = s.items(root["tools"], true)
	v.OmittedInputs = v.InputCount - len(v.Inputs)
	v.OmittedTools = v.ToolCount - len(v.Tools)
	v.Stream = snapshotBool(root["stream"])
	v.ToolChoice = snapshotLabel(root["tool_choice"], "auto", "none", "required")
	if choice, ok := snapshotObject(root["tool_choice"]); ok {
		v.ToolChoice = snapshotLabel(choice["type"], "auto", "none", "any", "function", "tool")
		switch v.ToolChoice {
		case "any":
			v.ToolChoice = "required"
		case "function", "tool":
			v.ToolChoice = "named"
		}
	}
	v.Reasoning = "absent"
	if reasoning, ok := snapshotObject(root["reasoning"]); ok {
		v.Reasoning = snapshotLabel(reasoning["effort"], "none", "minimal", "low", "medium", "high", "xhigh", "max")
	} else if len(root["reasoning_effort"]) > 0 {
		v.Reasoning = snapshotLabel(root["reasoning_effort"], "none", "minimal", "low", "medium", "high", "xhigh", "max")
	} else if thinking, ok := snapshotObject(root["thinking"]); ok {
		v.Reasoning = snapshotLabel(thinking["type"], "enabled", "disabled", "adaptive")
	}
	for _, key := range []string{"max_output_tokens", "max_completion_tokens", "max_tokens"} {
		if limit := snapshotOutputLimit(root[key]); limit != nil {
			v.MaxOutputTokens = limit
			break
		}
	}
	// Includes instructions and all remaining settings, but publishes no keys,
	// model names, cache keys, custom identifiers or plaintext values.
	delete(root, inputKey)
	delete(root, "tools")
	settings, _ := json.Marshal(root)
	v.SettingsFingerprint = s.fingerprint(append([]byte("settings:"), settings...))
	v.Inspection = "complete"
}

func (s *RequestSnapshotObserver) items(raw json.RawMessage, tool bool) ([]DebugRequestItem, int) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, 0
	}
	items := []DebugRequestItem{}
	add := func(value json.RawMessage, index int) {
		if index < maxSnapshotItems {
			items = append(items, s.item(value, index, tool))
		}
	}
	if trimmed[0] != '[' {
		add(raw, 0)
		return items, 1
	}
	d := json.NewDecoder(bytes.NewReader(trimmed))
	_, _ = d.Token()
	count := 0
	for d.More() {
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return items, count
		}
		add(value, count)
		count++
	}
	return items, count
}

func (s *RequestSnapshotObserver) item(raw json.RawMessage, index int, tool bool) DebugRequestItem {
	var compact bytes.Buffer
	_ = json.Compact(&compact, raw)
	domain := "input:"
	if tool {
		domain = "tool:"
	}
	v := DebugRequestItem{Index: index, Kind: "other", Role: "absent", JSONBytes: len(raw), Fingerprint: s.fingerprint(append([]byte(domain), compact.Bytes()...))}
	if object, ok := snapshotObject(raw); ok {
		v.Kind = snapshotLabel(object["type"], "message", "function_call", "function_call_output", "reasoning", "tool_search_call", "tool_search_output", "function", "custom", "namespace", "tool_use", "tool_result", "text", "thinking", "redacted_thinking")
		v.Role = snapshotLabel(object["role"], "system", "developer", "user", "assistant", "tool", "function")
		if v.Kind == "absent" && v.Role != "absent" && v.Role != "other" {
			v.Kind = "message"
		}
		if tool {
			v.SchemaBytes = len(object["parameters"]) + len(object["input_schema"])
			if function, ok := snapshotObject(object["function"]); ok {
				v.SchemaBytes += len(function["parameters"])
			}
		}
	} else if len(raw) > 0 && raw[0] == '"' {
		v.Kind = "string"
	}
	return v
}

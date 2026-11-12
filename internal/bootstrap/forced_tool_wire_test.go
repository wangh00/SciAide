package bootstrap

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
)

// Check the actual sole callable target and nonparallel choice, not a name
// mentioned in an arbitrary string. Required alone is not a forced target.
func singleStageToolForcedOnWire(protocol modelprofile.APIProtocol, body []byte) bool {
	const target = "submit_stage_result"
	var request struct {
		Tools []struct {
			Type, Name  string
			Parameters  json.RawMessage
			InputSchema json.RawMessage `json:"input_schema"`
			Function    struct {
				Name       string
				Parameters json.RawMessage
			}
		}
		ToolChoice json.RawMessage `json:"tool_choice"`
		Parallel   *bool           `json:"parallel_tool_calls"`
	}
	if json.Unmarshal(body, &request) != nil || len(request.Tools) != 1 {
		return false
	}
	tool := request.Tools[0]
	if protocol == modelprofile.ProtocolOpenAIResponses {
		var choice string
		return json.Unmarshal(request.ToolChoice, &choice) == nil && choice == "required" && tool.Type == "function" && tool.Name == target && request.Parallel != nil && !*request.Parallel
	}
	var choice struct {
		Type, Name      string
		Function        struct{ Name string }
		DisableParallel bool `json:"disable_parallel_tool_use"`
	}
	if json.Unmarshal(request.ToolChoice, &choice) != nil {
		return false
	}
	switch protocol {
	case modelprofile.ProtocolOpenAIChat:
		return tool.Type == "function" && tool.Function.Name == target && choice.Type == "function" && choice.Function.Name == target && request.Parallel != nil && !*request.Parallel
	case modelprofile.ProtocolAnthropic:
		return tool.Name == target && choice.Type == "tool" && choice.Name == target && choice.DisableParallel
	default:
		return false
	}
}

// This is a separate negative check: the negation of the strict forced
// contract above does NOT imply optional selection (e.g. named + two tools).
func stageToolSelectionOptionalOnWire(protocol modelprofile.APIProtocol, body []byte) bool {
	switch protocol {
	case modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic:
	default:
		return false
	}
	var request map[string]json.RawMessage
	if json.Unmarshal(body, &request) != nil || request == nil {
		return false
	}
	choice, present := request["tool_choice"]
	if !present {
		return true
	}
	var option string
	if protocol != modelprofile.ProtocolAnthropic && json.Unmarshal(choice, &option) == nil {
		return option == "auto"
	}
	if protocol != modelprofile.ProtocolAnthropic {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(choice, &fields) != nil || fields == nil {
		return false
	}
	if json.Unmarshal(fields["type"], &option) != nil || option != "auto" {
		return false
	}
	for key, value := range fields {
		if key == "type" {
			continue
		}
		if key != "disable_parallel_tool_use" {
			return false
		}
		var disabled *bool
		if json.Unmarshal(value, &disabled) != nil || disabled == nil {
			return false
		}
	}
	return true
}

func stageToolOfferedOnWire(protocol modelprofile.APIProtocol, body []byte) bool {
	var request struct {
		Tools []struct {
			Type, Name string
			Function   struct{ Name string }
		}
	}
	if json.Unmarshal(body, &request) != nil {
		return false
	}
	for _, tool := range request.Tools {
		switch protocol {
		case modelprofile.ProtocolOpenAIResponses:
			if tool.Type == "function" && tool.Name == "submit_stage_result" {
				return true
			}
		case modelprofile.ProtocolOpenAIChat:
			if tool.Type == "function" && tool.Function.Name == "submit_stage_result" {
				return true
			}
		case modelprofile.ProtocolAnthropic:
			if tool.Name == "submit_stage_result" {
				return true
			}
		}
	}
	return false
}

// Required planner action is deliberately NOT a forced submission target:
// both the actual Skill IO and the typed completion control must remain.
func plannerActionRequiredOnWire(protocol modelprofile.APIProtocol, body []byte) bool {
	var r struct {
		Tools []struct {
			Type, Name  string
			Parameters  json.RawMessage
			InputSchema json.RawMessage `json:"input_schema"`
			Function    struct {
				Name       string
				Parameters json.RawMessage
			}
		}
		Choice   json.RawMessage `json:"tool_choice"`
		Parallel *bool           `json:"parallel_tool_calls"`
	}
	if json.Unmarshal(body, &r) != nil || len(r.Tools) < 2 || !wireObjectSchema(plannerSubmissionSchemaOnWire(protocol, body)) {
		return false
	}
	ioOffered := false
	for _, d := range r.Tools {
		if protocol != modelprofile.ProtocolAnthropic && d.Type != "function" {
			continue
		}
		if protocol == modelprofile.ProtocolAnthropic && d.Type != "" {
			continue
		}
		name := d.Name
		schema := d.Parameters
		if protocol == modelprofile.ProtocolOpenAIChat {
			name = d.Function.Name
			schema = d.Function.Parameters
		} else if protocol == modelprofile.ProtocolAnthropic {
			schema = d.InputSchema
		}
		ioOffered = ioOffered || wireObjectSchema(schema) && (name == "builtin_resource_open" || name == "builtin.resource.open" || name == "resource_open" || name == "builtin_skill_load" || name == "builtin.skill.load")
	}
	if !ioOffered {
		return false
	}
	if protocol == modelprofile.ProtocolAnthropic {
		var choice struct {
			Type            string
			Name            string
			DisableParallel *bool `json:"disable_parallel_tool_use"`
		}
		return json.Unmarshal(r.Choice, &choice) == nil && choice.Type == "any" && choice.Name == "" && choice.DisableParallel != nil && *choice.DisableParallel
	}
	if protocol != modelprofile.ProtocolOpenAIChat && protocol != modelprofile.ProtocolOpenAIResponses {
		return false
	}
	var choice string
	return json.Unmarshal(r.Choice, &choice) == nil && choice == "required" && r.Parallel != nil && !*r.Parallel
}

func wireObjectSchema(schema json.RawMessage) bool {
	var value struct{ Type string }
	return json.Unmarshal(schema, &value) == nil && value.Type == "object"
}

func plannerSubmissionSchemaOnWire(protocol modelprofile.APIProtocol, body []byte) json.RawMessage {
	var r struct {
		Tools []struct {
			Type, Name  string
			Parameters  json.RawMessage
			InputSchema json.RawMessage `json:"input_schema"`
			Function    struct {
				Name       string
				Parameters json.RawMessage
			}
		}
	}
	if json.Unmarshal(body, &r) != nil {
		return nil
	}
	for _, d := range r.Tools {
		switch protocol {
		case modelprofile.ProtocolOpenAIResponses:
			if d.Type == "function" && d.Name == "submit_stage_result" {
				return d.Parameters
			}
		case modelprofile.ProtocolOpenAIChat:
			if d.Type == "function" && d.Function.Name == "submit_stage_result" {
				return d.Function.Parameters
			}
		case modelprofile.ProtocolAnthropic:
			if d.Type == "" && d.Name == "submit_stage_result" {
				return d.InputSchema
			}
		}
	}
	return nil
}

func TestPlannerWireRequiresActualCallableIOAndNonparallelChoice(t *testing.T) {
	for _, tc := range []struct {
		protocol  modelprofile.APIProtocol
		good, bad string
	}{
		{modelprofile.ProtocolOpenAIResponses, `{"tools":[{"type":"function","name":"submit_stage_result","parameters":{"type":"object"}},{"type":"function","name":"resource_open","parameters":{"type":"object"}}],"tool_choice":"required","parallel_tool_calls":false}`, `{"tools":[{"type":"function","name":"submit_stage_result","parameters":{"type":"object"}},{"type":"file_search","name":"resource_open","parameters":{"type":"object"}}],"tool_choice":"required","parallel_tool_calls":false}`},
		{modelprofile.ProtocolOpenAIChat, `{"tools":[{"type":"function","function":{"name":"submit_stage_result","parameters":{"type":"object"}}},{"type":"function","function":{"name":"resource_open","parameters":{"type":"object"}}}],"tool_choice":"required","parallel_tool_calls":false}`, `{"tools":[{"type":"function","function":{"name":"submit_stage_result","parameters":{"type":"object"}}},{"type":"file_search","function":{"name":"resource_open","parameters":{"type":"object"}}}],"tool_choice":"required","parallel_tool_calls":false}`},
		{modelprofile.ProtocolAnthropic, `{"tools":[{"name":"submit_stage_result","input_schema":{"type":"object"}},{"name":"resource_open","input_schema":{"type":"object"}}],"tool_choice":{"type":"any","disable_parallel_tool_use":true}}`, `{"tools":[{"name":"submit_stage_result","input_schema":{"type":"object"}},{"type":"web_search_20250305","name":"resource_open","input_schema":{"type":"object"}}],"tool_choice":{"type":"any","disable_parallel_tool_use":true}}`},
	} {
		if !plannerActionRequiredOnWire(tc.protocol, []byte(tc.good)) || plannerActionRequiredOnWire(tc.protocol, []byte(tc.bad)) {
			t.Fatal("planner wire fixture accepted a non-callable IO disguise", tc.protocol)
		}
		for _, schemaKey := range []string{"parameters", "input_schema"} {
			missing := strings.Replace(tc.good, `,"`+schemaKey+`":{"type":"object"}`, "", 1)
			if missing != tc.good && plannerActionRequiredOnWire(tc.protocol, []byte(missing)) {
				t.Fatal("missing native submission schema was accepted", tc.protocol)
			}
		}
		var body map[string]any
		_ = json.Unmarshal([]byte(tc.good), &body)
		body["tool_choice"] = "auto"
		data, _ := json.Marshal(body)
		if plannerActionRequiredOnWire(tc.protocol, data) {
			t.Fatal("optional selection mistaken for typed planner contract", tc.protocol)
		}
	}
}

func TestStageToolOptionalWireCheckIsIndependentOfStrictForcedContract(t *testing.T) {
	for _, tc := range []struct {
		protocol modelprofile.APIProtocol
		body     string
		optional bool
	}{
		{modelprofile.ProtocolOpenAIResponses, `{"tool_choice":"required","parallel_tool_calls":true}`, false},
		{modelprofile.ProtocolOpenAIResponses, `{"tool_choice":"required"}`, false},
		{modelprofile.ProtocolOpenAIResponses, `{"tool_choice":{"type":"function","name":"submit_stage_result"},"tools":[{},{}]}`, false},
		{modelprofile.ProtocolOpenAIChat, `{"tool_choice":{"type":"function","function":{"name":"submit_stage_result"}},"tools":[{},{}]}`, false},
		{modelprofile.ProtocolAnthropic, `{"tool_choice":{"type":"tool","name":"submit_stage_result"},"tools":[{},{}]}`, false},
		{modelprofile.ProtocolOpenAIChat, `{"tool_choice":{"type":"function","function":{"name":"submit_stage_result"}}}`, false},
		{modelprofile.ProtocolOpenAIResponses, `{"tool_choice":"auto"}`, true},
		{modelprofile.ProtocolOpenAIChat, `{}`, true},
		{modelprofile.ProtocolAnthropic, `{"tool_choice":{"type":"auto","disable_parallel_tool_use":true}}`, true},
		{modelprofile.ProtocolAnthropic, `{"tool_choice":{"type":"auto","name":"submit_stage_result"}}`, false},
		{modelprofile.ProtocolAnthropic, `{"tool_choice":"auto"}`, false},
		{modelprofile.ProtocolAnthropic, `{"tool_choice":{"type":"auto","disable_parallel_tool_use":null}}`, false},
		{modelprofile.APIProtocol("unknown"), `{"tool_choice":"auto"}`, false},
		{modelprofile.ProtocolOpenAIResponses, `{"tool_choice":null}`, false},
		{modelprofile.ProtocolOpenAIResponses, `{"tool_choice":"unknown"}`, false},
		{modelprofile.ProtocolOpenAIResponses, `null`, false},
	} {
		if got := stageToolSelectionOptionalOnWire(tc.protocol, []byte(tc.body)); got != tc.optional {
			t.Fatalf("optional selection misclassified for %s: got=%v want=%v", tc.protocol, got, tc.optional)
		}
	}
	if stageToolOfferedOnWire(modelprofile.ProtocolOpenAIResponses, []byte(`{"input":"submit_stage_result","tools":[{"type":"function","name":"other","description":"submit_stage_result"}]}`)) {
		t.Fatal("mentioned target mistaken for actual tool definition")
	}
}

func TestSingleStageToolWireRecognitionDoesNotWeakenRequired(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"tools":[{"type":"function","name":"submit_stage_result"}],"tool_choice":"required","parallel_tool_calls":false}`, true},
		{`{"tools":[{"type":"function","name":"submit_stage_result"}]}`, false},
		{`{"tools":[{"type":"function","name":"submit_stage_result"}],"tool_choice":"none","parallel_tool_calls":false}`, false},
		{`{"tools":[{"type":"function","name":"submit_stage_result"}],"tool_choice":"required","parallel_tool_calls":true}`, false},
		{`{"tools":[{"type":"function","name":"submit_stage_result"}],"tool_choice":"required"}`, false},
		{`{"tools":[{"type":"function","name":"other","description":"submit_stage_result"}],"tool_choice":"required","parallel_tool_calls":false}`, false},
		{`{"tools":[{"type":"function","name":"submit_stage_result"},{"type":"function","name":"other"}],"tool_choice":"required","parallel_tool_calls":false}`, false},
		{`{"tools":[],"tool_choice":"required","parallel_tool_calls":false}`, false},
	} {
		if got := singleStageToolForcedOnWire(modelprofile.ProtocolOpenAIResponses, []byte(tc.body)); got != tc.want {
			t.Fatalf("wire recognition changed the forced target: got=%v want=%v", got, tc.want)
		}
	}
}

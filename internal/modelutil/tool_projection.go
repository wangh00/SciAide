package modelutil

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/wangh00/SciAide/internal/model"
)

// ProjectToolReferences creates a wire-only view using the same alias table as
// the function definitions. It never changes stored prompts, argument values,
// output schemas, scientific content, or signed/native provider items.
func ProjectToolReferences(request model.ChatRequest, aliases map[string]string) model.ChatRequest {
	references := make(map[string]string, len(aliases)+2)
	reverse := map[string]string{}
	for name, alias := range aliases {
		references[name] = alias
		reverse[alias] = name
	}

	project := toolNameProjection{aliases: references}
	names := []string{}
	for canonical, alias := range references {
		if canonical != alias {
			names = append(names, regexp.QuoteMeta(canonical))
		}
	}
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	if len(names) > 0 {
		project.pattern = regexp.MustCompile(strings.Join(names, "|"))
	}
	request.Tools = append([]model.ToolDefinition(nil), request.Tools...)
	for i := range request.Tools {
		request.Tools[i].Description = project.text(request.Tools[i].Description)
	}
	request.Messages = append([]model.Message(nil), request.Messages...)
	for i, m := range request.Messages {
		m.ToolCalls = append([]model.ToolCall(nil), m.ToolCalls...)
		for j := range m.ToolCalls {
			m.ToolCalls[j].Name = ResolveProviderToolName(m.ToolCalls[j].Name, reverse)
		}
		if m.Role == model.RoleSystem || m.HostToolReferences {
			m.Content = project.hostText(m.Content)
		}
		if m.Role == model.RoleTool {
			m.Content = project.result(m.Content)
		}
		request.Messages[i] = m
	}
	request.ProviderTurns = append([]model.ProviderTurn(nil), request.ProviderTurns...)
	for i, turn := range request.ProviderTurns {
		turn.ToolResults = append([]model.Message(nil), turn.ToolResults...)
		for j := range turn.ToolResults {
			turn.ToolResults[j].Content = project.result(turn.ToolResults[j].Content)
		}
		request.ProviderTurns[i] = turn
	}
	return request
}

type toolNameProjection struct {
	aliases map[string]string
	pattern *regexp.Regexp
}

func toolIdentifierByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-'
}
func (p toolNameProjection) text(text string) string {
	// Expand the host's old shorthand, rather than presenting a half-renamed name.
	if open := p.aliases["builtin.resource.open"]; open != "" {
		expanded := open
		if search := p.aliases["builtin.resource.search"]; search != "" {
			expanded += " / " + search
		}
		text = replaceToolSpelling(text, "builtin.resource.open/search", expanded)
	}
	if p.pattern == nil {
		return text
	}
	matches := p.pattern.FindAllStringIndex(text, -1)
	var out strings.Builder
	start := 0
	for _, match := range matches {
		a, b := match[0], match[1]
		if a > 0 && toolIdentifierByte(text[a-1]) || toolNameSuffix(text, b) {
			continue
		}
		out.WriteString(text[start:a])
		out.WriteString(p.aliases[text[a:b]])
		start = b
	}
	out.WriteString(text[start:])
	return out.String()
}

// These blocks are values/contracts, not executable tool instructions. Keep
// them byte-for-byte; rewriting a const/enum, path or quoted paper is wrong.
var toolProjectionBlocks = []string{"output_schema", "metadata_output_schema", "stage_input", "workflow_state", "trusted_workflow_citations", "resource_actions"}

func (p toolNameProjection) hostText(text string) string {
	var out strings.Builder
	for len(text) > 0 {
		at := -1
		tag := ""
		for _, name := range toolProjectionBlocks {
			if n := strings.Index(text, "<"+name+">"); n >= 0 && (at < 0 || n < at) {
				at = n
				tag = name
			}
		}
		if at < 0 {
			out.WriteString(p.text(text))
			break
		}
		opening := "<" + tag + ">"
		closing := "</" + tag + ">"
		end := strings.Index(text[at+len(opening):], closing)
		if end < 0 {
			out.WriteString(p.text(text[:at]))
			out.WriteString(text[at:])
			break
		}
		end += at + len(opening)
		// Instruction prose can mention <output_schema> before the actual block.
		// Do not let that placeholder swallow intervening host instructions/data.
		if body := text[at+len(opening) : end]; strings.Contains(body, opening) && !json.Valid([]byte(strings.TrimSpace(body))) {
			out.WriteString(p.text(text[:at+len(opening)]))
			text = text[at+len(opening):]
			continue
		}
		out.WriteString(p.text(text[:at]))
		out.WriteString(opening)
		body := text[at+len(opening) : end]
		if tag == "resource_actions" {
			body = p.menu(body)
		}
		out.WriteString(body)
		out.WriteString(closing)
		text = text[end+len(closing):]
	}
	return out.String()
}

func (p toolNameProjection) actions(raw json.RawMessage) (json.RawMessage, bool) {
	var actions []map[string]json.RawMessage
	if json.Unmarshal(raw, &actions) != nil {
		return raw, false
	}
	changed := false
	for _, a := range actions {
		var name string
		if json.Unmarshal(a["tool"], &name) == nil {
			if alias := p.aliases[name]; alias != "" && alias != name {
				a["tool"], _ = json.Marshal(alias)
				changed = true
			}
		}
	}
	if !changed {
		return raw, false
	}
	value, err := json.Marshal(actions)
	if err != nil {
		return raw, false
	}
	return value, true
}
func (p toolNameProjection) menu(text string) string {
	var value map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &value) != nil {
		return text
	}
	if actions, changed := p.actions(value["actions"]); changed {
		value["actions"] = actions
		raw, err := json.Marshal(value)
		if err == nil {
			return preserveJSONPadding(text, string(raw))
		}
	}
	return text
}
func (p toolNameProjection) result(text string) string {
	var value map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &value) != nil {
		return p.truncatedResourceResult(text)
	}
	var structured map[string]json.RawMessage
	changed := false
	if json.Unmarshal(value["structured"], &structured) == nil {
		var id string
		if json.Unmarshal(structured["actionId"], &id) == nil && strings.HasPrefix(id, "res_") {
			if actions, ok := p.actions(structured["actions"]); ok {
				structured["actions"] = actions
				value["structured"], _ = json.Marshal(structured)
				changed = true
			}
		}
	}
	var status, message string
	if json.Unmarshal(value["status"], &status) == nil && status == "error" && json.Unmarshal(value["text"], &message) == nil {
		projected := p.text(message)
		if projected != message {
			value["text"], _ = json.Marshal(projected)
			changed = true
		}
	}
	if !changed {
		return text
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return text
	}
	return string(raw)
}

func preserveJSONPadding(original, replacement string) string {
	left := len(original) - len(strings.TrimLeft(original, " \t\r\n"))
	right := len(strings.TrimRight(original, " \t\r\n"))
	return original[:left] + replacement + original[right:]
}

func toolNameSuffix(text string, index int) bool {
	if index >= len(text) {
		return false
	}
	if text[index] == '.' {
		return index+1 < len(text) && toolIdentifierByte(text[index+1]) && text[index+1] != '.'
	}
	return toolIdentifierByte(text[index])
}
func replaceToolSpelling(text, old, replacement string) string {
	var out strings.Builder
	start := 0
	scan := 0
	for scan < len(text) {
		pos := strings.Index(text[scan:], old)
		if pos < 0 {
			break
		}
		a := scan + pos
		b := a + len(old)
		scan = b
		if a > 0 && toolIdentifierByte(text[a-1]) || toolNameSuffix(text, b) {
			continue
		}
		out.WriteString(text[start:a])
		out.WriteString(replacement)
		start = b
	}
	out.WriteString(text[start:])
	return out.String()
}

// Referenced names are display-only. The adapter must keep its original active
// alias table for response resolution, schema validation and forced tool choice.
func ToolReferenceAliases(request model.ChatRequest, active map[string]string, name func(string) string) map[string]string {
	result := make(map[string]string, len(active)+len(request.ToolReferenceNames))
	for k, v := range active {
		result[k] = v
	}
	for _, k := range request.ToolReferenceNames {
		if _, ok := result[k]; !ok {
			result[k] = name(k)
		}
	}
	return result
}

// Context budgeting can retain the head/tail of a resource result, making the
// envelope invalid JSON. Only complete, flat host Option records are projected;
// escaped paper/code strings and partial records are never text-rewritten.
var resourceResultPrefix = regexp.MustCompile(`"structured"\s*:\s*\{\s*"actionId"\s*:\s*"res_[a-f0-9]{32}"`)
var flatResourceRecord = regexp.MustCompile(`\{(?:[^{}"]|"(?:\\.|[^"\\])*")*\}`)

func (p toolNameProjection) truncatedResourceResult(text string) string {
	if !resourceResultPrefix.MatchString(text) {
		return text
	}
	return flatResourceRecord.ReplaceAllStringFunc(text, func(raw string) string {
		var option map[string]string
		if json.Unmarshal([]byte(raw), &option) != nil || len(option) != 4 || option["label"] == "" || option["kind"] == "" {
			return raw
		}
		id := option["actionId"]
		if len(id) != 36 || !strings.HasPrefix(id, "res_") {
			return raw
		}
		for _, c := range id[4:] {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return raw
			}
		}
		alias := p.aliases[option["tool"]]
		if alias == "" || alias == option["tool"] {
			return raw
		}
		option["tool"] = alias
		value, err := json.Marshal(option)
		if err != nil {
			return raw
		}
		return string(value)
	})
}

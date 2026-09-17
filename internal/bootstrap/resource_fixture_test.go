package bootstrap

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/resource"
)

// Fixture providers select issued menu entries as a real model. No test
// reaches into SQLite to discover a hidden resource handle or constructs one.
func resourceFixtureSelection(body []byte, skill string, browseOnly bool) (string, string, bool, error) {
	var request struct {
		Tools []struct {
			Function struct {
				Name, Description string
				Parameters        json.RawMessage
			}
		}
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return "", "", false, err
	}
	function := ""
	var schema json.RawMessage
	for _, candidate := range request.Tools {
		if strings.Contains(candidate.Function.Description, "Operate on a host-issued resource action") {
			function = candidate.Function.Name
			schema = candidate.Function.Parameters
			break
		}
	}
	if function == "" {
		return "", "", false, nil
	}
	prompt := workflowAITestPrompt(body)
	start := strings.Index(prompt, "<resource_actions>\n")
	if start < 0 {
		return "", "", true, fmt.Errorf("resource tool has no host menu")
	}
	start += len("<resource_actions>\n")
	end := strings.Index(prompt[start:], "\n</resource_actions>")
	if end < 0 {
		return "", "", true, fmt.Errorf("resource menu not closed")
	}
	var menu struct{ Actions []resource.Option }
	if err := json.Unmarshal([]byte(prompt[start:start+end]), &menu); err != nil {
		return "", "", true, err
	}
	var params struct {
		Properties struct {
			ActionID struct {
				Pattern string `json:"pattern"`
			} `json:"actionId"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &params); err != nil {
		return "", "", true, err
	}
	if params.Properties.ActionID.Pattern != "^res_[a-f0-9]{32}$" {
		return "", "", true, fmt.Errorf("invalid resource ID schema")
	}
	pick := func(match func(resource.Option) bool) (string, bool) {
		for _, a := range menu.Actions {
			if a.Tool == function && match(a) {
				args, _ := json.Marshal(map[string]string{"actionId": a.ID})
				return string(args), true
			}
		}
		return "", false
	}
	if !browseOnly {
		if args, ok := pick(func(a resource.Option) bool {
			return a.Kind == "skill" && strings.HasPrefix(a.Label, "加载 Skill · "+skill+" —")
		}); ok {
			return function, args, true, nil
		}
	}
	// scientific-writing is deliberately selected by this fixture's plan; walk
	// the visible writing catalog if it is outside the initial ranked shortlist.
	for _, label := range []string{"Skill 分类 · writing", "浏览可用 Skill（仅目录，不算加载）"} {
		if args, ok := pick(func(a resource.Option) bool { return a.Kind == "menu" && a.Label == label }); ok {
			return function, args, true, nil
		}
	}
	return "", "", true, fmt.Errorf("resource menu cannot select requested fixture skill %s", skill)
}
func resourceFixtureSkillLoaded(body []byte, name string) bool {
	var request struct {
		Messages []struct {
			Role    string
			Content json.RawMessage
		}
	}
	if json.Unmarshal(body, &request) != nil {
		return false
	}
	for _, m := range request.Messages {
		if m.Role != "tool" {
			continue
		}
		var content string
		if json.Unmarshal(m.Content, &content) != nil {
			continue
		}
		if strings.Contains(content, `"loaded":true`) && strings.Contains(content, `"name":"`+name+`"`) {
			return true
		}
	}
	return false
}

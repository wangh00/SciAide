package agent

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/wangh00/SciAide/internal/app/tool"
)

// Uses only definitions already in this exact run's authorized projection.
// A successful host receipt restores a matching contract, not new permission.
func deferredBuiltinDefinitions(defs []tool.Definition, calls []tool.Call) ([]tool.Definition, string, int) {
	if !definitionVisible(defs, mcpSearchTool) {
		return defs, "", 0
	}
	selected := map[string]string{}
	for _, c := range calls {
		if c.ToolName != mcpSearchTool || c.Status != tool.CallCompleted || c.Result == nil || c.Result.Status != tool.ResultSuccess {
			continue
		}
		var r struct {
			Kind  string `json:"kind"`
			Tools []struct {
				Name        string `json:"name"`
				Fingerprint string `json:"fingerprint"`
			} `json:"tools"`
		}
		if json.Unmarshal(c.Result.Structured, &r) != nil || r.Kind != "builtin" {
			continue
		}
		for _, d := range r.Tools {
			selected[d.Name] = d.Fingerprint
		}
	}
	out := []tool.Definition{}
	hidden := []string{}
	saving := 0
	for _, d := range defs {
		if tool.OptionalBuiltinDefinition(d.QualifiedName) && selected[d.QualifiedName] != tool.DefinitionFingerprint(d) {
			hidden = append(hidden, d.QualifiedName)
			saving += len([]rune(d.QualifiedName + d.Description + string(d.InputSchema)))
			continue
		}
		out = append(out, d)
	}
	if len(hidden) == 0 {
		return defs, "", 0
	}
	sort.Strings(hidden)
	guide := "Optional builtin definitions are on demand to fit this unchanged context budget. Available names: " + strings.Join(hidden, ", ") + ". Use builtin.tools.search with kind=\"builtin\", query=the exact qualified name, limit=1. Wait for the NEXT turn's definition before invoking; search grants no execution permission."
	if saving <= len([]rune(guide)) {
		return defs, "", 0
	}
	return out, guide, len(hidden)
}

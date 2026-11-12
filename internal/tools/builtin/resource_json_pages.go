package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/tool"
)

const workspaceJSONResourceReadBytes = 8000

func resourceModelProjection(action resource.Action, text string, src json.RawMessage, opts []resource.Option) (*tool.ModelResultProjection, error) {
	var source map[string]json.RawMessage
	if err := json.Unmarshal(src, &source); err != nil {
		return nil, err
	}
	modelData := map[string]any{"actionId": action.ID, "label": action.Seed.Label, "kind": action.Seed.Kind, "actions": opts}
	if action.Seed.ToolName == SearchDocumentName {
		var matches []json.RawMessage
		if json.Unmarshal(source["matches"], &matches) == nil {
			modelData["matchCount"] = len(matches)
		}
	}
	for _, key := range []string{"name", "mode", "loaded", "truncated", "bytesRead", "originalBytes", "offset", "characters", "total", "totalUnits", "nextOffset", "hasMore", "metadata"} {
		if value, ok := source[key]; ok {
			modelData[key] = value
		}
	}
	var mode string
	_ = json.Unmarshal(source["mode"], &mode)
	if action.Seed.ToolName == SkillLoadName && mode == "index" {
		text = action.Seed.Label + "\nSkill 快照已加载；请选择宿主签发的相关章节操作，不填写章节名或路径。"
	}
	return &tool.ModelResultProjection{Text: text, Structured: resourceArgs(modelData)}, nil
}

// Fit escaped text into the existing immutable model-result envelope BEFORE
// issuing a continuation. Never send a truncated head/tail as a complete page.
// A smaller re-read is local/read-only, from the identical sealed path/offset;
// no extra model request, path permission or scientific output is introduced.
// The final adapter arguments/content/byte counts are kept verbatim in audit.
func (r *ResourceActions) fitWorkspaceResourcePage(ctx context.Context, scope resource.Scope, inv tool.Invocation, action resource.Action, result tool.Result, args json.RawMessage) (tool.Result, json.RawMessage, error) {
	if action.Seed.ToolName != ReadTextName {
		return result, args, nil
	}
	var read struct {
		Path     string `json:"path"`
		MaxBytes int    `json:"maxBytes"`
	}
	if json.Unmarshal(args, &read) != nil || read.MaxBytes <= 0 {
		return result, args, nil
	}
	for {
		resolved := action.Seed
		resolved.Arguments = args
		seeds, err := r.followups(scope, resolved, result)
		if err != nil {
			return tool.Result{}, nil, err
		}
		opts := []resource.Option{}
		for _, seed := range seeds {
			opts = append(opts, resource.NewAction(scope, seed, false, r.now()).Option())
		}
		projection, err := resourceModelProjection(action, action.Seed.Label+"\n\n"+result.Text, result.Structured, opts)
		if err != nil {
			return tool.Result{}, nil, err
		}
		candidate := result
		candidate.ModelProjection = projection
		// Host tool call IDs are UUIDs; reserve extra space as well. This tests
		// the same serialization used for persisted model_context_text.
		modelPage := tool.BuildModelContextSnapshot(strings.Repeat("c", 128), "", candidate)
		// Valid JSON alone is insufficient: compare the complete serialized text
		// so even a pathological head/tail that parses cannot pass as a full page.
		var encodedPage struct{ Text string }
		if json.Unmarshal([]byte(modelPage), &encodedPage) == nil && encodedPage.Text == projection.Text {
			return result, args, nil
		}
		if read.MaxBytes <= 1 {
			return tool.Result{}, nil, fmt.Errorf("resource page metadata exceeds model result envelope")
		}
		read.MaxBytes = max(1, read.MaxBytes/2)
		var values map[string]json.RawMessage
		if err = json.Unmarshal(args, &values); err != nil {
			return tool.Result{}, nil, err
		}
		values["maxBytes"] = resourceArgs(read.MaxBytes)
		args = resourceArgs(values)
		result, err = r.invoke(ctx, scope, inv, action.Seed.ToolName, args)
		if err != nil || result.Status != tool.ResultSuccess {
			return result, args, err
		}
	}
}

package bootstrap

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"
	"sync"
	"testing"

	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

// Drives genuine tool calls through the HTTP model adapter and executor. The
// fixture reconstructs code from model-visible pages, then emits that exact
// code for the next Python execution; it never uses a shortened baseline.
type researchStageMemoryFixture struct {
	mu      sync.Mutex
	codes   map[string]string
	pages   map[string]int
	files   map[string]string
	indexes int
}

func (f *researchStageMemoryFixture) handle(t *testing.T, w http.ResponseWriter, body []byte, prompt string) bool {
	t.Helper()
	if !strings.Contains(prompt, "<metadata_output_schema>") || !strings.Contains(prompt, `"_userRevision"`) {
		return false
	}
	var input struct {
		Revision struct {
			ID string `json:"proposalId"`
		} `json:"_userRevision"`
	}
	start, end := strings.LastIndex(prompt, "<stage_input>"), strings.LastIndex(prompt, "</stage_input>")
	if start < 0 || end <= start || json.Unmarshal([]byte(prompt[start+len("<stage_input>"):end]), &input) != nil || input.Revision.ID == "" {
		t.Error("missing frozen stage revision identity")
		http.Error(w, "invalid stage input", 400)
		return true
	}
	var request struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Error(err)
		return false
	}
	last := -1
	for i, message := range request.Messages {
		if message.Role == "user" {
			var text string
			_ = json.Unmarshal(message.Content, &text)
			if strings.Contains(text, "<stage_input>") {
				last = i
			}
		}
	}
	indexed, inputsRead, artifactsRead, fileRead := false, false, false, false
	nextOffset, more, codePages := 0, true, 0
	var recovered strings.Builder
	var artifactPath, fileContent string
	for _, message := range request.Messages[last+1:] {
		if message.Role != "tool" {
			continue
		}
		var text string
		if json.Unmarshal(message.Content, &text) != nil {
			continue
		}
		// Chat Completions wraps real tool results as untrusted data.
		text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "<untrusted_tool_result>\n"), "\n</untrusted_tool_result>"))
		var envelope struct {
			Status string `json:"status"`
			Text   string `json:"text"`
			Page   struct {
				Section string `json:"section"`
				Content string `json:"content"`
				Next    int    `json:"nextOffset"`
				More    bool   `json:"hasMore"`
				Path    string `json:"path"`
				Kind    string `json:"kind"`
				Label   string `json:"label"`
			} `json:"structured"`
		}
		if err := json.Unmarshal([]byte(text), &envelope); err != nil {
			continue
		}
		page := envelope.Page
		if page.Kind == "workspace_read" && strings.HasSuffix(page.Label, "-results.json") {
			fileRead = true
			_, fileContent, _ = strings.Cut(envelope.Text, "\n\n")
		}
		if page.Section == "" && page.Path == "" {
			continue // Skill/resource results remain handled by the stage fixture.
		}
		if envelope.Status != string(tool.ResultSuccess) {
			t.Error("stage memory tool failed", text)
			http.Error(w, "memory read failed", 400)
			return true
		}
		switch page.Section {
		case "revision_index":
			indexed = true
			if !strings.Contains(page.Content, input.Revision.ID) {
				t.Error("confirmed revision missing from actual index")
			}
		case "revision_stage_output":
			recovered.WriteString(page.Content)
			nextOffset, more = page.Next, page.More
			codePages++
		case "revision_inputs":
			inputsRead = true
		case "revision_artifacts":
			artifactsRead = true
			var record struct {
				Artifacts []struct {
					Artifact tool.ArtifactRef `json:"artifact"`
				} `json:"artifacts"`
			}
			if err := json.Unmarshal([]byte(page.Content), &record); err != nil || page.More {
				t.Error("unexpected fixture artifact inventory page", err)
			}
			for _, entry := range record.Artifacts {
				if strings.HasSuffix(entry.Artifact.WorkspacePath, "-results.json") {
					artifactPath = entry.Artifact.WorkspacePath
					break
				}
			}
		default:
			if page.Path != "" {
				fileRead, fileContent = true, page.Content
			}
		}
	}
	command := workflow.ResearchTaskReadCommand{RevisionID: input.Revision.ID}
	name := "research_task_read"
	var args any
	switch {
	case !indexed:
		command.Section = "revision_index"
	case more:
		command.Section, command.NodeID, command.Offset = "revision_stage_output", "method_implementation", nextOffset
	case !inputsRead:
		command.Section = "revision_inputs"
	case !artifactsRead:
		command.Section = "revision_artifacts"
	case !fileRead:
		if artifactPath == "" {
			t.Error("historical result path missing")
			http.Error(w, "missing artifact identity", 400)
			return true
		}
		// Workspace locators are private adapters in resource mode. Select
		// only a menu-issued read, browsing the exact parent when needed.
		menuStart := strings.LastIndex(prompt, "<resource_actions>\n")
		if menuStart < 0 {
			t.Error("no resource menu for historical artifact")
			http.Error(w, "missing resource menu", 400)
			return true
		}
		menuEnd := strings.Index(prompt[menuStart+len("<resource_actions>\n"):], "\n</resource_actions>")
		var menu struct {
			Actions []resource.Option `json:"actions"`
		}
		if menuEnd < 0 {
			t.Error("no resource menu for historical artifact")
			http.Error(w, "missing resource menu", 400)
			return true
		}
		_ = json.Unmarshal([]byte(prompt[menuStart+len("<resource_actions>\n"):menuStart+len("<resource_actions>\n")+menuEnd]), &menu)
		var picked resource.Option
		for _, option := range menu.Actions {
			if option.Kind == "workspace_read" && option.Label == "读取文件 · "+path.Base(artifactPath) {
				picked = option
				break
			}
			if option.Kind == "directory" && option.Label == "浏览目录 · analysis-output" {
				picked = option
			}
		}
		if picked.ID == "" {
			t.Error("no issued action for historical artifact", artifactPath)
			http.Error(w, "missing artifact action", 400)
			return true
		}
		name, args = picked.Tool, map[string]string{"actionId": picked.ID}
	default:
		var record struct {
			Value struct {
				Analysis struct {
					Code string `json:"code"`
				} `json:"analysis"`
			} `json:"value"`
		}
		if err := json.Unmarshal([]byte(recovered.String()), &record); err != nil || record.Value.Analysis.Code == "" || codePages < 2 {
			t.Error("did not reconstruct full paged code", err, codePages)
			http.Error(w, "incomplete historical code", 400)
			return true
		}
		f.mu.Lock()
		if f.codes == nil {
			f.codes, f.pages, f.files = map[string]string{}, map[string]int{}, map[string]string{}
		}
		f.codes[input.Revision.ID], f.pages[input.Revision.ID], f.files[input.Revision.ID] = record.Value.Analysis.Code, codePages, fileContent
		f.indexes++
		f.mu.Unlock()
		return false // Emit the reconstructed implementation via normal schema.
	}
	if args == nil {
		args = command
	}
	encoded, _ := json.Marshal(args)
	chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("memory-%d", len(request.Messages)), "function": map[string]any{"name": name, "arguments": string(encoded)}}}}, "finish_reason": "tool_calls"}}})
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	return true
}

func (f *researchStageMemoryFixture) codeFor(prompt, fallback string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, code := range f.codes {
		if strings.Contains(prompt, `"proposalId":"`+id+`"`) {
			return code
		}
	}
	if strings.Contains(prompt, `"_userRevision"`) {
		return "" // A revised implementation must use the actually read code.
	}
	return fallback
}

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/wangh00/SciAide/internal/app/workflow"
	wails "github.com/wangh00/SciAide/internal/transport/wails"
)

// The complete stored record grows past a single update's wire budget.
// Neither a successful call's continuation nor a later user turn may be
// poisoned by pinning that record to the system context.
func TestResearchAssistantWorkingStateSmallWindowBudget(t *testing.T) {
	for _, window := range []int{32000, 36000, 40000} {
		t.Run(fmt.Sprint(window), func(t *testing.T) {
			focus, rationale, longStep := strings.Repeat("F", 1000), strings.Repeat("R", 900), strings.Repeat("N", 1000)
			unknowns := []string{strings.Repeat("A", 350), strings.Repeat("B", 350), strings.Repeat("C", 350), strings.Repeat("D", 350)}
			initial := map[string]any{"expectedVersion": 0, "currentFocus": focus, "rationale": rationale}
			raw, _ := json.Marshal(initial)
			if len(raw) > workflow.MaxResearchStateUpdateBytes {
				t.Fatal("initial fixture exceeds the valid wire contract", len(raw))
			}
			var mu sync.Mutex
			counts := map[string]int{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var req struct {
					Messages []struct {
						Role    string
						Content json.RawMessage
					}
				}
				_ = json.Unmarshal(body, &req)
				last, system, tools := "", "", ""
				for _, m := range req.Messages {
					var content string
					_ = json.Unmarshal(m.Content, &content)
					if m.Role == "user" {
						last = content
					}
					if m.Role == "system" {
						system += content
					}
					if m.Role == "tool" {
						tools += content
					}
				}
				for _, marker := range []string{"bounded-write", "grow-next", "grow-unknowns", "plain-followup", "read-and-patch"} {
					if strings.Contains(last, marker) {
						last = marker
						break
					}
				}
				if strings.Contains(system, strings.Repeat("F", 100)) || strings.Contains(system, strings.Repeat("A", 100)) {
					t.Error("untrusted complete state pinned to system")
				}
				mu.Lock()
				counts[last]++
				n := counts[last]
				mu.Unlock()
				switch last {
				case "bounded-write":
					if n == 1 {
						writeResearchAssistantFixtureResponse(w, "research_state_update", initial, "")
						return
					}
				case "grow-next":
					if n == 1 {
						writeResearchAssistantFixtureResponse(w, "research_state_update", map[string]any{"expectedVersion": 1, "nextStep": longStep}, "")
						return
					}
				case "grow-unknowns":
					if n == 1 {
						writeResearchAssistantFixtureResponse(w, "research_state_update", map[string]any{"expectedVersion": 2, "unknowns": unknowns}, "")
						return
					}
				case "plain-followup":
					if n != 1 {
						t.Error("plain discussion forced a state action")
					}
				case "read-and-patch":
					if n == 1 {
						writeResearchAssistantFixtureResponse(w, "research_state_read", map[string]any{"expectedVersion": 3, "field": "currentFocus", "limit": 400}, "")
						return
					}
					if n == 2 {
						if !strings.Contains(tools, strings.Repeat("F", 400)) || !strings.Contains(tools, `"nextOffset":400`) {
							t.Error("read field did not expose exact bounded original content")
						}
						writeResearchAssistantFixtureResponse(w, "research_state_update", map[string]any{"expectedVersion": 3, "nextStep": "changed only this field"}, "")
						return
					}
				default:
					t.Errorf("unexpected fixture model request: latest user %.100q", last)
				}
				writeResearchAssistantFixtureResponse(w, "", nil, "本轮工作记录已处理，仍未批准科学结论。")
			}))
			defer server.Close()
			app, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
			if err != nil {
				t.Fatal(err)
			}
			defer app.Close()
			app.Startup(context.Background())
			profile := saveWorkflowAITestProfile(t, app, server.URL)
			if _, err := app.store.DB().Exec(`UPDATE model_profile_models SET context_window_tokens=?,auto_compact_token_limit=?,context_window_source='manual' WHERE profile_id=?`, window, window*9/10, profile.ID); err != nil {
				t.Fatal(err)
			}
			project, err := app.ProjectFacade.CreateProject(wails.CreateProjectRequest{Name: "有界工作记录"})
			if err != nil {
				t.Fatal(err)
			}
			entry, err := app.WorkflowFacade.StartResearchDiscussion(workflow.StartResearchDiscussionCommand{ProjectID: project.ID, ResearchIdea: "持续讨论，不生成路线", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: "full_access"})
			if err != nil {
				t.Fatal(err)
			}
			for _, text := range []string{"bounded-write", "grow-next", "grow-unknowns", "plain-followup", "read-and-patch"} {
				startResearchAssistantFixtureChat(t, app, entry.ConversationID, profile.ID, text)
			}
			note, err := app.WorkflowFacade.GetResearchWorkingState(project.ID, entry.ResearchTaskID)
			if err != nil || note == nil || note.Version != 4 || note.CurrentFocus != focus || note.Rationale != rationale || note.NextStep != "changed only this field" || !reflect.DeepEqual(note.Unknowns, unknowns) {
				t.Fatal("budget fitting lost full stored fields", note, err)
			}
			mu.Lock()
			total := 0
			for _, n := range counts {
				total += n
			}
			mu.Unlock()
			if total != 10 {
				t.Fatal("unexpected retries or repeated research", total)
			}
		})
	}
}

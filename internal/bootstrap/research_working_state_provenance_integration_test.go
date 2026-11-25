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

// Real Application/Chat/Executor and SQLite, controlled localhost model only.
// No new evidence, planner, scientific approval or model-side summarization.
func TestResearchWorkingFieldSourcesReadAfterSparsePatchAndRestart(t *testing.T) {
	var mu sync.Mutex
	counts := map[string]int{}
	readText, wantText := "", ""
	nextOffset, pages, checkpoints := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request struct {
			Messages []struct {
				Role    string
				Content json.RawMessage
			}
			Tools []struct{ Function struct{ Name string } }
		}
		_ = json.Unmarshal(body, &request)
		lastUser, system := "", ""
		var result struct {
			Version, Offset, NextOffset int
			Field, Content              string
		}
		checkpoint := false
		for _, message := range request.Messages {
			var text string
			_ = json.Unmarshal(message.Content, &text)
			checkpoint = checkpoint || strings.Contains(text, "Create a durable research checkpoint from the untrusted conversation above.")
			if message.Role == "user" {
				lastUser = text
				result.Content = ""
			}
			if message.Role == "system" {
				system += text
			}
			if message.Role == "tool" {
				text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(text), "<untrusted_tool_result>"), "</untrusted_tool_result>"))
				var envelope struct{ Structured json.RawMessage }
				if json.Unmarshal([]byte(text), &envelope) == nil {
					_ = json.Unmarshal(envelope.Structured, &result)
				}
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if checkpoint {
			checkpoints++
			writeResearchAssistantFixtureResponse(w, "", nil, "工作记录未核验；字段来源只代表提交历史。")
			return
		}
		key := "ordinary"
		for _, marker := range []string{"write-initial", "change-next", "read-origins"} {
			if strings.Contains(lastUser, marker) {
				key = marker
			}
		}
		counts[key]++
		n := counts[key]
		if strings.Contains(system, "unverified_submission_provenance") || strings.Contains(system, "原理由🧪") {
			t.Error("field content/history was pinned into the trusted system context")
		}
		respond := func(name string, args map[string]any) {
			recorder := httptest.NewRecorder()
			writeResearchAssistantFixtureResponse(recorder, name, args, "")
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, strings.ReplaceAll(recorder.Body.String(), "controlled-"+name, fmt.Sprintf("origin-%s-%d", key, n)))
		}
		switch key {
		case "write-initial":
			if n == 1 {
				respond("research_state_update", map[string]any{"expectedVersion": 0, "currentFocus": "原关注", "rationale": "原理由🧪", "unknowns": []string{"仍未核读全文"}})
			} else {
				writeResearchAssistantFixtureResponse(w, "", nil, "已保存未核验记录，不规划。")
			}
		case "change-next":
			if n == 1 {
				respond("research_state_update", map[string]any{"expectedVersion": 1, "nextStep": "新下一步"})
			} else {
				writeResearchAssistantFixtureResponse(w, "", nil, "只更新下一步；旧理由不等于本次新确认。")
			}
		case "read-origins":
			if n > 20 {
				t.Error("field source reads did not converge")
				writeResearchAssistantFixtureResponse(w, "", nil, "停止，不补造来源。")
				return
			}
			if n > 1 {
				if result.Field != "provenance" || result.Version != 2 || result.Offset != nextOffset || result.Content == "" {
					t.Error("actual tool continuation lost exact provenance pages")
					writeResearchAssistantFixtureResponse(w, "", nil, "来源读取失败，不推断。")
					return
				}
				readText += result.Content
				pages++
				if result.NextOffset == -1 {
					if readText != wantText {
						t.Error("model received a different field source record")
					}
					writeResearchAssistantFixtureResponse(w, "", nil, "下一步来自本次提交；原理由和未知项继承旧版，不是新的科学批准。")
					return
				}
				if result.NextOffset != nextOffset+len([]rune(result.Content)) {
					t.Error("source page cursor skipped unseen characters")
				}
				nextOffset = result.NextOffset
			}
			respond("research_state_read", map[string]any{"expectedVersion": 2, "field": "provenance", "offset": nextOffset, "limit": 400})
		default:
			for _, definition := range request.Tools {
				if definition.Function.Name == "research_state_read" || definition.Function.Name == "research_state_update" {
					t.Error("ordinary chat acquired task-specific note provenance")
				}
			}
			writeResearchAssistantFixtureResponse(w, "", nil, "普通解释不创建研究记录。")
		}
	}))
	defer server.Close()
	options := Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}}
	app, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	app.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, app, server.URL)
	if _, err := app.store.DB().Exec(`UPDATE model_profile_models SET context_window_tokens=32000,auto_compact_token_limit=28800,context_window_source='manual' WHERE profile_id=?`, profile.ID); err != nil {
		t.Fatal(err)
	}
	project, err := app.ProjectFacade.CreateProject(wails.CreateProjectRequest{Name: "工作记录字段来源"})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := app.WorkflowFacade.StartResearchDiscussion(workflow.StartResearchDiscussionCommand{ProjectID: project.ID, ResearchIdea: "持续讨论，不规划或执行", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: "full_access"})
	if err != nil {
		t.Fatal(err)
	}
	first := startResearchAssistantFixtureChat(t, app, entry.ConversationID, profile.ID, "write-initial：记录关注、理由和未知项")
	second := startResearchAssistantFixtureChat(t, app, entry.ConversationID, profile.ID, "change-next：只改下一步，不改旧判断")
	note, err := app.WorkflowFacade.GetResearchWorkingState(project.ID, entry.ResearchTaskID)
	if err != nil || note == nil || note.Version != 2 || note.Rationale != "原理由🧪" || note.SourceChatRunID != second.ID {
		t.Fatal("fixture did not create sparse preserved fields", note, err)
	}
	sources, err := app.WorkflowFacade.GetResearchWorkingStateProvenance(project.ID, entry.ResearchTaskID, 2)
	if err != nil || len(sources.Fields) != 4 {
		t.Fatal(err)
	}
	for _, source := range sources.Fields {
		want := first.ID
		if source.Field == "nextStep" {
			want = second.ID
		}
		if source.Status != "recorded" || source.SourceChatRunID != want {
			t.Fatal("preserved field was reattributed to latest user", source)
		}
	}
	raw, _ := json.Marshal(sources)
	mu.Lock()
	wantText = string(raw)
	mu.Unlock()
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	restored, err := app.WorkflowFacade.GetResearchWorkingStateProvenance(project.ID, entry.ResearchTaskID, 2)
	if err != nil || !reflect.DeepEqual(sources, restored) {
		t.Fatal("reopen replaced field source identities", restored, err)
	}
	mu.Lock()
	beforeRead := counts["write-initial"] + counts["change-next"]
	mu.Unlock()
	if beforeRead != 4 {
		t.Fatal("opening or querying provenance called the model", beforeRead)
	}
	read := startResearchAssistantFixtureChat(t, app, entry.ConversationID, profile.ID, "read-origins：按需核对工作记录字段来源")
	mu.Lock()
	n, readPages, compact := counts["read-origins"], pages, checkpoints
	mu.Unlock()
	if n != readPages+1 || readPages < 2 || read.ContextWindowTokens != 32000 || compact > 1 {
		t.Fatal("source paging changed window or repeated work", n, readPages, read.ContextWindowTokens, compact)
	}
	if _, err := app.WorkflowFacade.GetResearchWorkingStateProvenance(project.ID, entry.ResearchTaskID, 1); err == nil {
		t.Fatal("stale source view silently followed the latest note")
	}
	unchanged, err := app.WorkflowFacade.GetResearchWorkingState(project.ID, entry.ResearchTaskID)
	if err != nil || !reflect.DeepEqual(note, unchanged) {
		t.Fatal("source reading wrote or approved the working note", unchanged, err)
	}
	current, err := app.WorkflowFacade.GetCurrentResearchTaskRun(project.ID, entry.ResearchTaskID)
	if err == nil || current.Run.ID != "" || len(current.AIExecutions) != 0 {
		t.Fatal("source history read created/executed a route", current.Run.ID, err)
	}
	for _, table := range []string{"attachments", "knowledge_documents", "artifacts"} {
		var count int
		if err := app.store.DB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("source identity was promoted to material/evidence", table, count, err)
		}
	}
	ordinary, err := app.ConversationFacade.CreateConversation(wails.CreateConversationRequest{ProjectID: project.ID, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	startResearchAssistantFixtureChat(t, app, ordinary.ID, profile.ID, "ordinary-control")
	t.Logf("32k controlled local model HTTP: initial/patch=%d provenance=%d (%d exact pages) checkpoint=%d ordinary=1; no source HTTP or paid model", beforeRead, n, readPages, compact)
}

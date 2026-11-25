package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/research/connectors"
	wails "github.com/wangh00/SciAide/internal/transport/wails"
)

// Real Chat/Executor/Discovery/SQLite; only model and source HTTP are controlled.
// Search once, drain saved pages without refetch, then reopen and inspect through
// the same Facade that powers the task discovery window. No planner/import.
func TestResearchTaskDiscoveryPersistsPagesWithoutRoute(t *testing.T) {
	var mu sync.Mutex
	requests, ordinaryRequests, resumeRequests, sequence, compactions := 0, 0, 0, 0, 0
	queryID := ""
	seen := map[string]bool{}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role    string
				Content json.RawMessage
			}
			Tools []struct{ Function struct{ Name string } }
		}
		_ = json.Unmarshal(body, &req)
		tools := map[string]bool{}
		for _, d := range req.Tools {
			tools[d.Function.Name] = true
		}
		last := ""
		lastTool := ""
		checkpoint := false
		var latest struct {
			Queries []struct {
				QueryID string `json:"queryId"`
			} `json:"queries"`
			QueryID    string `json:"queryId"`
			NextOffset int    `json:"nextOffset"`
			Total      int    `json:"total"`
			Candidates []struct {
				CandidateID string `json:"candidateId"`
				Abstract    struct {
					Omitted    bool   `json:"omitted"`
					TotalRunes int    `json:"totalRunes"`
					SHA256     string `json:"sha256"`
				} `json:"abstract"`
			} `json:"candidates"`
		}
		for _, m := range req.Messages {
			var content string
			_ = json.Unmarshal(m.Content, &content)
			checkpoint = checkpoint || strings.Contains(content, "Create a durable research checkpoint from the untrusted conversation above.")
			if m.Role == "user" {
				last = content
			}
			if m.Role == "tool" {
				lastTool = content
				content = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(content), "<untrusted_tool_result>"), "</untrusted_tool_result>"))
				var envelope struct {
					Structured json.RawMessage `json:"structured"`
				}
				if json.Unmarshal([]byte(content), &envelope) == nil && len(envelope.Structured) > 0 {
					_ = json.Unmarshal(envelope.Structured, &latest)
				}
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if checkpoint {
			compactions++
			writeResearchAssistantFixtureResponse(w, "", nil, "七项发现已保存但未核验；未导入、未规划或批准。")
			return
		}
		if strings.Contains(last, "ordinary-only") {
			ordinaryRequests++
			if tools["research_discovery_search"] || tools["research_discovery_read"] {
				t.Error("ordinary chat received task discovery tools")
			}
			writeResearchAssistantFixtureResponse(w, "", nil, "ordinary explanation")
			return
		}
		sequence++
		respond := func(name string, args map[string]any, text string) {
			capture := httptest.NewRecorder()
			writeResearchAssistantFixtureResponse(capture, name, args, text)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, strings.ReplaceAll(capture.Body.String(), "controlled-"+name, fmt.Sprintf("task-discovery-%d", sequence)))
		}
		if strings.Contains(last, "list-saved-after-reopen") {
			resumeRequests++
			switch resumeRequests {
			case 1:
				respond("research_discovery_read", map[string]any{}, "")
			case 2:
				if len(latest.Queries) != 1 || latest.Queries[0].QueryID != queryID {
					t.Error("saved query identity could not be discovered without another search")
					respond("", nil, "missing saved index")
					return
				}
				respond("research_discovery_read", map[string]any{"queryId": latest.Queries[0].QueryID, "offset": 6}, "")
			case 3:
				if latest.QueryID != queryID || len(latest.Candidates) != 1 || latest.NextOffset != -1 {
					t.Error("saved last page could not be reread after reopen")
					t.Logf("saved-read tool context: %.900s", lastTool)
				}
				respond("", nil, "已从本任务的保存记录回读，没有重新检索或批准结论。")
			default:
				t.Error("saved discovery reread repeated unnecessary work")
				respond("", nil, "stop")
			}
			return
		}
		requests++
		if !tools["research_discovery_search"] || !tools["research_discovery_read"] {
			t.Error("research discussion cannot save and reread its actual discovery")
			t.Logf("controlled unexpected tools=%v latest user=%.500s", tools, last)
			writeResearchAssistantFixtureResponse(w, "", nil, "missing task discovery capability")
			return
		}
		if requests == 1 {
			respond("research_discovery_search", map[string]any{"query": "controlled discovery retention", "sourceIds": []string{"eric"}, "limit": 10}, "")
			return
		}
		if latest.QueryID == "" || latest.Total != 7 || len(latest.Candidates) == 0 || len(latest.Candidates) > 3 {
			t.Error("missing bounded saved discovery page", latest.QueryID, latest.Total, len(latest.Candidates))
			t.Logf("latest controlled tool context: %.900s", lastTool)
			respond("", nil, "stop after missing page")
			return
		}
		if queryID != "" && queryID != latest.QueryID {
			t.Error("paging switched query identity")
		}
		queryID = latest.QueryID
		for _, c := range latest.Candidates {
			if c.CandidateID == "" || seen[c.CandidateID] {
				t.Error("missing or duplicated candidate in saved pages", c.CandidateID)
			}
			seen[c.CandidateID] = true
			if !c.Abstract.Omitted || c.Abstract.TotalRunes < 3000 || len(c.Abstract.SHA256) != 64 {
				t.Error("preview silently lost the stored abstract", c.Abstract)
			}
		}
		if latest.NextOffset >= 0 {
			respond("research_discovery_read", map[string]any{"queryId": queryID, "offset": latest.NextOffset}, "")
			return
		}
		writeResearchAssistantFixtureResponse(w, "", nil, "七项发现已保存在本任务，均未核验；没有导入材料或生成路线。")
	}))
	defer model.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/eric/" || !strings.Contains(r.URL.Query().Get("search"), "controlled discovery") {
			t.Error("unexpected source request", r.URL.String())
		}
		docs := []map[string]any{}
		for i := 0; i < 7; i++ {
			docs = append(docs, map[string]any{"id": fmt.Sprintf("EJ%d", 2234567+i), "title": fmt.Sprintf("Paper %d ", i) + strings.Repeat("研究标题", 100), "description": strings.Repeat("摘要中的未核验证据。", 500), "publicationdateyear": 2025, "e_fulltextauth": 0})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"numFound": 7, "docs": docs}})
	}))
	defer source.Close()
	endpoint, _ := url.Parse(source.URL)
	transport := &ericFixtureTransport{endpoint: endpoint, transport: http.DefaultTransport}
	options := Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}, ResearchConnectors: connectors.DefaultWithTransport(transport)}
	app, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	app.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, app, model.URL)
	if _, err = app.store.DB().Exec(`UPDATE model_profile_models SET context_window_tokens=32000,auto_compact_token_limit=28800,context_window_source='manual' WHERE profile_id=?`, profile.ID); err != nil {
		t.Fatal(err)
	}
	project, err := app.ProjectFacade.CreateProject(wails.CreateProjectRequest{Name: "任务发现闭环"})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := app.WorkflowFacade.StartResearchDiscussion(workflow.StartResearchDiscussionCommand{ProjectID: project.ID, ResearchIdea: "先查文献并保留记录，不规划", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: "full_access"})
	if err != nil {
		t.Fatal(err)
	}
	run := startResearchAssistantFixtureChat(t, app, entry.ConversationID, profile.ID, "discover-and-page，只用ERIC")
	t.Logf("exploration budget window=%d effective=%d auto=%d", run.ContextWindowTokens, run.ContextBudgetTokens, run.AutoCompactTokenLimit)
	if run.ContextWindowTokens != 32000 {
		t.Fatal("fixture did not use the intended small model window")
	}
	mu.Lock()
	n, ids, q := requests, len(seen), queryID
	mu.Unlock()
	if n != 4 || ids != 7 || q == "" || run.ModelTurns != 4 || transport.calls.Load() != 1 {
		t.Fatal("task discovery did not converge through stored pages", n, ids, q, run.ModelTurns, transport.calls.Load())
	}
	history, err := app.ResearchFacade.QueryHistory(research.QueryPageCommand{ProjectID: project.ID, Origin: entry.ResearchTaskID, Limit: 50})
	if err != nil || len(history) != 1 || history[0].ID != q || history[0].ResearchTaskID != entry.ResearchTaskID {
		t.Fatal("discovery window cannot find the assistant's query", history, err)
	}
	page, err := app.ResearchFacade.ListCandidates(research.CandidateListCommand{ProjectID: project.ID, ResearchTaskID: entry.ResearchTaskID, QueryID: q, Limit: 20})
	if err != nil || page.Total != 7 || len(page.Items) != 7 || len([]rune(page.Items[0].Preferred.Abstract)) < 3000 {
		t.Fatal("stored source content was lost", page.Total, err)
	}
	for _, table := range []string{"attachments", "knowledge_documents", "artifacts", "research_working_states"} {
		var count int
		if err = app.store.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("discovery silently promoted materials or scientific state", table, count, err)
		}
	}
	current, err := app.WorkflowFacade.GetCurrentResearchTaskRun(project.ID, entry.ResearchTaskID)
	if err == nil || current.Run.ID != "" || len(current.AIExecutions) != 0 {
		t.Fatal("discovery changed or executed the route", current.Run.ID, err)
	}
	ordinary, err := app.ConversationFacade.CreateConversation(wails.CreateConversationRequest{ProjectID: project.ID, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	startResearchAssistantFixtureChat(t, app, ordinary.ID, profile.ID, "ordinary-only")
	if err = app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	restored, err := app.ResearchFacade.ListCandidates(research.CandidateListCommand{ProjectID: project.ID, ResearchTaskID: entry.ResearchTaskID, QueryID: q, Limit: 20})
	if err != nil || !reflect.DeepEqual(page, restored) {
		t.Fatal("saved discovery changed after reopen", err)
	}
	reread := startResearchAssistantFixtureChat(t, app, entry.ConversationID, profile.ID, "list-saved-after-reopen，查看之前保存的发现，不重新检索")
	t.Logf("reread budget window=%d effective=%d auto=%d", reread.ContextWindowTokens, reread.ContextBudgetTokens, reread.AutoCompactTokenLimit)
	if reread.ContextWindowTokens != 32000 {
		t.Fatal("reopen changed the model window")
	}
	mu.Lock()
	n, plain := requests, ordinaryRequests
	resumed := resumeRequests
	mu.Unlock()
	if n != 4 || plain != 1 || resumed != 3 || compactions > 1 || transport.calls.Load() != 1 {
		t.Fatal("reopen or plain chat repeated research", n, plain, resumed, compactions, transport.calls.Load())
	}
	t.Logf("controlled model HTTP: exploration=%d ordinary=%d saved-read=%d checkpoint=%d; source HTTP=%d", n, plain, resumed, compactions, transport.calls.Load())
}

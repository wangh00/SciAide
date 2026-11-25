package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/research/connectors"
	wails "github.com/wangh00/SciAide/internal/transport/wails"
)

// The source and project-wide record change after the first query. A real
// model-tool continuation must still read the original saved abstract without
// fetching, importing or promoting it to full-text/scientific evidence.
func TestResearchDiscoveryReadsOriginalFieldAfterRestart(t *testing.T) {
	original := strings.Repeat("保存时的摘要🙂 \"quoted\"\n", 18) + "原始尾部"
	later := "LATER metadata must not replace the saved abstract."
	wantHash := sha256.Sum256([]byte(original))
	var generation atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		text := original
		if generation.Load() > 0 {
			text = later
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"numFound": 1, "docs": []map[string]any{{"id": "EJ4234567", "title": "Original stored abstract", "description": text, "publicationdateyear": 2025, "e_fulltextauth": 0}}}})
	}))
	defer source.Close()
	endpoint, _ := url.Parse(source.URL)
	transport := &ericFixtureTransport{endpoint: endpoint, transport: http.DefaultTransport}
	var mu sync.Mutex
	requests, checkpoints := 0, 0
	queryID, candidateID, readText := "", "", ""
	nextOffset := 0
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
		var result map[string]json.RawMessage
		checkpoint := false
		for _, m := range req.Messages {
			var text string
			_ = json.Unmarshal(m.Content, &text)
			checkpoint = checkpoint || strings.Contains(text, "Create a durable research checkpoint from the untrusted conversation above.")
			if m.Role == "tool" {
				text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(text), "<untrusted_tool_result>"), "</untrusted_tool_result>"))
				var v struct {
					Structured map[string]json.RawMessage `json:"structured"`
				}
				if json.Unmarshal([]byte(text), &v) == nil {
					result = v.Structured
				}
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if checkpoint {
			checkpoints++
			writeResearchAssistantFixtureResponse(w, "", nil, "摘要未核验；保留保存记录。")
			return
		}
		requests++
		respond := func(args map[string]any) {
			capture := httptest.NewRecorder()
			writeResearchAssistantFixtureResponse(capture, "research_discovery_read", args, "")
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, strings.ReplaceAll(capture.Body.String(), "controlled-research_discovery_read", fmt.Sprintf("saved-field-%d", requests)))
		}
		stop := func(reason string) {
			t.Error(reason)
			writeResearchAssistantFixtureResponse(w, "", nil, "停止，不补造缺失内容。")
		}
		if requests > 16 {
			stop("saved-field reading did not converge")
			return
		}
		switch requests {
		case 1:
			respond(map[string]any{})
			return
		case 2:
			var rows []struct {
				QueryID string `json:"queryId"`
				Query   struct {
					Text string `json:"text"`
				} `json:"query"`
			}
			_ = json.Unmarshal(result["queries"], &rows)
			for _, q := range rows {
				if q.Query.Text == "snapshot original" {
					queryID = q.QueryID
				}
			}
			if queryID == "" {
				stop("original query was not discoverable after restart")
				return
			}
			respond(map[string]any{"queryId": queryID})
			return
		case 3:
			var rows []struct {
				ID       string `json:"candidateId"`
				Abstract struct {
					SHA     string `json:"sha256"`
					Omitted bool   `json:"omitted"`
				} `json:"abstract"`
			}
			_ = json.Unmarshal(result["candidates"], &rows)
			if len(rows) != 1 || !rows[0].Abstract.Omitted || rows[0].Abstract.SHA != hex.EncodeToString(wantHash[:]) {
				stop("index lost the original saved-field identity")
				return
			}
			candidateID = rows[0].ID
		default:
			var status, field, hash, content string
			var offset, next, total int
			var available bool
			_ = json.Unmarshal(result["status"], &status)
			_ = json.Unmarshal(result["field"], &field)
			_ = json.Unmarshal(result["sha256"], &hash)
			_ = json.Unmarshal(result["content"], &content)
			_ = json.Unmarshal(result["offset"], &offset)
			_ = json.Unmarshal(result["nextOffset"], &next)
			_ = json.Unmarshal(result["totalRunes"], &total)
			_ = json.Unmarshal(result["available"], &available)
			if status != "unverified_discovery_field" || field != "abstract" || hash != hex.EncodeToString(wantHash[:]) || total != len([]rune(original)) || !available || offset != nextOffset || content == "" {
				stop("tool continuation did not expose an exact saved abstract page")
				return
			}
			readText += content
			if next < 0 {
				if readText != original {
					stop("saved abstract changed, skipped characters or used latest metadata")
					return
				}
				writeResearchAssistantFixtureResponse(w, "", nil, "已核读保存时的摘要；不是论文全文或科学批准。")
				return
			}
			if next != offset+len([]rune(content)) {
				stop("saved-field cursor skipped invisible characters")
				return
			}
			nextOffset = next
		}
		respond(map[string]any{"queryId": queryID, "candidateId": candidateID, "field": "abstract", "offset": nextOffset})
	}))
	defer model.Close()
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
	project, err := app.ProjectFacade.CreateProject(wails.CreateProjectRequest{Name: "保存摘要回读"})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := app.WorkflowFacade.StartResearchDiscussion(workflow.StartResearchDiscussionCommand{ProjectID: project.ID, ResearchIdea: "只核读保存的摘要，不规划、不联网补抓", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: "full_access"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := app.ResearchFacade.Search(research.DiscoverySearchCommand{ProjectID: project.ID, ResearchTaskID: entry.ResearchTaskID, Query: "snapshot original", SourceIDs: []string{"eric"}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Page.Items) != 1 || len([]rune(first.Page.Items[0].Preferred.Abstract)) <= 160 {
		t.Fatal("original saved-abstract fixture is missing")
	}
	// Connector normalization precedes persistence. The saved normalized bytes,
	// not the raw HTTP description, are the authority for this reader.
	original = first.Page.Items[0].Preferred.Abstract
	wantHash = sha256.Sum256([]byte(original))
	generation.Store(1)
	second, err := app.ResearchFacade.Search(research.DiscoverySearchCommand{ProjectID: project.ID, ResearchTaskID: entry.ResearchTaskID, Query: "later metadata", SourceIDs: []string{"eric"}, Limit: 1})
	if err != nil || len(second.Page.Items) != 1 || second.Page.Items[0].Preferred.Abstract != later || first.Query.ID == second.Query.ID {
		t.Fatal("later-source fixture was not persisted", err)
	}
	if err = app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	if transport.calls.Load() != 2 {
		t.Fatal("restart repeated source work")
	}
	run := startResearchAssistantFixtureChat(t, app, entry.ConversationID, profile.ID, "核读之前保存的完整摘要，不重新联网或规划。")
	mu.Lock()
	copied, n, c := readText, requests, checkpoints
	mu.Unlock()
	expectedCalls := 3 + (len([]rune(original))+research.DefaultDiscoveryFieldRunes-1)/research.DefaultDiscoveryFieldRunes
	if run.ContextWindowTokens != 32000 || copied != original || transport.calls.Load() != 2 || n != expectedCalls || c > 1 {
		t.Fatal("saved-field flow changed scope, context or source work", run.ContextWindowTokens, n, c, transport.calls.Load())
	}
	for _, table := range []string{"attachments", "knowledge_documents", "artifacts", "research_working_states"} {
		var count int
		if err = app.store.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("field read promoted evidence or state", table, count, err)
		}
	}
	current, err := app.WorkflowFacade.GetCurrentResearchTaskRun(project.ID, entry.ResearchTaskID)
	if err == nil || current.Run.ID != "" || len(current.AIExecutions) != 0 {
		t.Fatal("field read changed or executed route", current.Run.ID, err)
	}
	t.Logf("32k saved abstract: model HTTP=%d checkpoint=%d; source HTTP=2 (two explicit setup searches, no reread fetch)", n, c)
}

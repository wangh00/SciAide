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
	"sync/atomic"
	"testing"

	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/research/connectors"
	wails "github.com/wangh00/SciAide/internal/transport/wails"
)

// Original Application, tools, success barrier and SQLite; only the model and
// ERIC HTTP endpoints are controlled localhost fixtures, never a paid model.
func TestResearchWorkingBasisLinksActualReadReceiptsAndReopensAfterRestart(t *testing.T) {
	abstract := strings.Repeat("保存的摘要🧪\n\"结局不一致，仍需核对\"。", 18)
	var sourceHTTP atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceHTTP.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"numFound": 1, "docs": []map[string]any{{"id": "EJ4234567", "title": "Original abstract", "description": abstract, "publicationdateyear": 2025}}}})
	}))
	defer source.Close()
	endpoint, _ := url.Parse(source.URL)
	var mu sync.Mutex
	queryID, candidateID, savedAbstract := "", "", ""
	reads := []string{}
	requests, checkpoints := 0, 0
	requestSizes := []int{}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role    string
				Content json.RawMessage
			}
		}
		_ = json.Unmarshal(body, &req)
		lastUser := ""
		checkpoint := false
		var envelope struct {
			ToolCallID string          `json:"toolCallId"`
			Status     string          `json:"status"`
			Structured json.RawMessage `json:"structured"`
		}
		for _, m := range req.Messages {
			var text string
			_ = json.Unmarshal(m.Content, &text)
			checkpoint = checkpoint || strings.Contains(text, "Create a durable research checkpoint from the untrusted conversation above.")
			if m.Role == "user" {
				lastUser = text
				envelope.ToolCallID = ""
			}
			if m.Role == "system" && (strings.Contains(text, abstract[:30]) || strings.Contains(text, "模型关联理由🧪")) {
				t.Error("working scientific content pinned in trusted system context")
			}
			if m.Role == "tool" {
				text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(text), "<untrusted_tool_result>"), "</untrusted_tool_result>"))
				_ = json.Unmarshal([]byte(text), &envelope)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		requestSizes = append(requestSizes, len(body))
		if checkpoint {
			checkpoints++
			writeResearchAssistantFixtureResponse(w, "", nil, "工作记录未核验，不补造来源。")
			return
		}
		requests++
		n := requests
		respond := func(name string, args map[string]any) {
			rec := httptest.NewRecorder()
			writeResearchAssistantFixtureResponse(rec, name, args, "")
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, strings.ReplaceAll(rec.Body.String(), "controlled-"+name, fmt.Sprintf("basis-fixture-%d", n)))
		}
		if strings.Contains(lastUser, "link-basis") {
			if n == 2 || n == 3 {
				p, err := workflow.ParseResearchWorkingAbstractPage(envelope.Structured)
				wantOffset := (n - 2) * 64
				if err != nil || envelope.Status != "success" || envelope.ToolCallID == "" || p.Offset != wantOffset || p.Content != string([]rune(savedAbstract)[wantOffset:wantOffset+64]) {
					t.Errorf("actual continuation page mismatch: request=%d status=%q receipt=%q offset=%d length=%d parse=%v", n, envelope.Status, envelope.ToolCallID, p.Offset, len([]rune(p.Content)), err)
					writeResearchAssistantFixtureResponse(w, "", nil, "停止，不补造关联。")
					return
				}
				reads = append(reads, envelope.ToolCallID)
			}
			switch n {
			case 1, 2:
				respond("research_discovery_read", map[string]any{"queryId": queryID, "candidateId": candidateID, "field": "abstract", "offset": (n - 1) * 64, "limit": 64})
			case 3:
				respond("research_state_update", map[string]any{"expectedVersion": 0, "currentFocus": "核对冲突条件", "rationale": "模型关联理由🧪，不是科学批准", "basis": map[string]any{"rationale": append([]string{}, reads...)}})
			case 4:
				writeResearchAssistantFixtureResponse(w, "", nil, "已保存两页的显式关联；仍未核验，不规划。")
			default:
				t.Error("read/link fixture did not converge")
				writeResearchAssistantFixtureResponse(w, "", nil, "停止。")
			}
		} else if strings.Contains(lastUser, "sparse-next") && n == 5 {
			respond("research_state_update", map[string]any{"expectedVersion": 1, "nextStep": "先核对任务条件，仍不执行实验"})
		} else {
			writeResearchAssistantFixtureResponse(w, "", nil, "仅更新下一步；旧关联不代表新的支持判断。")
		}
	}))
	defer model.Close()
	options := Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}, ResearchConnectors: connectors.DefaultWithTransport(&ericFixtureTransport{endpoint: endpoint, transport: http.DefaultTransport})}
	app, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	app.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, app, model.URL)
	if _, err := app.store.DB().Exec(`UPDATE model_profile_models SET context_window_tokens=32000,auto_compact_token_limit=28800,context_window_source='manual' WHERE profile_id=?`, profile.ID); err != nil {
		t.Fatal(err)
	}
	project, err := app.ProjectFacade.CreateProject(wails.CreateProjectRequest{Name: "工作记录显式摘要关联"})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := app.WorkflowFacade.StartResearchDiscussion(workflow.StartResearchDiscussionCommand{ProjectID: project.ID, ResearchIdea: "只关联实际读过的摘要页，不规划、不采用结论", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: "full_access"})
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := app.ResearchFacade.Search(research.DiscoverySearchCommand{ProjectID: project.ID, ResearchTaskID: entry.ResearchTaskID, Query: "basis fixture", SourceIDs: []string{"eric"}, Limit: 1})
	if err != nil || len(discovery.Page.Items) != 1 {
		t.Fatal("missing source fixture", err)
	}
	mu.Lock()
	queryID, candidateID = discovery.Query.ID, discovery.Page.Items[0].ID
	savedAbstract = discovery.Page.Items[0].Preferred.Abstract
	mu.Unlock()
	first := startResearchAssistantFixtureChat(t, app, entry.ConversationID, profile.ID, "link-basis：读取两页摘要并保存工作记录关联，不规划")
	second := startResearchAssistantFixtureChat(t, app, entry.ConversationID, profile.ID, "sparse-next：只改下一步，保留旧字段及原关联")
	if first.ContextWindowTokens != 32000 || second.ContextWindowTokens != 32000 {
		t.Fatal("context limit changed")
	}
	note, err := app.WorkflowFacade.GetResearchWorkingState(project.ID, entry.ResearchTaskID)
	if err != nil || note == nil || note.Version != 2 || note.Rationale != "模型关联理由🧪，不是科学批准" {
		t.Fatal("linked sparse state not published", note, err)
	}
	sources, err := app.WorkflowFacade.GetResearchWorkingStateProvenance(project.ID, entry.ResearchTaskID, 2)
	if err != nil || len(sources.Fields) != 4 || len(sources.Fields[1].Basis) != 2 || sources.Fields[1].SourceChatRunID != first.ID {
		t.Fatal("field relation lost original submission", sources, err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	reopened, err := app.WorkflowFacade.GetResearchWorkingStateProvenance(project.ID, entry.ResearchTaskID, 2)
	if err != nil || !reflect.DeepEqual(sources, reopened) {
		t.Fatal("restart changed basis", reopened, err)
	}
	for i, b := range reopened.Fields[1].Basis {
		got, err := app.WorkflowFacade.GetResearchWorkingStateBasis(project.ID, entry.ResearchTaskID, 2, "rationale", b.ReadCallID)
		if err != nil || got.Content != string([]rune(savedAbstract)[i*64:(i+1)*64]) {
			t.Fatal("reopened page is not exact original", got, err)
		}
	}
	if _, err := app.WorkflowFacade.GetResearchWorkingStateBasis(project.ID, entry.ResearchTaskID, 1, "rationale", reads[0]); err == nil {
		t.Fatal("stale read accepted")
	}
	current, err := app.WorkflowFacade.GetCurrentResearchTaskRun(project.ID, entry.ResearchTaskID)
	if err == nil || current.Run.ID != "" || len(current.AIExecutions) != 0 {
		t.Fatal("read/link started a route", current.Run.ID, err)
	}
	for _, table := range []string{"attachments", "knowledge_documents", "artifacts"} {
		var n int
		if err := app.store.DB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatal("association promoted source into evidence", table, n, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 6 || checkpoints > 1 || sourceHTTP.Load() != 1 {
		t.Fatal("read/link repeated work or read-only calls contacted a model/source", requests, checkpoints, sourceHTTP.Load())
	}
	total, maxRequest := 0, 0
	for _, n := range requestSizes {
		total += n
		maxRequest = max(maxRequest, n)
	}
	t.Logf("32k localhost fixtures: 6 model HTTP + %d checkpoint, 1 ERIC; bytes=%v total=%d max=%d; 2 exact 64-rune pages linked/reopened after sparse patch/restart; 0 paid model", checkpoints, requestSizes, total, maxRequest)
}

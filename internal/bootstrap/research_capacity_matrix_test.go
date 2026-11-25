package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/research/connectors"
	wails "github.com/wangh00/SciAide/internal/transport/wails"
)

type researchCapacityCase struct {
	Name           string `json:"name"`
	Mode           string `json:"mode"`
	Window         int    `json:"window"`
	Candidates     int    `json:"candidates"`
	AbstractRunes  int    `json:"abstractRunes"`
	InputRunes     int    `json:"inputRunes"`
	SyntheticUsage bool   `json:"syntheticUsage,omitempty"`
}

func researchCapacityCases() []researchCapacityCase {
	cases := []researchCapacityCase{}
	for _, window := range []int{16000, 24000, 28000, 30000, 32000, 36000} {
		cases = append(cases, researchCapacityCase{Name: fmt.Sprintf("plain_%d", window), Mode: "plain", Window: window})
	}
	for _, n := range []int{3, 12, 48} {
		cases = append(cases, researchCapacityCase{Name: fmt.Sprintf("index_%d_32000", n), Mode: "index", Window: 32000, Candidates: n, AbstractRunes: 10000})
	}
	for _, n := range []int{128, 1600, 6400, 50000} {
		mode := "abstract"
		if n == 50000 {
			mode = "abstract_edges"
		}
		cases = append(cases, researchCapacityCase{Name: fmt.Sprintf("abstract_%d_32000", n), Mode: mode, Window: 32000, Candidates: 1, AbstractRunes: n})
	}
	for _, window := range []int{28000, 30000, 32000} {
		cases = append(cases, researchCapacityCase{Name: fmt.Sprintf("linked_%d", window), Mode: "linked", Window: window, Candidates: 1, AbstractRunes: 128})
	}
	return append(cases, researchCapacityCase{Name: "plain_input8000_32000", Mode: "plain", Window: 32000, InputRunes: 8000}, researchCapacityCase{Name: "index_12_30000", Mode: "index", Window: 30000, Candidates: 12, AbstractRunes: 10000})
}

type capacityToolSize struct {
	Name  string `json:"name"`
	Bytes int    `json:"bytes"`
	Runes int    `json:"runes"`
}
type capacityRequest struct {
	Bytes            int    `json:"bytes"`
	ToolBytes        int    `json:"toolBytes"`
	ToolRunes        int    `json:"toolRunes"`
	ToolCount        int    `json:"toolCount"`
	SystemRunes      int    `json:"systemRunes"`
	UserRunes        int    `json:"userRunes"`
	ToolMessageRunes int    `json:"toolMessageRunes"`
	Phase            string `json:"phase"`
	Checkpoint       bool   `json:"checkpoint"`
}
type capacityResult struct {
	Case                  researchCapacityCase `json:"case"`
	Outcome               string               `json:"outcome"`
	Boundary              string               `json:"boundary,omitempty"`
	RunStatus             chat.RunStatus       `json:"runStatus"`
	ErrorCode             string               `json:"errorCode,omitempty"`
	ErrorMessage          string               `json:"errorMessage,omitempty"`
	ErrorDetails          string               `json:"errorDetails,omitempty"`
	EffectiveBudget       int                  `json:"effectiveBudget"`
	AutoCompactLimit      int                  `json:"autoCompactLimit"`
	ModelHTTP             int                  `json:"modelHTTP"`
	CheckpointHTTP        int                  `json:"checkpointHTTP"`
	SourceHTTP            int                  `json:"sourceHTTP"`
	SavedCandidates       int                  `json:"savedCandidates"`
	SavedAbstractRunes    int                  `json:"savedAbstractRunes"`
	CandidateIndexPages   int                  `json:"candidateIndexPages"`
	CandidatesShown       int                  `json:"candidatesShown"`
	AbstractPagesShown    int                  `json:"abstractPagesShown"`
	AbstractRunesShown    int                  `json:"abstractRunesShown"`
	CompletedFieldResults int                  `json:"completedFieldResults"`
	CompletedIndexResults int                  `json:"completedIndexResults"`
	StoredIndexCandidates int                  `json:"storedIndexCandidates"`
	CompletedTools        int                  `json:"completedTools"`
	PhaseAtStop           string               `json:"phaseAtStop"`
	ProviderInputTokens   int                  `json:"providerReportedInputTokens"`
	NotePublished         bool                 `json:"notePublished"`
	ScriptFinished        bool                 `json:"scriptFinished"`
	LargestTools          []capacityToolSize   `json:"largestTools,omitempty"`
	Requests              []capacityRequest    `json:"requests"`
}

// This is a bounded model-response fixture, NOT another research executor.
// All routing, context admission, reads, writes and persistence use the original
// Application. It measures host capacity, not an LLM's understanding or recall.
type capacityScript struct {
	mu                   sync.Mutex
	test                 researchCapacityCase
	result               capacityResult
	phase                string
	queryID, candidateID string
	texts                map[string]string
	seenCandidates       map[string]bool
	seenReads            map[string]bool
	readIDs              []string
	failure              string
	boundary             string
	done                 bool
}

func (s *capacityScript) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		http.Error(w, "fixture read failed", 500)
		return
	}
	var req struct {
		Messages []struct {
			Role    string
			Content json.RawMessage
		}
		Tools []json.RawMessage
	}
	_ = json.Unmarshal(body, &req)
	var latest struct {
		ToolCallID string          `json:"toolCallId"`
		Status     string          `json:"status"`
		Structured json.RawMessage `json:"structured"`
	}
	m := capacityRequest{Bytes: len(body), ToolCount: len(req.Tools)}
	for _, message := range req.Messages {
		var text string
		_ = json.Unmarshal(message.Content, &text)
		m.Checkpoint = m.Checkpoint || strings.Contains(text, "Create a durable research checkpoint from the untrusted conversation above.")
		switch message.Role {
		case "system":
			m.SystemRunes += utf8.RuneCountInString(text)
		case "user":
			m.UserRunes += utf8.RuneCountInString(text)
			latest.Structured = nil
		case "tool":
			m.ToolMessageRunes += utf8.RuneCountInString(text)
			text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(text), "<untrusted_tool_result>"), "</untrusted_tool_result>"))
			_ = json.Unmarshal([]byte(text), &latest)
		}
	}
	toolJSON, _ := json.Marshal(req.Tools)
	m.ToolBytes = len(toolJSON)
	m.ToolRunes = utf8.RuneCount(toolJSON)
	s.mu.Lock()
	defer s.mu.Unlock()
	m.Phase = s.phase
	s.result.Requests = append(s.result.Requests, m)
	if len(s.result.Requests) == 1 {
		for _, raw := range req.Tools {
			var d struct{ Function struct{ Name string } }
			_ = json.Unmarshal(raw, &d)
			s.result.LargestTools = append(s.result.LargestTools, capacityToolSize{Name: d.Function.Name, Bytes: len(raw), Runes: utf8.RuneCount(raw)})
		}
		sort.Slice(s.result.LargestTools, func(i, j int) bool { return s.result.LargestTools[i].Runes > s.result.LargestTools[j].Runes })
		if len(s.result.LargestTools) > 6 {
			s.result.LargestTools = s.result.LargestTools[:6]
		}
	}
	// An explicitly synthetic 4-wire-runes/input-token control exercises the
	// existing usage-anchor path. It is NOT a tokenizer or measured model usage.
	write := func(name string, args map[string]any, text string) {
		rec := httptest.NewRecorder()
		writeResearchAssistantFixtureResponse(rec, name, args, text)
		payload := strings.ReplaceAll(rec.Body.String(), "controlled-"+name, fmt.Sprintf("capacity-%d", len(s.result.Requests)))
		if s.test.SyntheticUsage {
			line := strings.SplitN(strings.TrimPrefix(payload, "data: "), "\n", 2)[0]
			var chunk map[string]any
			_ = json.Unmarshal([]byte(line), &chunk)
			input := max(1, utf8.RuneCount(body)/4)
			chunk["usage"] = map[string]int{"prompt_tokens": input, "completion_tokens": 1, "total_tokens": input + 1}
			encoded, _ := json.Marshal(chunk)
			payload = "data: " + string(encoded) + "\n\ndata: [DONE]\n\n"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, payload)
	}
	stop := func(reason string) {
		if s.failure == "" {
			s.failure = reason
		}
		write("", nil, "容量夹具停止；不补造内容或批准科学结论。")
	}
	// The host's compaction turn is summarization-only and carries no tool
	// definitions. Answer with a bounded plain summary so the durable
	// checkpoint stays a summary, not a forged tool action.
	if m.Checkpoint || (len(req.Tools) == 0 && s.phase != "start") {
		write("", nil, "已读页与候选索引已按检查点概述；继续按可见 nextOffset 或已保存摘要回答，不补造未读内容。")
		return
	}
	if len(s.result.Requests) > 96 {
		stop("fixture HTTP ceiling reached")
		return
	}
	if m.Checkpoint {
		s.result.CheckpointHTTP++
		if s.result.CheckpointHTTP > 4 {
			stop("fixture checkpoint ceiling reached")
			return
		}
		write("", nil, "受控容量检查点：摘要与工作记录仍未核验，未规划或执行。")
		return
	}
	s.result.ModelHTTP++
	respond := func(name string, args map[string]any) {
		write(name, args, "")
	}
	finish := func() {
		s.done = true
		write("", nil, "所选读取已完成；不是全文核验、科学批准或研究执行。")
	}
	readField := func(offset int) {
		limit := 400
		if s.test.Mode == "linked" {
			limit = 64
		}
		respond("research_discovery_read", map[string]any{"queryId": s.queryID, "candidateId": s.candidateID, "field": "abstract", "offset": offset, "limit": limit})
		s.phase = "field"
	}
	if s.phase == "start" {
		if s.test.Mode == "plain" || s.test.Mode == "ordinary" {
			finish()
			return
		}
		respond("research_discovery_read", map[string]any{})
		s.phase = "queries"
		return
	}
	if latest.Status != "success" || len(latest.Structured) == 0 {
		stop("latest real tool result was not visible/successful")
		return
	}
	switch s.phase {
	case "queries":
		var p struct {
			Queries []struct {
				QueryID string `json:"queryId"`
			}
		}
		if json.Unmarshal(latest.Structured, &p) != nil || len(p.Queries) != 1 || p.Queries[0].QueryID == "" {
			stop("saved query directory missing")
			return
		}
		s.queryID = p.Queries[0].QueryID
		respond("research_discovery_read", map[string]any{"queryId": s.queryID, "offset": 0, "limit": 3})
		s.phase = "index"
	case "index":
		var p struct {
			QueryID    string `json:"queryId"`
			Offset     int    `json:"offset"`
			NextOffset int    `json:"nextOffset"`
			Total      int    `json:"total"`
			Candidates []struct {
				ID string `json:"candidateId"`
			}
		}
		if json.Unmarshal(latest.Structured, &p) != nil || p.QueryID != s.queryID || p.Total != s.test.Candidates || len(p.Candidates) == 0 {
			stop("candidate page missing/changed")
			return
		}
		s.result.CandidateIndexPages++
		for _, c := range p.Candidates {
			if c.ID == "" || s.seenCandidates[c.ID] {
				stop("candidate page repeated an identity")
				return
			}
			s.seenCandidates[c.ID] = true
		}
		s.result.CandidatesShown = len(s.seenCandidates)
		if s.test.Mode == "index" {
			if p.NextOffset < 0 {
				finish()
				return
			}
			if p.NextOffset != p.Offset+len(p.Candidates) {
				stop("candidate continuation skipped unseen entries")
				return
			}
			respond("research_discovery_read", map[string]any{"queryId": s.queryID, "offset": p.NextOffset, "limit": 3})
			return
		}
		s.candidateID = p.Candidates[0].ID
		readField(0)
	case "field":
		if _, parseErr := workflow.ParseResearchWorkingAbstractPage(latest.Structured); parseErr != nil && s.result.AbstractPagesShown > 0 {
			var regression struct {
				Status string `json:"status"`
			}
			if json.Unmarshal(latest.Structured, &regression) == nil && regression.Status == "unverified_discovery" {
				// The host compacted this run: the visible suffix restarted at an
				// earlier discovery envelope. That is an explicit checkpointed
				// boundary, not silent page loss; record it as a measurement.
				s.boundary = "compaction_continued"
				write("", nil, "容量夹具到达压缩边界；已读页保存在原快照与检查点中，不补造内容。")
				return
			}
		}
		p, err := workflow.ParseResearchWorkingAbstractPage(latest.Structured)
		text, exists := s.texts[s.candidateID]
		end := p.Offset + utf8.RuneCountInString(p.Content)
		if err != nil || !exists || p.QueryID != s.queryID || p.CandidateID != s.candidateID || end > utf8.RuneCountInString(text) || p.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(text))) || p.Content != string([]rune(text)[p.Offset:end]) {
			s.failure = fmt.Sprintf("model-visible page differs from original saved snapshot: err=%v structured=%s", err, string(latest.Structured))
			stop("model-visible page differs from original saved snapshot")
			return
		}
		key := fmt.Sprintf("%s:%d", p.CandidateID, p.Offset)
		if s.seenReads[key] || latest.ToolCallID == "" {
			stop("source page repeated or lost host receipt")
			return
		}
		s.seenReads[key] = true
		s.result.AbstractPagesShown++
		s.result.AbstractRunesShown += utf8.RuneCountInString(p.Content)
		s.readIDs = append(s.readIDs, latest.ToolCallID)
		if s.test.Mode == "abstract_edges" {
			if s.result.AbstractPagesShown == 1 {
				readField(max(end, p.TotalRunes-400))
				return
			}
			finish()
			return
		}
		if p.NextOffset >= 0 {
			if p.NextOffset != end {
				stop("field cursor skipped unseen characters")
				return
			}
			readField(p.NextOffset)
			return
		}
		if s.test.Mode == "linked" {
			respond("research_state_update", map[string]any{"expectedVersion": 0, "currentFocus": "核对研究条件", "rationale": "受控模型工作判断，未核验", "basis": map[string]any{"rationale": append([]string{}, s.readIDs...)}})
			s.phase = "state"
			return
		}
		finish()
	case "state":
		var p struct {
			Version int `json:"version"`
		}
		if json.Unmarshal(latest.Structured, &p) != nil || p.Version != 1 {
			stop("working note was not published via original tool")
			return
		}
		finish()
	default:
		stop("unexpected fixture phase")
	}
}

func capacityText(n int) string {
	seed := []rune("研究摘要🧪 中文与English：\"对照\"\\路径；条件不同，结局未核验。\n")
	out := make([]rune, n)
	for i := range out {
		out[i] = seed[i%len(seed)]
	}
	return string(out)
}

func waitCapacityChat(t *testing.T, app *Application, conversationID, profileID, text string) wails.RunSnapshot {
	t.Helper()
	messageID, _ := id.New()
	run, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: conversationID, ModelProfileID: profileID, ModelID: workflowAITestModelID, ClientMessageID: messageID, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := app.ChatFacade.GetRunSnapshot(run.ID)
		if err != nil {
			t.Fatal(err)
		}
		switch snapshot.Run.Status {
		case chat.RunCompleted, chat.RunFailed, chat.RunCancelled, chat.RunInterrupted:
			return snapshot
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("capacity fixture failed to terminate within 45s")
	return wails.RunSnapshot{}
}

// Survey results are deliberately separated from acceptance: a correctly
// refused narrow window is a measured stop, NOT successful research completion.
func TestResearchCapacityRepresentativeScaleAndNarrowWindows(t *testing.T) {
	for _, c := range researchCapacityCases() {
		t.Run(c.Name, func(t *testing.T) { runResearchCapacityCase(t, c) })
	}
}

func TestResearchCapacitySyntheticUsageControls(t *testing.T) {
	for _, c := range []researchCapacityCase{
		{Name: "synthetic_plain_24000", Mode: "plain", Window: 24000, SyntheticUsage: true},
		{Name: "synthetic_linked_28000", Mode: "linked", Window: 28000, Candidates: 1, AbstractRunes: 128, SyntheticUsage: true},
		{Name: "synthetic_index_48_32000", Mode: "index", Window: 32000, Candidates: 48, AbstractRunes: 10000, SyntheticUsage: true},
		{Name: "synthetic_abstract_6400_32000", Mode: "abstract", Window: 32000, Candidates: 1, AbstractRunes: 6400, SyntheticUsage: true},
	} {
		t.Run(c.Name, func(t *testing.T) { runResearchCapacityCase(t, c) })
	}
}

func runResearchCapacityCase(t *testing.T, c researchCapacityCase) capacityResult {
	t.Helper()
	var sourceHTTP atomic.Int32
	inputAbstract := capacityText(c.AbstractRunes)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceHTTP.Add(1)
		docs := []map[string]any{}
		for i := 0; i < c.Candidates; i++ {
			docs = append(docs, map[string]any{"id": fmt.Sprintf("EJ%07d", 7000000+i), "title": fmt.Sprintf("Capacity study %03d separate record", i), "description": inputAbstract, "publicationdateyear": 2025})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"numFound": c.Candidates, "docs": docs}})
	}))
	defer source.Close()
	script := &capacityScript{test: c, result: capacityResult{Case: c, Requests: []capacityRequest{}}, phase: "start", texts: map[string]string{}, seenCandidates: map[string]bool{}, seenReads: map[string]bool{}}
	model := httptest.NewServer(script)
	defer model.Close()
	endpoint, _ := url.Parse(source.URL)
	app, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}, ResearchConnectors: connectors.DefaultWithTransport(&ericFixtureTransport{endpoint: endpoint, transport: http.DefaultTransport})})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, app, model.URL)
	if _, err := app.store.DB().Exec(`UPDATE model_profile_models SET context_window_tokens=?,auto_compact_token_limit=?,context_window_source='manual' WHERE profile_id=?`, c.Window, c.Window*9/10, profile.ID); err != nil {
		t.Fatal(err)
	}
	project, err := app.ProjectFacade.CreateProject(wails.CreateProjectRequest{Name: "科研容量受控矩阵"})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := app.WorkflowFacade.StartResearchDiscussion(workflow.StartResearchDiscussionCommand{ProjectID: project.ID, ResearchIdea: "按需阅读并比较保存摘要，不规划、不执行", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: "full_access"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Candidates > 0 {
		found, err := app.ResearchFacade.Search(research.DiscoverySearchCommand{ProjectID: project.ID, ResearchTaskID: entry.ResearchTaskID, Query: "capacity fixture", SourceIDs: []string{"eric"}, Limit: c.Candidates})
		if err != nil {
			t.Fatal(err)
		}
		page, err := app.ResearchFacade.ListCandidates(research.CandidateListCommand{ProjectID: project.ID, ResearchTaskID: entry.ResearchTaskID, QueryID: found.Query.ID, Limit: 100})
		if err != nil || page.Total != c.Candidates || len(page.Items) != c.Candidates {
			t.Fatal("source fixture did not preserve distinct candidate identities", page.Total, len(page.Items), err)
		}
		for _, candidate := range page.Items {
			script.texts[candidate.ID] = candidate.Preferred.Abstract
			script.result.SavedAbstractRunes += utf8.RuneCountInString(candidate.Preferred.Abstract)
		}
		script.result.SavedCandidates = len(page.Items)
	}
	before := databaseCapacitySnapshots(t, app)
	input := "capacity-probe：仅按需核读保存材料；摘要不是全文或科学批准。" + strings.Repeat("界", c.InputRunes)
	snapshot := waitCapacityChat(t, app, entry.ConversationID, profile.ID, input)
	script.mu.Lock()
	result := script.result
	result.ScriptFinished = script.done
	result.PhaseAtStop = script.phase
	failure := script.failure
	boundary := script.boundary
	script.mu.Unlock()
	result.RunStatus = snapshot.Run.Status
	result.ProviderInputTokens = snapshot.Run.InputTokens
	if c.SyntheticUsage && result.ModelHTTP > 0 && result.ProviderInputTokens <= 0 {
		t.Error("synthetic usage did not reach original accounting/anchor path")
	}
	result.ErrorCode = snapshot.Run.ErrorCode
	result.ErrorMessage = snapshot.Run.ErrorMessage
	result.ErrorDetails = snapshot.Run.ErrorDetails
	result.EffectiveBudget = snapshot.Run.ContextBudgetTokens
	result.AutoCompactLimit = snapshot.Run.AutoCompactTokenLimit
	result.SourceHTTP = int(sourceHTTP.Load())
	if snapshot.Run.ContextWindowTokens != c.Window || result.EffectiveBudget != c.Window*95/100 {
		t.Error("window silently changed", snapshot.Run.ContextWindowTokens, result.EffectiveBudget)
	}
	if failure != "" {
		result.Outcome = "fixture_or_visibility_failure"
		t.Error(failure)
	} else if boundary == "compaction_continued" {
		result.Outcome = "compaction_continued"
		result.Boundary = boundary
	} else if snapshot.Run.Status == chat.RunCompleted && result.ScriptFinished {
		result.Outcome = "selected_work_completed"
	} else if snapshot.Run.Status == chat.RunFailed && snapshot.Run.ErrorCode == "CONTEXT_BUILD_FAILED" {
		result.Outcome = "capacity_stopped"
	} else {
		result.Outcome = "unexpected_termination"
		t.Error("unexpected terminal state", snapshot.Run.Status, snapshot.Run.ErrorCode, snapshot.Run.ErrorDetails)
	}
	if c.SyntheticUsage && c.Mode != "plain" && result.Outcome != "selected_work_completed" {
		t.Error("known synthetic usage-positive control did not complete; not a real model acceptance", result.Outcome)
	}
	if !c.SyntheticUsage && result.ProviderInputTokens != 0 {
		t.Error("no-usage survey unexpectedly received a usage anchor")
	}
	for _, call := range snapshot.ToolCalls {
		if call.Status == tool.CallCompleted {
			result.CompletedTools++
		}
		if call.ToolName == workflow.ResearchDiscoveryReadTool && call.Status == tool.CallCompleted && call.Result != nil && call.Result.Status == tool.ResultSuccess {
			var args struct {
				CandidateID string `json:"candidateId"`
				QueryID     string `json:"queryId"`
			}
			_ = json.Unmarshal(call.Arguments, &args)
			if args.CandidateID != "" {
				if _, err := workflow.ParseResearchWorkingAbstractPage(call.Result.Structured); err != nil {
					t.Error("stored field result corrupt", err)
				}
				result.CompletedFieldResults++
			} else if args.QueryID != "" {
				var page struct {
					Candidates []json.RawMessage `json:"candidates"`
				}
				if json.Unmarshal(call.Result.Structured, &page) != nil {
					t.Error("stored index result corrupt")
				}
				result.CompletedIndexResults++
				result.StoredIndexCandidates += len(page.Candidates)
			}
		}
	}
	if result.AbstractPagesShown > result.CompletedFieldResults {
		t.Error("model saw invented source pages")
	}
	if !reflect.DeepEqual(before, databaseCapacitySnapshots(t, app)) {
		t.Error("capacity fitting mutated original saved snapshots")
	}
	note, err := app.WorkflowFacade.GetResearchWorkingState(project.ID, entry.ResearchTaskID)
	if err != nil {
		t.Fatal(err)
	}
	result.NotePublished = note != nil
	if note != nil && c.Mode != "linked" {
		t.Error("reading created an unsolicited working note")
	}
	if c.Mode == "linked" && result.Outcome == "selected_work_completed" {
		if note == nil || note.Version != 1 {
			t.Fatal("successful linked control lost note")
		}
		sources, err := app.WorkflowFacade.GetResearchWorkingStateProvenance(project.ID, entry.ResearchTaskID, 1)
		if err != nil || len(sources.Fields) != 4 || len(sources.Fields[1].Basis) != 2 {
			t.Fatal("successful linked control lost explicit sources", err)
		}
		for _, b := range sources.Fields[1].Basis {
			if _, err := app.WorkflowFacade.GetResearchWorkingStateBasis(project.ID, entry.ResearchTaskID, 1, "rationale", b.ReadCallID); err != nil {
				t.Error("original linked page cannot reopen", err)
			}
		}
	}
	current, err := app.WorkflowFacade.GetCurrentResearchTaskRun(project.ID, entry.ResearchTaskID)
	if err == nil || current.Run.ID != "" || len(current.AIExecutions) != 0 {
		t.Error("capacity fitting created/changed/executed a route", current.Run.ID, err)
	}
	for _, table := range []string{"attachments", "knowledge_documents", "artifacts"} {
		var n int
		if err := app.store.DB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Error("capacity test promoted source to material/evidence", table, n, err)
		}
	}
	messages, err := app.ConversationFacade.ListMessages(entry.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	preserved := false
	for _, m := range messages {
		if m.ID == snapshot.Run.UserMessageID {
			var text string
			for _, part := range m.Parts {
				if part.Type == "text" {
					text += part.Text
				}
			}
			preserved = text == input
		}
	}
	if !preserved {
		t.Error("capacity stop/fitting changed original user input")
	}
	wantSource := 0
	if c.Candidates > 0 {
		wantSource = 1
	}
	if result.SourceHTTP != wantSource {
		t.Error("saved reads refetched sources", result.SourceHTTP, wantSource)
	}
	if (c.Name == "plain_32000" || c.Name == "plain_36000" || c.Name == "index_3_32000" || c.Name == "abstract_128_32000" || c.Name == "linked_32000") && result.Outcome != "selected_work_completed" {
		t.Error("minimum 32k operating control did not complete", result.Outcome)
	}
	emitCapacityResult(t, result)
	return result
}

// Follow-up controls distinguish shared chat admission overhead from research
// scope. Same profile/window/input/full-access mode; no bound research task.
func TestResearchCapacityOrdinaryConversationControls(t *testing.T) {
	for _, window := range []int{24000, 28000, 32000} {
		t.Run(fmt.Sprint(window), func(t *testing.T) {
			c := researchCapacityCase{Name: fmt.Sprintf("ordinary_%d", window), Mode: "ordinary", Window: window}
			script := &capacityScript{test: c, result: capacityResult{Case: c, Requests: []capacityRequest{}}, phase: "start"}
			server := httptest.NewServer(script)
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
			project, err := app.ProjectFacade.CreateProject(wails.CreateProjectRequest{Name: "普通对话容量对照"})
			if err != nil {
				t.Fatal(err)
			}
			conv, err := app.ConversationFacade.CreateConversation(wails.CreateConversationRequest{ProjectID: project.ID, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := app.ConversationFacade.SetPermissionMode(conv.ID, "full_access"); err != nil {
				t.Fatal(err)
			}
			snapshot := waitCapacityChat(t, app, conv.ID, profile.ID, "capacity-probe：仅按需核读保存材料；摘要不是全文或科学批准。")
			script.mu.Lock()
			result := script.result
			result.ScriptFinished = script.done
			result.PhaseAtStop = script.phase
			failure := script.failure
			script.mu.Unlock()
			result.RunStatus = snapshot.Run.Status
			result.ErrorCode = snapshot.Run.ErrorCode
			result.ErrorDetails = snapshot.Run.ErrorDetails
			result.EffectiveBudget = snapshot.Run.ContextBudgetTokens
			result.AutoCompactLimit = snapshot.Run.AutoCompactTokenLimit
			result.ProviderInputTokens = snapshot.Run.InputTokens
			if failure != "" {
				t.Error(failure)
			}
			if snapshot.Run.Status == chat.RunCompleted && result.ScriptFinished {
				result.Outcome = "selected_work_completed"
			} else if snapshot.Run.Status == chat.RunFailed && snapshot.Run.ErrorCode == "CONTEXT_BUILD_FAILED" {
				result.Outcome = "capacity_stopped"
			} else {
				result.Outcome = "unexpected_termination"
				t.Error("unexpected ordinary control result", snapshot.Run)
			}
			if snapshot.Run.ContextWindowTokens != window || result.EffectiveBudget != window*95/100 {
				t.Error("ordinary control changed budget")
			}
			var tasks int
			if err := app.store.DB().QueryRow(`SELECT COUNT(*) FROM research_tasks`).Scan(&tasks); err != nil || tasks != 0 {
				t.Error("ordinary conversation acquired a research task", tasks, err)
			}
			if window == 32000 && result.Outcome != "selected_work_completed" {
				t.Error("ordinary 32k baseline failed")
			}
			emitCapacityResult(t, result)
		})
	}
}

func emitCapacityResult(t *testing.T, result capacityResult) {
	t.Helper()
	encoded, _ := json.Marshal(result)
	t.Log("CAPACITY_RESULT " + string(encoded))
	if dir := os.Getenv("SCIAIDE_CAPACITY_OUTPUT"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		pretty, _ := json.MarshalIndent(result, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, result.Case.Name+".json"), append(pretty, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func databaseCapacitySnapshots(t *testing.T, app *Application) map[string]string {
	t.Helper()
	rows, err := app.store.DB().Query(`SELECT query_id||':'||candidate_id,snapshot_json FROM research_query_candidates ORDER BY query_id,candidate_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			t.Fatal(err)
		}
		out[key] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

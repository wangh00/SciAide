package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/research/connectors"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wails "github.com/wangh00/SciAide/internal/transport/wails"
)

// V0 uses the real Application/Chat/Agent/Tool/SQLite chain with controlled
// HTTP. It proves task ownership and adaptive action composition, not science.
func TestResearchTaskVerticalSliceReadsCounterevidenceWithoutWorkflow(t *testing.T) {
	const firstAbstract = "In one synthetic delayed-test setting, the spaced group retained more material than the concentrated group. The result is limited to this sample and does not establish a general effect."
	const secondAbstract = "In a second synthetic setting, spaced practice did not improve retention and showed a negative transfer result relative to concentrated practice. This counterevidence contradicts an unqualified benefit claim."
	const firstNext = "读取另一份已保存摘要，核对结果方向是否一致。"
	const changedNext = "停止扩大结论，仅输出两份摘要的有限对照。"
	type toolEnvelope struct {
		Status     string          `json:"status"`
		Text       string          `json:"text"`
		ToolCallID string          `json:"toolCallId"`
		Structured json.RawMessage `json:"structured"`
	}
	type candidate struct {
		CandidateID    string `json:"candidateId"`
		SourceRecordID string `json:"recordId"`
		Abstract       struct {
			Text    string `json:"text"`
			Omitted bool   `json:"omitted"`
		} `json:"abstract"`
	}
	var mu sync.Mutex
	requests := 0
	queryID := ""
	candidates := map[string]string{}
	readCalls := []string{}
	cancelStarted := make(chan struct{})
	var cancelOnce sync.Once
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "invalid controlled request", 400)
			return
		}
		contents := make([]string, len(request.Messages))
		latestUser := -1
		lastText := ""
		for i, message := range request.Messages {
			_ = json.Unmarshal(message.Content, &contents[i])
			if message.Role == "user" {
				latestUser, lastText = i, contents[i]
			}
		}
		if strings.Contains(lastText, "停止本次读取") {
			cancelOnce.Do(func() { close(cancelStarted) })
			<-r.Context().Done()
			return
		}
		all := strings.Join(contents, "\n")
		tools := map[string]bool{}
		for _, definition := range request.Tools {
			tools[definition.Function.Name] = true
		}
		if !tools["research_discovery_search"] || !tools["research_discovery_read"] || !tools["research_state_update"] || !tools["research_plan_prepare"] || tools["research_task_read"] || strings.Contains(all, "original planner/source history is missing") {
			t.Error("direct task received a fabricated planner contract or lacks exploration tools", tools)
			http.Error(w, "wrong direct task tools", 400)
			return
		}
		mu.Lock()
		requests++
		turn := requests
		mu.Unlock()
		fail := func(reason string) {
			t.Error(reason)
			http.Error(w, reason, 400)
		}
		latestTool := func() (toolEnvelope, error) {
			for i := len(request.Messages) - 1; i > latestUser; i-- {
				if request.Messages[i].Role != "tool" {
					continue
				}
				var envelope toolEnvelope
				raw := strings.TrimSpace(contents[i])
				raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(raw, "<untrusted_tool_result>"), "</untrusted_tool_result>"))
				if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
					return envelope, fmt.Errorf("decode tool result: %w raw=%s", err, contents[i])
				}
				return envelope, nil
			}
			return toolEnvelope{}, fmt.Errorf("missing tool result")
		}
		if strings.Contains(lastText, "跨任务读取") {
			envelope, readErr := latestTool()
			if readErr != nil {
				mu.Lock()
				q, candidateID := queryID, candidates["EJ000001"]
				mu.Unlock()
				writeResearchTaskVerticalSliceResponse(w, "research_discovery_read", map[string]any{"queryId": q, "candidateId": candidateID, "field": "abstract", "limit": 400}, "")
				return
			}
			if envelope.Status != "error" || strings.Contains(string(envelope.Structured), firstAbstract) || strings.Contains(envelope.Text, firstAbstract) {
				fail("cross-task read exposed another task's saved abstract")
				return
			}
			writeResearchTaskVerticalSliceResponse(w, "", nil, "其他任务的保存页不可读取，未获得任何摘要正文。")
			return
		}
		switch turn {
		case 1:
			writeResearchTaskVerticalSliceResponse(w, "research_discovery_search", map[string]any{"query": "synthetic spaced practice counterevidence", "sourceIds": []string{"eric"}, "limit": 2}, "")
		case 2:
			envelope, err := latestTool()
			var page struct {
				QueryID    string      `json:"queryId"`
				Candidates []candidate `json:"candidates"`
			}
			if err != nil || envelope.Status != "success" || json.Unmarshal(envelope.Structured, &page) != nil || page.QueryID == "" || len(page.Candidates) != 2 {
				fail(fmt.Sprintf("search did not return two addressable saved candidates: err=%v status=%s payload=%s", err, envelope.Status, envelope.Structured))
				return
			}
			local := map[string]string{}
			for _, item := range page.Candidates {
				if !item.Abstract.Omitted || item.Abstract.Text != "" {
					fail("long abstract was pinned into the navigation response")
					return
				}
				local[item.SourceRecordID] = item.CandidateID
			}
			if local["EJ000001"] == "" || local["EJ000002"] == "" {
				fail("candidate source identities are missing")
				return
			}
			mu.Lock()
			queryID, candidates = page.QueryID, local
			mu.Unlock()
			writeResearchTaskVerticalSliceResponse(w, "research_discovery_read", map[string]any{"queryId": page.QueryID, "candidateId": local["EJ000001"], "field": "abstract", "limit": 400}, "")
		case 3:
			envelope, err := latestTool()
			var page struct {
				Content    string `json:"content"`
				NextOffset int    `json:"nextOffset"`
				Available  bool   `json:"available"`
			}
			if err != nil || json.Unmarshal(envelope.Structured, &page) != nil || !page.Available || page.Content != firstAbstract || page.NextOffset != -1 {
				fail("first exact abstract page was not read")
				return
			}
			mu.Lock()
			readCalls = append(readCalls, envelope.ToolCallID)
			mu.Unlock()
			writeResearchTaskVerticalSliceResponse(w, "research_state_update", map[string]any{
				"expectedVersion": 0, "currentFocus": "核对 spaced practice 的延迟保持证据", "rationale": "第一份已读摘要只报告单一合成场景中的正向结果。",
				"nextStep": firstNext, "unknowns": []string{"另一份摘要尚未核读", "全文未读"},
				"basis": map[string][]string{"currentFocus": {envelope.ToolCallID}, "rationale": {envelope.ToolCallID}},
			}, "")
		case 4:
			envelope, err := latestTool()
			mu.Lock()
			q, second := queryID, candidates["EJ000002"]
			mu.Unlock()
			if err != nil || envelope.Status != "success" || !strings.Contains(string(envelope.Structured), "pending_tool_completion") || q == "" || second == "" {
				fail(fmt.Sprintf("first observation was not completed before the next action: err=%v status=%s text=%s payload=%s q=%s second=%s", err, envelope.Status, envelope.Text, envelope.Structured, q, second))
				return
			}
			writeResearchTaskVerticalSliceResponse(w, "research_discovery_read", map[string]any{"queryId": q, "candidateId": second, "field": "abstract", "limit": 400}, "")
		case 5:
			envelope, err := latestTool()
			var page struct {
				Content    string `json:"content"`
				NextOffset int    `json:"nextOffset"`
			}
			if err != nil || json.Unmarshal(envelope.Structured, &page) != nil || page.Content != secondAbstract || page.NextOffset != -1 {
				fail("counterevidence was not actually read")
				return
			}
			mu.Lock()
			readCalls = append(readCalls, envelope.ToolCallID)
			firstRead := readCalls[0]
			mu.Unlock()
			writeResearchTaskVerticalSliceResponse(w, "research_state_update", map[string]any{
				"expectedVersion": 1, "currentFocus": "比较反证出现的任务与条件差异", "rationale": "第二份已读摘要与第一份方向相反，不能保留无条件正向判断。",
				"nextStep": changedNext, "unknowns": []string{"全文未读", "反向结果的原因尚未核实"},
				"basis": map[string][]string{"currentFocus": {envelope.ToolCallID}, "rationale": {firstRead, envelope.ToolCallID}, "nextStep": {envelope.ToolCallID}},
			}, "")
		case 6:
			envelope, err := latestTool()
			if err != nil || envelope.Status != "success" || !strings.Contains(string(envelope.Structured), "pending_tool_completion") {
				fail("changed plan was not persisted before delivery")
				return
			}
			writeResearchTaskVerticalSliceResponse(w, "", nil, "两份已读摘要方向相反；这里只作有限对照，不作总体结论，也未执行正式路线。")
		default:
			fail("adaptive slice repeated completed work")
		}
	}))
	defer model.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/eric/" || !strings.Contains(r.URL.Query().Get("search"), "counterevidence") {
			t.Error("unexpected scholarly source request", r.URL.Path)
			http.Error(w, "wrong source request", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"numFound": 2, "docs": []any{
			map[string]any{"id": "EJ000001", "title": "Synthetic positive abstract", "description": firstAbstract, "publicationdateyear": 2025, "e_fulltextauth": 0},
			map[string]any{"id": "EJ000002", "title": "Synthetic counterevidence abstract", "description": secondAbstract, "publicationdateyear": 2025, "e_fulltextauth": 0},
		}}})
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
	project, err := app.ProjectFacade.CreateProject(wails.CreateProjectRequest{Name: "目标架构V0合成竖切"})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := app.WorkflowFacade.StartResearchDiscussion(workflow.StartResearchDiscussionCommand{
		ProjectID: project.ID, ResearchIdea: "比较两份合成摘要，遇到反证就调整下一步，不生成路线。",
		ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: "full_access",
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ResearchTaskID == "" || entry.ConversationID == "" || entry.ResearchTaskID == entry.ConversationID {
		t.Fatal("direct task/conversation identities are invalid", entry)
	}
	repository := sqlite.NewWorkflowRuntimeRepository(app.store.DB())
	discussion, found, err := repository.ResearchDiscussionForConversation(context.Background(), entry.ConversationID)
	if err != nil || !found || !discussion.CanExplore || discussion.SourceWorkflowRunID != "" || discussion.ResearchTaskID != entry.ResearchTaskID || discussion.PrimaryConversationID != entry.ConversationID {
		t.Fatal("direct discussion authority is not task-owned", discussion, found, err)
	}
	var workflowRuns int
	if err := app.store.DB().QueryRow(`SELECT count(*) FROM workflow_runs WHERE research_task_id=?`, entry.ResearchTaskID).Scan(&workflowRuns); err != nil || workflowRuns != 0 {
		t.Fatal("direct exploration created a compatibility Workflow", workflowRuns, err)
	}
	run := startResearchAssistantFixtureChat(t, app, entry.ConversationID, profile.ID, "读取已保存的两份合成摘要，遇反证调整下一步，只做有限对照。")
	state, err := app.WorkflowFacade.GetResearchWorkingState(project.ID, entry.ResearchTaskID)
	if err != nil || state == nil || state.Version != 2 || state.NextStep != changedNext || state.SourceChatRunID != run.ID {
		t.Fatal("counterevidence did not change the durable next action", state, err)
	}
	mu.Lock()
	reads := append([]string(nil), readCalls...)
	mu.Unlock()
	if len(reads) != 2 || run.ModelTurns != 6 || transport.calls.Load() != 1 {
		t.Fatal("unexpected reads/model/source consumption", len(reads), run.ModelTurns, transport.calls.Load())
	}
	basisA, err := app.WorkflowFacade.GetResearchWorkingStateBasis(project.ID, entry.ResearchTaskID, 2, "rationale", reads[0])
	if err != nil || basisA.Content != firstAbstract {
		t.Fatal("first observation lost its exact source page", basisA, err)
	}
	basisB, err := app.WorkflowFacade.GetResearchWorkingStateBasis(project.ID, entry.ResearchTaskID, 2, "rationale", reads[1])
	if err != nil || basisB.Content != secondAbstract {
		t.Fatal("counterevidence lost its exact source page", basisB, err)
	}
	var preservedNext string
	if err := app.store.DB().QueryRow(`SELECT next_step FROM research_working_states WHERE project_id=? AND research_task_id=? AND version=1`, project.ID, entry.ResearchTaskID).Scan(&preservedNext); err != nil || preservedNext != firstNext {
		t.Fatal("the earlier plan version was overwritten", preservedNext, err)
	}

	messageID, _ := id.New()
	cancelRun, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: entry.ConversationID, ModelProfileID: profile.ID, ModelID: workflowAITestModelID, ClientMessageID: messageID, Text: "停止本次读取"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("controlled cancellation request did not start")
	}
	if err := app.ChatFacade.CancelRun(cancelRun.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		snapshot, snapshotErr := app.ChatFacade.GetRunSnapshot(cancelRun.ID)
		if snapshotErr != nil {
			t.Fatal(snapshotErr)
		}
		if snapshot.Run.Status == chat.RunCancelled {
			break
		}
		if time.Now().After(deadline) || snapshot.Run.Status == chat.RunFailed || snapshot.Run.Status == chat.RunCompleted {
			t.Fatal("controlled request did not cancel", snapshot.Run.Status, snapshot.Run.ErrorCode)
		}
		time.Sleep(20 * time.Millisecond)
	}
	afterCancel, err := app.WorkflowFacade.GetResearchWorkingState(project.ID, entry.ResearchTaskID)
	if err != nil || !reflect.DeepEqual(*state, *afterCancel) {
		t.Fatal("cancellation changed the published research record", afterCancel, err)
	}

	other, err := app.WorkflowFacade.StartResearchDiscussion(workflow.StartResearchDiscussionCommand{
		ProjectID: project.ID, ResearchIdea: "另一项直接任务", ModelProfileID: profile.ID, ModelID: workflowAITestModelID,
	})
	if err != nil {
		t.Fatal(err)
	}
	crossRun := startResearchAssistantFixtureChat(t, app, other.ConversationID, profile.ID, "跨任务读取第一份任务保存的摘要")
	crossSnapshot, err := app.ChatFacade.GetRunSnapshot(crossRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	crossRejected := false
	for _, call := range crossSnapshot.ToolCalls {
		if call.ToolName == workflow.ResearchDiscoveryReadTool && call.Result != nil && call.Result.Status == "error" && !strings.Contains(string(call.Result.Structured), firstAbstract) {
			crossRejected = true
		}
	}
	if !crossRejected || crossRun.ModelTurns != 2 {
		t.Fatal("direct task accepted a foreign discovery page", crossSnapshot.ToolCalls, crossRun.ModelTurns)
	}
	if err := app.store.DB().QueryRow(`SELECT count(*) FROM workflow_runs WHERE research_task_id=?`, other.ResearchTaskID).Scan(&workflowRuns); err != nil || workflowRuns != 0 {
		t.Fatal("cross-task rejection created a Workflow", workflowRuns, err)
	}
	afterCross, err := app.WorkflowFacade.GetResearchWorkingState(project.ID, entry.ResearchTaskID)
	if err != nil || !reflect.DeepEqual(*state, *afterCross) {
		t.Fatal("cross-task rejection changed the original work record", afterCross, err)
	}

	beforeRestart := *afterCross
	mu.Lock()
	requestsBeforeRestart := requests
	mu.Unlock()
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	reopened, err := app.WorkflowFacade.GetResearchWorkingState(project.ID, entry.ResearchTaskID)
	if err != nil || reopened == nil || !reflect.DeepEqual(beforeRestart, *reopened) {
		t.Fatal("restart lost direct-task research records", reopened, err)
	}
	discussion, found, err = sqlite.NewWorkflowRuntimeRepository(app.store.DB()).ResearchDiscussionForConversation(context.Background(), entry.ConversationID)
	if err != nil || !found || !discussion.CanExplore || discussion.SourceWorkflowRunID != "" {
		t.Fatal("restart fabricated a planner or revoked direct discussion", discussion, found, err)
	}
	if err := app.store.DB().QueryRow(`SELECT count(*) FROM workflow_runs WHERE research_task_id=?`, entry.ResearchTaskID).Scan(&workflowRuns); err != nil || workflowRuns != 0 {
		t.Fatal("restart created a Workflow", workflowRuns, err)
	}
	mu.Lock()
	totalRequests := requests
	mu.Unlock()
	if totalRequests != requestsBeforeRestart || transport.calls.Load() != 1 {
		t.Fatal("restart replayed model or source work", totalRequests, requestsBeforeRestart, transport.calls.Load())
	}
}

func writeResearchTaskVerticalSliceResponse(w http.ResponseWriter, name string, arguments map[string]any, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	delta := map[string]any{"content": text}
	finish := "stop"
	if name != "" {
		encoded, _ := json.Marshal(arguments)
		providerID, _ := id.New()
		delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "controlled-v0-" + providerID, "function": map[string]any{"name": name, "arguments": string(encoded)}}}}
		finish = "tool_calls"
	}
	chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}})
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
}

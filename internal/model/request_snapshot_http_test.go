package model_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/anthropic"
	"github.com/wangh00/SciAide/internal/model/openai"
	"github.com/wangh00/SciAide/internal/model/responses"
	"github.com/wangh00/SciAide/internal/modelcap"
	"github.com/wangh00/SciAide/internal/modelutil"
)

func writeSnapshotHTTPReply(w http.ResponseWriter, protocol modelcap.APIProtocol) {
	w.Header().Set("Content-Type", "text/event-stream")
	switch protocol {
	case modelcap.ProtocolOpenAIChat:
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":17,\"completion_tokens\":2,\"total_tokens\":19}}\n\ndata: [DONE]\n\n")
	case modelcap.ProtocolOpenAIResponses:
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"done\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-fixture\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":17,\"output_tokens\":2}}}\n\n")
	case modelcap.ProtocolAnthropic:
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-fixture\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":17,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}
}

func TestRequestSnapshotsDoNotChangeThreeProtocolWireOrStreams(t *testing.T) {
	for _, protocol := range []modelcap.APIProtocol{modelcap.ProtocolOpenAIChat, modelcap.ProtocolOpenAIResponses, modelcap.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) {
			var mu sync.Mutex
			var bodies []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				bodies = append(bodies, string(body))
				mu.Unlock()
				if r.Header.Get("Authorization") != "Bearer PRIVATE_API_KEY" && r.Header.Get("X-Api-Key") != "PRIVATE_API_KEY" {
					t.Error("authentication changed")
				}
				if r.Header.Get("X-Private") != "PRIVATE_HEADER" {
					t.Error("custom header changed")
				}
				writeSnapshotHTTPReply(w, protocol)
			}))
			defer server.Close()
			profile := modelprofile.Profile{BaseURL: server.URL + "/v1", ModelID: "fixture-model", APIProtocol: protocol, TimeoutSeconds: 5, CustomHeaders: map[string]string{"X-Private": "PRIVATE_HEADER"}}
			request := model.ChatRequest{PromptCacheKey: "PRIVATE_CACHE", Tools: []model.ToolDefinition{{Name: "builtin.workspace.list", Description: "PRIVATE_DESCRIPTION", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"additionalProperties":false}`)}}, Messages: []model.Message{
				{Role: model.RoleSystem, Content: "PRIVATE_SYSTEM 中文 🧪 \"quotes\""},
				{Role: model.RoleUser, Content: "PRIVATE_USER"},
				{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "call-old", Name: "builtin.workspace.list", Arguments: json.RawMessage(`{"path":"PRIVATE_PATH"}`)}}},
				{Role: model.RoleTool, ToolCallID: "call-old", Content: "PRIVATE_TOOL_RESULT"},
			}}
			var snapshots []modelutil.DebugRequestSnapshot
			observer, err := modelutil.NewRequestSnapshotObserver(func(v modelutil.DebugRequestSnapshot) { snapshots = append(snapshots, v) })
			if err != nil {
				t.Fatal(err)
			}
			var texts []string
			for _, enabled := range []bool{false, true} {
				var client model.ChatModel
				switch protocol {
				case modelcap.ProtocolOpenAIChat:
					client = openai.New(profile, []byte("PRIVATE_API_KEY"))
				case modelcap.ProtocolOpenAIResponses:
					client = responses.New(profile, []byte("PRIVATE_API_KEY"))
				case modelcap.ProtocolAnthropic:
					client = anthropic.New(profile, []byte("PRIVATE_API_KEY"))
				}
				control := client.(interface {
					SetDebugRequestSnapshotObserver(*modelutil.RequestSnapshotObserver)
					SetRequestGate(func(context.Context) error)
				})
				admissions := 0
				control.SetRequestGate(func(context.Context) error { admissions++; return nil })
				if enabled {
					control.SetDebugRequestSnapshotObserver(observer)
				}
				ctx := model.WithRequestTraceContext(context.Background(), model.RequestTraceContext{RunID: "12345678-1234-4234-8234-123456789abc", TurnIndex: 1, Kind: "conversation", WindowTokens: 64000, EffectiveTokens: 60800, AutoCompactTokens: 57600, WindowSource: "manual"})
				stream, e := client.Stream(ctx, request)
				if e != nil {
					t.Fatal(e)
				}
				text := ""
				for {
					event, e := stream.Recv()
					if e == io.EOF {
						break
					}
					if e != nil {
						t.Fatal(e)
					}
					if event.Type == model.EventTextDelta {
						text += event.Text
					}
				}
				_ = stream.Close()
				texts = append(texts, text)
				if admissions != 1 {
					t.Fatal("observation added HTTP", admissions)
				}
			}
			mu.Lock()
			wire := append([]string(nil), bodies...)
			mu.Unlock()
			if len(wire) != 2 || wire[0] != wire[1] || len(snapshots) != 1 || texts[0] != "done" || texts[1] != texts[0] {
				t.Fatal("on/off changed final serialized request or stream")
			}
			v := snapshots[0]
			if v.BodyBytes != len(wire[1]) || v.DeclaredBodyBytes != int64(len(wire[1])) || v.ToolCount != 1 || v.Inspection != "complete" || v.HTTPStatus != 200 || v.ConfiguredBudget == nil || v.ConfiguredBudget.EffectiveTokens != 60800 {
				t.Fatal("snapshot did not measure the actual adapter wire", v)
			}
			encoded, _ := json.Marshal(v)
			if strings.Contains(string(encoded), "PRIVATE_") || strings.Contains(string(encoded), server.URL) {
				t.Fatal("capture leaked source/header/address")
			}
			t.Logf("%s: off/on HTTP bodies exactly equal (%d bytes); same stream, 1 admitted HTTP each; actual tools=%d, input items=%d", protocol, len(wire[1]), v.ToolCount, v.InputCount)
		})
	}
}

func TestRequestSnapshotsObserveOriginalNegotiationAndRedirectsWithoutExtraHTTP(t *testing.T) {
	var events []modelutil.DebugRequestSnapshot
	observer, _ := modelutil.NewRequestSnapshotObserver(func(v modelutil.DebugRequestSnapshot) { events = append(events, v) })
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls++
		if calls == 1 {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":{"message":"Unknown parameter: prompt_cache_key"}}`)
			return
		}
		writeSnapshotHTTPReply(w, modelcap.ProtocolOpenAIResponses)
	}))
	defer server.Close()
	client := responses.New(modelprofile.Profile{BaseURL: server.URL + "/v1", ModelID: "fixture", TimeoutSeconds: 5}, nil)
	admissions := 0
	client.SetDebugRequestSnapshotObserver(observer)
	client.SetRequestGate(func(context.Context) error { admissions++; return nil })
	ctx := model.WithRequestTraceContext(context.Background(), model.RequestTraceContext{RunID: "run-fixture", TurnIndex: 1, Kind: "conversation"})
	stream, err := client.Stream(ctx, model.ChatRequest{PromptCacheKey: "PRIVATE_CACHE", Messages: []model.Message{{Role: model.RoleUser, Content: "PRIVATE_TEXT"}}})
	if err != nil {
		t.Fatal(err)
	}
	for {
		_, e := stream.Recv()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
	}
	_ = stream.Close()
	if calls != 2 || admissions != 2 || len(events) != 2 || events[0].HTTPStatus != 400 || events[1].HTTPStatus != 200 || events[1].Delta == nil || !events[1].Delta.SettingsChanged || events[1].Delta.InputCountDelta != 0 {
		t.Fatal("original parameter negotiation not represented one-for-one", calls, admissions, len(events))
	}
	// Each native HTTP redirect is still gated and observed, including the
	// standard POST->GET conversion. No response body is consumed by capture.
	events = nil
	calls = 0
	admissions = 0
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls++
		if r.URL.Path == "/first" {
			http.Redirect(w, r, "/last", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "original-body")
	}))
	defer redirect.Close()
	httpClient := modelutil.WithRequestGate(modelutil.WithRequestSnapshots(&http.Client{}, modelcap.ProtocolOpenAIResponses, observer), func(context.Context) error { admissions++; return nil })
	req, _ := http.NewRequestWithContext(ctx, "POST", redirect.URL+"/first", strings.NewReader(`{"input":[]}`))
	response, err := httpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if calls != 2 || admissions != 2 || len(events) != 2 || events[0].HTTPStatus != 302 || events[1].HTTPStatus != 200 || events[1].Method != "GET" || events[1].Inspection != "no_body" || string(body) != "original-body" {
		t.Fatal("redirect admission, body or observation changed", calls, admissions, len(events))
	}
}

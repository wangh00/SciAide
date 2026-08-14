package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/modelcap"
	"github.com/wangh00/SciAide/internal/modelutil"
)

func TestStreamMapsMessagesToolsThinkingAndUsage(t *testing.T) {
	qualified := "builtin.workspace.read_text"
	alias := modelutil.ProviderToolName(qualified)
	maxTokens := 4096
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "secret" || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Fatalf("headers = %#v", r.Header)
		}
		var body payload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.System != "system" || body.Thinking == nil || body.Thinking.Type != "enabled" || body.Thinking.BudgetTokens != 2048 || body.MaxTokens != maxTokens {
			t.Fatalf("body = %#v", body)
		}
		if len(body.Tools) != 1 || body.Tools[0].Name != alias {
			t.Fatalf("tools = %#v", body.Tools)
		}
		if len(body.Messages) != 3 || body.Messages[1].Content[0].Type != "tool_use" || body.Messages[1].Content[0].ID != "old" || body.Messages[2].Content[0].Type != "tool_result" || body.Messages[2].Content[0].ToolUseID != "old" || !strings.Contains(body.Messages[2].Content[0].Content, "<untrusted_tool_result>") {
			t.Fatalf("messages = %#v", body.Messages)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5,\"cache_read_input_tokens\":2,\"cache_creation_input_tokens\":1}}}\n\n")
		io.WriteString(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"inspect workspace\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"signed-state\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		io.WriteString(w, "data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"redacted_thinking\",\"data\":\"opaque-state\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"content_block_stop\",\"index\":1}\n\n")
		io.WriteString(w, fmt.Sprintf("data: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":%q,\"input\":{}}}\n\n", alias))
		io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"paper.md\\\"}\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"content_block_stop\",\"index\":2}\n\n")
		io.WriteString(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":3}}\n\n")
		io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL + "/v1", ModelID: "claude-sonnet-4-5", TimeoutSeconds: 5, MaxOutputTokens: &maxTokens}, []byte("secret"))
	stream, err := client.Stream(context.Background(), model.ChatRequest{
		ResolvedReasoningLevel: modelcap.ReasoningHigh,
		Tools:                  []model.ToolDefinition{{Name: qualified, InputSchema: json.RawMessage(`{"type":"object"}`)}},
		Messages:               []model.Message{{Role: model.RoleSystem, Content: "system"}, {Role: model.RoleUser, Content: "read"}, {Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "old", Name: qualified, Arguments: json.RawMessage(`{}`)}}}, {Role: model.RoleTool, ToolCallID: "old", Content: "result"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	thinking, err := stream.Recv()
	if err != nil || thinking.ProviderItem == nil || thinking.ProviderItem.Type != "thinking" || !strings.Contains(string(thinking.ProviderItem.Payload), `"signature":"signed-state"`) {
		t.Fatalf("thinking = %#v, %v", thinking, err)
	}
	redacted, err := stream.Recv()
	if err != nil || redacted.ProviderItem == nil || redacted.ProviderItem.Type != "redacted_thinking" || !strings.Contains(string(redacted.ProviderItem.Payload), `"data":"opaque-state"`) {
		t.Fatalf("redacted thinking = %#v, %v", redacted, err)
	}
	call, err := stream.Recv()
	if err != nil || call.ToolCall == nil || call.ToolCall.ID != "call_1" || call.ToolCall.Name != qualified || string(call.ToolCall.Arguments) != `{"path":"paper.md"}` {
		t.Fatalf("call = %#v, %v", call, err)
	}
	toolItem, err := stream.Recv()
	if err != nil || toolItem.ProviderItem == nil || toolItem.ProviderItem.Type != "tool_use" || toolItem.ProviderItem.CallID != "call_1" || toolItem.ProviderItem.Ordinal != 2 {
		t.Fatalf("tool item = %#v, %v", toolItem, err)
	}
	usage, err := stream.Recv()
	if err != nil || usage.Usage == nil || usage.Usage.InputTokens != 8 || usage.Usage.FreshInputTokens != 5 || usage.Usage.CachedInputTokens != 2 || usage.Usage.CacheWriteTokens != 1 || usage.Usage.OutputTokens != 3 || !usage.Usage.CacheDetailsReported {
		t.Fatalf("usage = %#v, %v", usage, err)
	}
	done, err := stream.Recv()
	if err != nil || done.FinishReason != "tool_calls" {
		t.Fatalf("done = %#v, %v", done, err)
	}
}

func TestThinkingBudgetStaysBelowMaxTokens(t *testing.T) {
	if got := thinkingBudget(modelcap.ReasoningMax, 2048); got != 1740 {
		t.Fatalf("budget = %d", got)
	}
	if got := thinkingBudget(modelcap.ReasoningLow, 1500); got != 1024 {
		t.Fatalf("low budget = %d", got)
	}
}

func TestProviderTurnReplaysThinkingSignatureBeforeToolResult(t *testing.T) {
	qualified := "builtin.workspace.read_text"
	alias := modelutil.ProviderToolName(qualified)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body payload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Messages) != 3 {
			t.Fatalf("messages = %#v", body.Messages)
		}
		assistant := body.Messages[1]
		if assistant.Role != "assistant" || len(assistant.Content) != 2 || assistant.Content[0].Type != "thinking" || assistant.Content[0].Thinking != "inspect" || assistant.Content[0].Signature != "signed-state" {
			t.Fatalf("assistant replay = %#v", assistant)
		}
		if assistant.Content[1].Type != "tool_use" || assistant.Content[1].ID != "call_1" || assistant.Content[1].Name != alias || string(assistant.Content[1].Input) != `{"path":"paper.md"}` {
			t.Fatalf("tool replay = %#v", assistant.Content[1])
		}
		result := body.Messages[2]
		if result.Role != "user" || len(result.Content) != 1 || result.Content[0].Type != "tool_result" || result.Content[0].ToolUseID != "call_1" || !strings.Contains(result.Content[0].Content, "paper content") {
			t.Fatalf("result replay = %#v", result)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	stream, err := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "claude-sonnet-4-6", TimeoutSeconds: 5}, nil).Stream(context.Background(), model.ChatRequest{
		Messages: []model.Message{{Role: model.RoleUser, Content: "read"}},
		Tools:    []model.ToolDefinition{{Name: qualified, InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ProviderTurns: []model.ProviderTurn{{
			TurnIndex: 1,
			Protocol:  modelcap.ProtocolAnthropic,
			Items: []model.ProviderItem{
				{Ordinal: 0, Type: "thinking", Payload: json.RawMessage(`{"type":"thinking","thinking":"inspect","signature":"signed-state"}`)},
				{Ordinal: 1, Type: "tool_use", CallID: "call_1", Payload: json.RawMessage(fmt.Sprintf(`{"type":"tool_use","id":"call_1","name":%q,"input":{"path":"paper.md"}}`, alias))},
			},
			ToolResults: []model.Message{{Role: model.RoleTool, ToolCallID: "call_1", Content: "paper content"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if event, err := stream.Recv(); err != nil || event.Type != model.EventDone {
		t.Fatalf("done = %#v, %v", event, err)
	}
}

type reasoningRecorder struct{ result modelcap.ReasoningResult }

func (r *reasoningRecorder) RecordReasoningResult(_ context.Context, _, _ string, result modelcap.ReasoningResult) error {
	r.result = result
	return nil
}

func TestUnknownModelOptimisticallyUsesAdaptiveMax(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body payload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Thinking == nil || body.Thinking.Type != "adaptive" || body.Thinking.BudgetTokens != 0 || body.OutputConfig == nil || body.OutputConfig.Effort != "max" {
			t.Fatalf("adaptive body = %#v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	recorder := &reasoningRecorder{}
	stream, err := New(modelprofile.Profile{ID: "profile", BaseURL: server.URL, ModelID: "future-anthropic-compatible", TimeoutSeconds: 5}, nil, recorder).Stream(context.Background(), model.ChatRequest{RequestedReasoningLevel: modelcap.ReasoningMax, ResolvedReasoningLevel: modelcap.ReasoningMax})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	reporter, ok := stream.(model.ReasoningResolutionReporter)
	if !ok || reporter.ReasoningResolution().Resolved != modelcap.ReasoningMax {
		t.Fatalf("resolution = %#v", reporter)
	}
	if recorder.result.Resolved != modelcap.ReasoningMax || recorder.result.WireMode != string(thinkingAdaptive) {
		t.Fatalf("observation = %#v", recorder.result)
	}
}

func TestStreamRetriesWithoutRejectedThinkingControl(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body payload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if requests <= 2 {
			if body.Thinking == nil || (requests == 1 && body.Thinking.Type != "adaptive") || (requests == 2 && body.Thinking.Type != "enabled") {
				t.Fatalf("request %d thinking = %#v", requests, body.Thinking)
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"thinking is not supported by this model"}}`)
			return
		}
		if body.Thinking != nil {
			t.Fatalf("fallback thinking = %#v", body.Thinking)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	stream, err := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "custom", TimeoutSeconds: 5}, nil).Stream(context.Background(), model.ChatRequest{ResolvedReasoningLevel: modelcap.ReasoningMedium})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	if requests != 3 {
		t.Fatalf("requests = %d", requests)
	}
}

func TestClientReusesNegotiatedLegacyModeWithinRun(t *testing.T) {
	var modes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body payload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		modes = append(modes, body.Thinking.Type)
		if len(modes) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"thinking type adaptive is not supported"}}`)
			return
		}
		if body.Thinking.Type != "enabled" {
			t.Fatalf("thinking = %#v", body.Thinking)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "custom", TimeoutSeconds: 5}, nil)
	for range 2 {
		stream, err := client.Stream(context.Background(), model.ChatRequest{RequestedReasoningLevel: modelcap.ReasoningHigh, ResolvedReasoningLevel: modelcap.ReasoningHigh})
		if err != nil {
			t.Fatal(err)
		}
		_ = stream.Close()
	}
	if fmt.Sprint(modes) != "[adaptive enabled enabled]" {
		t.Fatalf("thinking modes = %v", modes)
	}
}

func TestStreamHonorsContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	stream, err := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil).Stream(ctx, model.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_, err = stream.Recv()
	if err == nil || ctx.Err() == nil {
		t.Fatalf("Recv() error = %v, context = %v", err, ctx.Err())
	}
}

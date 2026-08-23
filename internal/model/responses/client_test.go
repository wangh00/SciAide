package responses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/modelcap"
	"github.com/wangh00/SciAide/internal/modelutil"
)

func TestStreamMapsResponsesRequestToolsAndUsage(t *testing.T) {
	qualified := "builtin.workspace.read_text"
	alias := modelutil.ProviderToolName(qualified)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		var body payload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Reasoning == nil || body.Reasoning.Effort != "high" || body.Reasoning.Summary != "auto" {
			t.Fatalf("reasoning = %#v", body.Reasoning)
		}
		if body.Store || len(body.Include) != 1 || body.Include[0] != "reasoning.encrypted_content" {
			t.Fatalf("state controls = store:%v include:%v", body.Store, body.Include)
		}
		if body.PromptCacheKey != "conversation-1" {
			t.Fatalf("prompt cache key = %q", body.PromptCacheKey)
		}
		if body.Instructions != "system" || len(body.Tools) != 1 || strings.Contains(body.Tools[0].Name, ".") {
			t.Fatalf("body = %#v", body)
		}
		if len(body.Input) != 3 || body.Input[1].Type != "function_call" || body.Input[1].CallID != "old" || body.Input[2].Type != "function_call_output" || body.Input[2].CallID != "old" || !strings.Contains(body.Input[2].Output, "<untrusted_tool_result>") {
			t.Fatalf("input = %#v", body.Input)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"id\":\"rs_1\",\"summary\":[]}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"id\":\"rs_1\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"inspect\"}],\"encrypted_content\":\"opaque-reasoning\"}}\n\n")
		io.WriteString(w, fmt.Sprintf("data: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"id\":\"item_1\",\"call_id\":\"call_1\",\"name\":%q,\"arguments\":\"\"}}\n\n", alias))
		io.WriteString(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"item_1\",\"delta\":\"{\\\"path\\\":\\\"paper.md\\\"}\"}\n\n")
		io.WriteString(w, fmt.Sprintf("data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"id\":\"item_1\",\"call_id\":\"call_1\",\"name\":%q,\"arguments\":\"\"}}\n\n", alias))
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":3,\"input_tokens_details\":{\"cached_tokens\":4},\"output_tokens_details\":{\"reasoning_tokens\":2}}}}\n\n")
	}))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL + "/v1", ModelID: "fixture", TimeoutSeconds: 5}, []byte("secret"))
	stream, err := client.Stream(context.Background(), model.ChatRequest{
		PromptCacheKey:         "conversation-1",
		ResolvedReasoningLevel: modelcap.ReasoningHigh,
		Tools:                  []model.ToolDefinition{{Name: qualified, InputSchema: json.RawMessage(`{"type":"object"}`)}},
		Messages:               []model.Message{{Role: model.RoleSystem, Content: "system"}, {Role: model.RoleUser, Content: "read"}, {Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "old", Name: qualified, Arguments: json.RawMessage(`{}`)}}}, {Role: model.RoleTool, ToolCallID: "old", Content: "result"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	text, err := stream.Recv()
	if err != nil || text.Text != "hello" {
		t.Fatalf("text = %#v, %v", text, err)
	}
	reasoning, err := stream.Recv()
	if err != nil || reasoning.ProviderItem == nil || reasoning.ProviderItem.Type != "reasoning" || reasoning.ProviderItem.Ordinal != 0 || !strings.Contains(string(reasoning.ProviderItem.Payload), `"encrypted_content":"opaque-reasoning"`) {
		t.Fatalf("reasoning = %#v, %v", reasoning, err)
	}
	call, err := stream.Recv()
	if err != nil || call.ToolCall == nil || call.ToolCall.ID != "call_1" || call.ToolCall.Name != qualified || string(call.ToolCall.Arguments) != `{"path":"paper.md"}` {
		t.Fatalf("call = %#v, %v", call, err)
	}
	functionItem, err := stream.Recv()
	if err != nil || functionItem.ProviderItem == nil || functionItem.ProviderItem.Type != "function_call" || functionItem.ProviderItem.CallID != "call_1" || functionItem.ProviderItem.Ordinal != 1 {
		t.Fatalf("function item = %#v, %v", functionItem, err)
	}
	usage, err := stream.Recv()
	if err != nil || usage.Usage == nil || usage.Usage.InputTokens != 10 || usage.Usage.FreshInputTokens != 6 || usage.Usage.CachedInputTokens != 4 || usage.Usage.ReasoningTokens != 2 || !usage.Usage.CacheDetailsReported {
		t.Fatalf("usage = %#v, %v", usage, err)
	}
	done, err := stream.Recv()
	if err != nil || done.FinishReason != "tool_calls" {
		t.Fatalf("done = %#v, %v", done, err)
	}
}

func TestStreamMapsUserImageToResponsesInputContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []struct {
				Type    string         `json:"type"`
				Role    string         `json:"role"`
				Content []inputContent `json:"content"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Input) != 1 || body.Input[0].Type != "message" || body.Input[0].Role != "user" || len(body.Input[0].Content) != 2 || body.Input[0].Content[0].Type != "input_text" || body.Input[0].Content[1].Type != "input_image" || body.Input[0].Content[1].ImageURL != "data:image/webp;base64,AQID" {
			t.Fatalf("input = %#v", body.Input)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()
	stream, err := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil).Stream(context.Background(), model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: "inspect", Parts: []model.ContentPart{{Type: "input_image", MediaType: "image/webp", Data: "AQID"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
}

func TestStreamPreservesInitialTextAndSeparatesCommentaryFromFinalAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg_commentary\",\"role\":\"assistant\",\"phase\":\"commentary\",\"content\":[{\"type\":\"output_text\",\"text\":\"Working\"}]}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"item_id\":\"msg_commentary\",\"delta\":\"...\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg_commentary\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Working...\"}]}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"type\":\"message\",\"id\":\"msg_final\",\"role\":\"assistant\",\"phase\":\"final_answer\",\"content\":[{\"type\":\"output_text\",\"text\":\"Final\"}]}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":1,\"item_id\":\"msg_final\",\"delta\":\" answer\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"type\":\"message\",\"id\":\"msg_final\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Final answer\"}]}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()

	stream, err := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil).Stream(context.Background(), model.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	var commentary, final string
	var items []model.ProviderItem
	for {
		event, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if event.Type == model.EventTextDelta {
			if event.Phase == "commentary" {
				commentary += event.Text
			} else {
				final += event.Text
			}
		}
		if event.ProviderItem != nil {
			items = append(items, *event.ProviderItem)
		}
		if event.Type == model.EventDone {
			break
		}
	}
	if commentary != "Working..." || final != "Final answer" {
		t.Fatalf("commentary = %q, final = %q", commentary, final)
	}
	if len(items) != 2 || items[0].Phase != "commentary" || items[1].Phase != "final_answer" {
		t.Fatalf("provider items = %#v", items)
	}
}

func TestStreamRecoversTextFromCompletedMessageWithoutDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg_final\",\"role\":\"assistant\",\"phase\":\"final_answer\",\"content\":[{\"type\":\"output_text\",\"text\":\"Complete fallback answer\"}]}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()

	stream, err := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil).Stream(context.Background(), model.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	textEvent, err := stream.Recv()
	if err != nil || textEvent.Type != model.EventTextDelta || textEvent.Text != "Complete fallback answer" || textEvent.Phase != "final_answer" {
		t.Fatalf("text event = %#v, %v", textEvent, err)
	}
}

func TestStreamAssignsStableLocalOrdinalsForCompatibleProviderIndexes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":4,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}]}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":4,\"item\":{\"type\":\"reasoning\",\"id\":\"rs_1\",\"summary\":[]}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":9,\"item\":{\"type\":\"reasoning\",\"id\":\"rs_1\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"inspect\"}]}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":4,\"item\":{\"type\":\"reasoning\",\"id\":\"rs_2\",\"summary\":[]}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":4,\"item\":{\"type\":\"reasoning\",\"id\":\"rs_2\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"verify\"}]}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()

	stream, err := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "compatible", TimeoutSeconds: 5}, nil).Stream(context.Background(), model.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	items := make([]model.ProviderItem, 0, 3)
	for {
		event, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if event.ProviderItem != nil {
			items = append(items, *event.ProviderItem)
		}
		if event.Type == model.EventDone {
			break
		}
	}
	if len(items) != 3 {
		t.Fatalf("provider items = %#v", items)
	}
	for index, item := range items {
		if item.Ordinal != index {
			t.Fatalf("provider item %d ordinal = %d", index, item.Ordinal)
		}
	}
}

func TestStreamRejectsDuplicateCompletionAndPreservesEventDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"id\":\"rs_duplicate\",\"summary\":[]}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"id\":\"rs_duplicate\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"inspect\"}]}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"type\":\"reasoning\",\"id\":\"rs_duplicate\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"inspect\"}]}}\n\n")
	}))
	defer server.Close()

	stream, err := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "compatible", TimeoutSeconds: 5}, nil).Stream(context.Background(), model.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	first, err := stream.Recv()
	if err != nil || first.ProviderItem == nil || first.ProviderItem.Ordinal != 0 {
		t.Fatalf("first completion = %#v, %v", first, err)
	}
	_, err = stream.Recv()
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "MODEL_STREAM_INVALID" || !strings.Contains(appErr.Details, "rs_duplicate") || !strings.Contains(appErr.Details, `"output_index": 1`) {
		t.Fatalf("duplicate completion error = %#v", err)
	}
}

func TestStreamClassifiesOversizedToolArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc","call_id":"call","name":"fixture","arguments":""}}`+"\n\n")
		payload, _ := json.Marshal(map[string]any{"type": "response.function_call_arguments.delta", "item_id": "fc", "delta": strings.Repeat("x", modelutil.MaxToolArgsBytes+1)})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
	}))
	defer server.Close()
	stream, err := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil).Stream(context.Background(), model.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_, err = stream.Recv()
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "MODEL_TOOL_CALL_INVALID" || appErr.Retryable {
		t.Fatalf("error = %#v", err)
	}
}

func TestStreamOrdersParallelFunctionItemsByFirstObservation(t *testing.T) {
	qualified := "builtin.workspace.read_text"
	alias := modelutil.ProviderToolName(qualified)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":%q,\"arguments\":\"{}\"}}\n\n", alias)
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_2\",\"call_id\":\"call_2\",\"name\":%q,\"arguments\":\"{}\"}}\n\n", alias)
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":8,\"item\":{\"type\":\"function_call\",\"id\":\"fc_2\",\"call_id\":\"call_2\",\"name\":%q,\"arguments\":\"{}\"}}\n\n", alias)
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":9,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":%q,\"arguments\":\"{}\"}}\n\n", alias)
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()

	stream, err := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "compatible", TimeoutSeconds: 5}, nil).Stream(context.Background(), model.ChatRequest{
		Tools: []model.ToolDefinition{{Name: qualified, InputSchema: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	items := make([]model.ProviderItem, 0, 2)
	for {
		event, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if event.ProviderItem != nil {
			items = append(items, *event.ProviderItem)
		}
		if event.Type == model.EventDone {
			break
		}
	}
	if len(items) != 2 || items[0].Ordinal != 0 || items[0].CallID != "call_1" || items[1].Ordinal != 1 || items[1].CallID != "call_2" {
		t.Fatalf("ordered provider items = %#v", items)
	}
}

func TestStreamRetriesWithoutRejectedPromptCacheKey(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body payload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if requests == 1 {
			if body.PromptCacheKey != "conversation-1" {
				t.Fatalf("first prompt cache key = %q", body.PromptCacheKey)
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"Unknown parameter: prompt_cache_key"}}`)
			return
		}
		if body.PromptCacheKey != "" {
			t.Fatalf("fallback prompt cache key = %q", body.PromptCacheKey)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()

	client := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil)
	stream, err := client.Stream(context.Background(), model.ChatRequest{PromptCacheKey: "conversation-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if event, err := stream.Recv(); err != nil || event.Type != model.EventDone {
		t.Fatalf("done = %#v, %v", event, err)
	}
	second, err := client.Stream(context.Background(), model.ChatRequest{PromptCacheKey: "conversation-1"})
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Close()
	if requests != 3 {
		t.Fatalf("requests = %d", requests)
	}
}

func TestStreamIncompleteIsTerminal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.incomplete\",\"response\":{\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n")
	}))
	defer server.Close()
	stream, err := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil).Stream(context.Background(), model.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	event, err := stream.Recv()
	if err != nil || event.Type != model.EventDone || event.FinishReason != "length" {
		t.Fatalf("event = %#v, %v", event, err)
	}
}

func TestStreamFailurePreservesNestedProviderDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.failed","response":{"status":"failed","error":{"code":"context_length_exceeded","message":"Input exceeds the model context window","param":"input"}},"request_id":"req-fixture"}`+"\n\n")
	}))
	defer server.Close()
	stream, err := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil).Stream(context.Background(), model.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_, err = stream.Recv()
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.UserMessage != "Input exceeds the model context window" || !strings.Contains(appErr.Details, "context_length_exceeded") || !strings.Contains(appErr.Details, "req-fixture") {
		t.Fatalf("error = %#v", err)
	}
}

func TestProviderTurnReplaysEncryptedReasoningBeforeFunctionOutput(t *testing.T) {
	qualified := "builtin.workspace.read_text"
	alias := modelutil.ProviderToolName(qualified)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input   []json.RawMessage `json:"input"`
			Include []string          `json:"include"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Input) != 4 || len(body.Include) != 1 {
			t.Fatalf("body = %#v", body)
		}
		var reasoning, functionCall, functionOutput map[string]any
		if err := json.Unmarshal(body.Input[1], &reasoning); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body.Input[2], &functionCall); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body.Input[3], &functionOutput); err != nil {
			t.Fatal(err)
		}
		if reasoning["type"] != "reasoning" || reasoning["encrypted_content"] != "opaque-reasoning" || functionCall["type"] != "function_call" || functionCall["call_id"] != "call_1" || functionOutput["type"] != "function_call_output" || functionOutput["call_id"] != "call_1" || !strings.Contains(functionOutput["output"].(string), "paper content") {
			t.Fatalf("replay = reasoning:%#v call:%#v output:%#v", reasoning, functionCall, functionOutput)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()

	stream, err := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "gpt-reasoning", TimeoutSeconds: 5}, nil).Stream(context.Background(), model.ChatRequest{
		Messages: []model.Message{{Role: model.RoleUser, Content: "read"}},
		Tools:    []model.ToolDefinition{{Name: qualified, InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ProviderTurns: []model.ProviderTurn{{
			TurnIndex: 1,
			Protocol:  modelcap.ProtocolOpenAIResponses,
			Items: []model.ProviderItem{
				{Ordinal: 0, Type: "reasoning", Payload: json.RawMessage(`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque-reasoning"}`)},
				{Ordinal: 1, Type: "function_call", CallID: "call_1", Payload: json.RawMessage(fmt.Sprintf(`{"type":"function_call","id":"fc_1","call_id":"call_1","name":%q,"arguments":"{\"path\":\"paper.md\"}"}`, alias))},
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

func TestStreamRetriesWithoutRejectedReasoningControl(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body payload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if requests == 1 {
			if body.Reasoning == nil {
				t.Fatal("first request omitted reasoning")
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"reasoning.effort is unsupported"}}`)
			return
		}
		if body.Reasoning != nil {
			t.Fatalf("fallback reasoning = %#v", body.Reasoning)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
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
	if requests != 2 {
		t.Fatalf("requests = %d", requests)
	}
}

func TestStreamRetriesWithoutRejectedReasoningSummary(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body payload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Reasoning == nil || body.Reasoning.Effort != "high" {
			t.Fatalf("reasoning = %#v", body.Reasoning)
		}
		if requests == 1 {
			if body.Reasoning.Summary != "auto" {
				t.Fatalf("first summary = %#v", body.Reasoning)
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"Unknown parameter: reasoning.summary"}}`)
			return
		}
		if body.Reasoning.Summary != "" {
			t.Fatalf("fallback summary = %#v", body.Reasoning)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()
	stream, err := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "custom", TimeoutSeconds: 5}, nil).Stream(context.Background(), model.ChatRequest{ResolvedReasoningLevel: modelcap.ReasoningHigh})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d", requests)
	}
}

type reasoningRecorder struct{ result modelcap.ReasoningResult }

func (r *reasoningRecorder) RecordReasoningResult(_ context.Context, _, _ string, result modelcap.ReasoningResult) error {
	r.result = result
	return nil
}

func TestStreamNegotiatesMaxToXHigh(t *testing.T) {
	var efforts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body payload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		effort := ""
		if body.Reasoning != nil {
			effort = body.Reasoning.Effort
		}
		efforts = append(efforts, effort)
		if effort == "max" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"reasoning.effort has invalid value max; expected one of low, medium, high, xhigh"}}`)
			return
		}
		if effort != "xhigh" {
			t.Fatalf("fallback effort = %q", effort)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()
	recorder := &reasoningRecorder{}
	stream, err := New(modelprofile.Profile{ID: "profile", BaseURL: server.URL, ModelID: "future-model", TimeoutSeconds: 5}, nil, recorder).Stream(context.Background(), model.ChatRequest{RequestedReasoningLevel: modelcap.ReasoningMax, ResolvedReasoningLevel: modelcap.ReasoningMax})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	reporter, ok := stream.(model.ReasoningResolutionReporter)
	if !ok || reporter.ReasoningResolution().Resolved != modelcap.ReasoningXHigh {
		t.Fatalf("resolution = %#v", reporter)
	}
	if fmt.Sprint(efforts) != "[max xhigh]" || recorder.result.Resolved != modelcap.ReasoningXHigh || len(recorder.result.Rejected) != 1 || recorder.result.Rejected[0] != modelcap.ReasoningMax {
		t.Fatalf("efforts = %v, observation = %#v", efforts, recorder.result)
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

func TestStreamMarksPrematureEOFAsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	stream, err := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil).Stream(context.Background(), model.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_, err = stream.Recv()
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "MODEL_STREAM_INTERRUPTED" || !appErr.Retryable {
		t.Fatalf("Recv() error = %#v", err)
	}
}

package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func TestStreamSendsResolvedReasoningEffort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var requestBody requestPayload
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		if requestBody.ReasoningEffort != "xhigh" {
			t.Fatalf("reasoning_effort = %q", requestBody.ReasoningEffort)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil)
	stream, err := client.Stream(context.Background(), model.ChatRequest{ResolvedReasoningLevel: modelcap.ReasoningXHigh})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
}

func TestStreamNormalizesSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-secret" {
			t.Fatalf("Authorization = %q", got)
		}
		var requestBody requestPayload
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(requestBody.Messages) != 1 || requestBody.Messages[0].Content == nil || !strings.Contains(*requestBody.Messages[0].Content, "<untrusted_conversation_content>") {
			t.Fatalf("request did not mark user content as untrusted: %#v", requestBody)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"你好\"},\"finish_reason\":null}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"prompt_tokens_details\":{\"cached_tokens\":2}}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL + "/v1", ModelID: "fixture", TimeoutSeconds: 5}, []byte("test-secret"))
	stream, err := client.Stream(context.Background(), model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer stream.Close()
	textEvent, err := stream.Recv()
	if err != nil || textEvent.Text != "你好" {
		t.Fatalf("text event = %#v, err=%v", textEvent, err)
	}
	usageEvent, err := stream.Recv()
	if err != nil || usageEvent.Usage == nil || usageEvent.Usage.InputTokens != 3 || usageEvent.Usage.CachedInputTokens != 2 || !usageEvent.Usage.CacheDetailsReported {
		t.Fatalf("usage event = %#v, err=%v", usageEvent, err)
	}
	doneEvent, err := stream.Recv()
	if err != nil || doneEvent.Type != model.EventDone || doneEvent.FinishReason != "stop" {
		t.Fatalf("done event = %#v, err=%v", doneEvent, err)
	}
}

func TestResponseUsageNormalizesCompatibleCacheFields(t *testing.T) {
	read, created, hit, miss := 13, 5, 11, 7
	usage := responseUsage{PromptTokens: 30, CompletionTokens: 9, CacheReadInputTokens: &read, CacheCreationInputTokens: &created, PromptCacheHitTokens: &hit, PromptCacheMissTokens: &miss}
	got := usage.normalized()
	if got.InputTokens != 30 || got.FreshInputTokens != 12 || got.OutputTokens != 9 || got.CachedInputTokens != 13 || got.CacheWriteTokens != 5 || !got.CacheDetailsReported {
		t.Fatalf("normalized usage = %#v", got)
	}
}

func TestStreamMapsToolsAndAccumulatesFragmentedToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var requestBody requestPayload
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		providerName := providerToolName("builtin.workspace.read_text")
		if len(requestBody.Tools) != 1 || requestBody.Tools[0].Function.Name != providerName || strings.Contains(requestBody.Tools[0].Function.Name, ".") {
			t.Fatalf("tools = %#v", requestBody.Tools)
		}
		if len(requestBody.Messages) != 3 || len(requestBody.Messages[1].ToolCalls) != 1 || requestBody.Messages[1].ToolCalls[0].Function.Name != providerToolName("builtin.workspace.list") || requestBody.Messages[2].ToolCallID != "call-old" || requestBody.Messages[2].Content == nil || !strings.Contains(*requestBody.Messages[2].Content, "<untrusted_tool_result>") {
			t.Fatalf("messages = %#v", requestBody.Messages)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, fmt.Sprintf(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-new","function":{"name":%q,"arguments":"{\"path\":\"paper.md\"}"}}]},"finish_reason":"tool_calls"}]}`, providerName)+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil)
	stream, err := client.Stream(context.Background(), model.ChatRequest{
		Messages: []model.Message{
			{Role: model.RoleUser, Content: "读取论文"},
			{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "call-old", Name: "builtin.workspace.list", Arguments: json.RawMessage(`{}`)}}},
			{Role: model.RoleTool, ToolCallID: "call-old", Content: "上一次结果"},
		},
		Tools: []model.ToolDefinition{{Name: "builtin.workspace.read_text", Description: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	event, err := stream.Recv()
	if err != nil || event.Type != model.EventToolCall || event.ToolCall == nil || event.ToolCall.ID != "call-new" || event.ToolCall.Name != "builtin.workspace.read_text" || string(event.ToolCall.Arguments) != `{"path":"paper.md"}` {
		t.Fatalf("tool event = %#v, %v", event, err)
	}
	if event, err = stream.Recv(); err != nil || event.Type != model.EventDone || event.FinishReason != "tool_calls" {
		t.Fatalf("done event = %#v, %v", event, err)
	}
}

func TestProviderToolNamesAreCompatibleStableAndDistinct(t *testing.T) {
	values := []string{
		"builtin.workspace.read_text",
		"builtin_workspace_read_text",
		"mcp.chrome-devtools.take_screenshot",
		strings.Repeat("long.namespace.", 20),
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		name := providerToolName(value)
		if len(name) == 0 || len(name) > maxProviderToolName {
			t.Fatalf("providerToolName(%q) = %q", value, name)
		}
		for _, character := range name {
			if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-') {
				t.Fatalf("provider name %q contains incompatible character %q", name, character)
			}
		}
		if _, duplicate := seen[name]; duplicate {
			t.Fatalf("provider name collision for %q", name)
		}
		seen[name] = struct{}{}
		if providerToolName(value) != name {
			t.Fatalf("provider name for %q is not stable", value)
		}
	}
}

func TestRequestRejectionIncludesBoundedProviderDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"Invalid tools[0].function.name"}}`)
	}))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil)
	_, err := client.Stream(context.Background(), model.ChatRequest{})
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "MODEL_REQUEST_REJECTED" || !strings.Contains(appErr.UserMessage, "HTTP 400") || !strings.Contains(appErr.UserMessage, "Invalid tools") {
		t.Fatalf("error = %#v", err)
	}
}

func TestStreamRejectsInvalidToolCallArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call","function":{"name":"fixture","arguments":"not-json"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
	}))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil)
	stream, err := client.Stream(context.Background(), model.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_, err = stream.Recv()
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "MODEL_TOOL_CALL_INVALID" {
		t.Fatalf("error = %#v", err)
	}
}

func TestStreamRetriesBeforeContent(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	client := NewWithHTTPClient(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture"}, nil, &http.Client{Timeout: 3 * time.Second})
	stream, err := client.Stream(context.Background(), model.ChatRequest{})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	stream.Close()
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestAuthErrorIsClassifiedAndNotRetried(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { attempts.Add(1); w.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil)
	_, err := client.Stream(context.Background(), model.ChatRequest{})
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "MODEL_AUTH_FAILED" || appErr.Retryable {
		t.Fatalf("error = %#v", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}

func TestStreamAcceptsFinishReasonWithoutDoneSentinel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil)
	stream, err := client.Stream(context.Background(), model.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if event, err := stream.Recv(); err != nil || event.Text != "ok" {
		t.Fatalf("first Recv() = %#v, %v", event, err)
	}
	if event, err := stream.Recv(); err != nil || event.Type != model.EventDone || event.FinishReason != "stop" {
		t.Fatalf("second Recv() = %#v, %v", event, err)
	}
}

func TestDiscoverModelsSortsAndDeduplicates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer discovery-key" {
			t.Fatalf("missing discovery authorization")
		}
		io.WriteString(w, `{"object":"list","data":[{"id":"z-model","owned_by":"lab"},{"id":"a-model","owned_by":"openai"},{"id":"a-model"},{"id":""}]}`)
	}))
	defer server.Close()
	client := New(modelprofile.Profile{TimeoutSeconds: 5}, nil)
	values, err := client.Discover(context.Background(), modelprofile.Profile{BaseURL: server.URL + "/v1", TimeoutSeconds: 5}, []byte("discovery-key"))
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(values) != 2 || values[0].ID != "a-model" || values[1].ID != "z-model" || values[1].OwnedBy != "lab" {
		t.Fatalf("models = %#v", values)
	}
}

func TestDiscoverModelsRejectsOversizedOrMalformedPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `not-json`) }))
	defer server.Close()
	client := New(modelprofile.Profile{TimeoutSeconds: 5}, nil)
	_, err := client.Discover(context.Background(), modelprofile.Profile{BaseURL: server.URL, TimeoutSeconds: 5}, nil)
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "MODEL_LIST_INVALID" {
		t.Fatalf("error = %#v", err)
	}
}

func TestEndpointURLAcceptsBaseAndFullEndpoint(t *testing.T) {
	tests := []struct{ base, suffix, want string }{
		{"https://example.test/v1", "models", "https://example.test/v1/models"},
		{"https://example.test/v1/models", "models", "https://example.test/v1/models"},
		{"https://example.test/v1/models", "chat/completions", "https://example.test/v1/chat/completions"},
		{"https://example.test/v1/chat/completions", "models", "https://example.test/v1/models"},
	}
	for _, test := range tests {
		if got := endpointURL(test.base, test.suffix); got != test.want {
			t.Errorf("endpointURL(%q,%q)=%q, want %q", test.base, test.suffix, got, test.want)
		}
	}
}

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
		io.WriteString(w, fmt.Sprintf("data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":%q,\"input\":{}}}\n\n", alias))
		io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"paper.md\\\"}\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		io.WriteString(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":3}}\n\n")
		io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL + "/v1", ModelID: "fixture", TimeoutSeconds: 5, MaxOutputTokens: &maxTokens}, []byte("secret"))
	stream, err := client.Stream(context.Background(), model.ChatRequest{
		ResolvedReasoningLevel: modelcap.ReasoningHigh,
		Tools:                  []model.ToolDefinition{{Name: qualified, InputSchema: json.RawMessage(`{"type":"object"}`)}},
		Messages:               []model.Message{{Role: model.RoleSystem, Content: "system"}, {Role: model.RoleUser, Content: "read"}, {Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "old", Name: qualified, Arguments: json.RawMessage(`{}`)}}}, {Role: model.RoleTool, ToolCallID: "old", Content: "result"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	call, err := stream.Recv()
	if err != nil || call.ToolCall == nil || call.ToolCall.ID != "call_1" || call.ToolCall.Name != qualified || string(call.ToolCall.Arguments) != `{"path":"paper.md"}` {
		t.Fatalf("call = %#v, %v", call, err)
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

func TestStreamRetriesWithoutRejectedThinkingControl(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body payload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if requests == 1 {
			if body.Thinking == nil {
				t.Fatal("first request omitted thinking")
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
	if requests != 2 {
		t.Fatalf("requests = %d", requests)
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

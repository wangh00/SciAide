package responses

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
		if body.Reasoning == nil || body.Reasoning.Effort != "high" {
			t.Fatalf("reasoning = %#v", body.Reasoning)
		}
		if body.Instructions != "system" || len(body.Tools) != 1 || strings.Contains(body.Tools[0].Name, ".") {
			t.Fatalf("body = %#v", body)
		}
		if len(body.Input) != 3 || body.Input[1].Type != "function_call" || body.Input[1].CallID != "old" || body.Input[2].Type != "function_call_output" || body.Input[2].CallID != "old" || !strings.Contains(body.Input[2].Output, "<untrusted_tool_result>") {
			t.Fatalf("input = %#v", body.Input)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
		io.WriteString(w, fmt.Sprintf("data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"id\":\"item_1\",\"call_id\":\"call_1\",\"name\":%q,\"arguments\":\"\"}}\n\n", alias))
		io.WriteString(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"item_1\",\"delta\":\"{\\\"path\\\":\\\"paper.md\\\"}\"}\n\n")
		io.WriteString(w, fmt.Sprintf("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"id\":\"item_1\",\"call_id\":\"call_1\",\"name\":%q,\"arguments\":\"{\\\"path\\\":\\\"paper.md\\\"}\"}}\n\n", alias))
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":3,\"input_tokens_details\":{\"cached_tokens\":4}}}}\n\n")
	}))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL + "/v1", ModelID: "fixture", TimeoutSeconds: 5}, []byte("secret"))
	stream, err := client.Stream(context.Background(), model.ChatRequest{
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
	call, err := stream.Recv()
	if err != nil || call.ToolCall == nil || call.ToolCall.ID != "call_1" || call.ToolCall.Name != qualified || string(call.ToolCall.Arguments) != `{"path":"paper.md"}` {
		t.Fatalf("call = %#v, %v", call, err)
	}
	usage, err := stream.Recv()
	if err != nil || usage.Usage == nil || usage.Usage.InputTokens != 10 || usage.Usage.FreshInputTokens != 6 || usage.Usage.CachedInputTokens != 4 || !usage.Usage.CacheDetailsReported {
		t.Fatalf("usage = %#v, %v", usage, err)
	}
	done, err := stream.Recv()
	if err != nil || done.FinishReason != "tool_calls" {
		t.Fatalf("done = %#v, %v", done, err)
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

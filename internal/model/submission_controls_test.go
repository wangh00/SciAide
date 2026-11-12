package model_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/anthropic"
	"github.com/wangh00/SciAide/internal/model/openai"
	"github.com/wangh00/SciAide/internal/model/responses"
)

var submissionClients = []struct {
	name string
	new  func(modelprofile.Profile) model.ChatModel
}{
	{"chat", func(p modelprofile.Profile) model.ChatModel { return openai.New(p, nil) }},
	{"responses", func(p modelprofile.Profile) model.ChatModel { return responses.New(p, nil) }},
	{"anthropic", func(p modelprofile.Profile) model.ChatModel { return anthropic.New(p, nil) }},
}

func TestRequiredRepairChoiceAcrossProtocols(t *testing.T) {
	for _, p := range submissionClients {
		t.Run(p.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if p.name == "anthropic" {
					choice, _ := body["tool_choice"].(map[string]any)
					if choice["type"] != "any" || choice["disable_parallel_tool_use"] != true || body["thinking"] != nil {
						t.Error("required choice conflicts with thinking/parallel calls", body)
					}
				} else if body["tool_choice"] != "required" || body["parallel_tool_calls"] != false {
					t.Error("repair was optional or parallel", body)
				}
				if len(body["tools"].([]any)) != 2 {
					t.Error("complete replacement unavailable")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer server.Close()
			client := p.new(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5})
			request := model.ChatRequest{RequireTool: true, RequestedReasoningLevel: "high", ResolvedReasoningLevel: "high", Tools: []model.ToolDefinition{{Name: "submit_stage_result", InputSchema: json.RawMessage(`{"type":"object"}`)}, {Name: "submit_stage_repair", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
			stream, err := client.Stream(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			stream.Close()
			for _, invalid := range []model.ChatRequest{{RequireTool: true}, {RequireTool: true, DisableTools: true, Tools: request.Tools}} {
				if stream, err := client.Stream(context.Background(), invalid); err == nil {
					stream.Close()
					t.Fatal("invalid host contract sent")
				}
			}
			if calls != 1 {
				t.Fatal("invalid host contract reached the provider", calls)
			}
		})
	}
}

func TestNativeRefusalAcrossProtocolsKeepsTextAndTerminal(t *testing.T) {
	for _, p := range submissionClients {
		for _, text := range []string{"Provider declined", ""} {
			t.Run(p.name+"/"+text, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					switch p.name {
					case "chat":
						fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"refusal\":%q},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", text)
					case "responses":
						fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n")
						fmt.Fprintf(w, "data: {\"type\":\"response.refusal.delta\",\"item_id\":\"m1\",\"delta\":%q}\n\n", text)
						fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"refusal\",\"refusal\":%q}]}}\n\ndata: {\"type\":\"response.completed\",\"response\":{}}\n\n", text)
					case "anthropic":
						fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"role\":\"assistant\",\"content\":[]}}\n\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
						fmt.Fprintf(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"refusal\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n", text)
					}
				}))
				defer server.Close()
				stream, err := p.new(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}).Stream(context.Background(), model.ChatRequest{})
				if err != nil {
					t.Fatal(err)
				}
				defer stream.Close()
				got, reason := "", ""
				for {
					event, err := stream.Recv()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if event.Type == model.EventTextDelta {
						got += event.Text
					}
					if event.Type == model.EventDone {
						reason = event.FinishReason
					}
				}
				if got != text || reason != "refusal" {
					t.Fatalf("refusal lost/duplicated: text=%q reason=%q", got, reason)
				}
			})
		}
	}
}

func TestBufferedChatRefusalPreservesNativeSignal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":null,"refusal":"Provider declined"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	stream, err := openai.New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil).Stream(context.Background(), model.ChatRequest{DisableStreaming: true})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	text, reason := "", ""
	for {
		e, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if e.Type == model.EventTextDelta {
			text += e.Text
		}
		if e.Type == model.EventDone {
			reason = e.FinishReason
		}
	}
	if text != "Provider declined" || reason != "refusal" {
		t.Fatal(text, reason)
	}
}

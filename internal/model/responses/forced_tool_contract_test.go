package responses

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/modelutil"
)

func TestForcedSingletonUsesRequiredWithoutWeakeningContract(t *testing.T) {
	for _, name := range []string{"submit_stage_result", "external.tools.submit"} {
		for _, alsoRequired := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/required_%t", name, alsoRequired), func(t *testing.T) {
				schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["marker"],"properties":{"marker":{"type":"string","enum":["ack"]}}}`)
				definitions := []model.ToolDefinition{{Name: name, Description: "Frozen control", InputSchema: schema}}
				before, _ := json.Marshal(definitions)
				calls, gates := 0, 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						return
					}
					tools, _ := body["tools"].([]any)
					if len(tools) != 1 || body["parallel_tool_calls"] != false {
						t.Error("single-target nonparallel contract changed")
						return
					}
					tool, _ := tools[0].(map[string]any)
					var expectedSchema any
					_ = json.Unmarshal(schema, &expectedSchema)
					if tool["name"] != modelutil.ProviderToolName(name) || tool["description"] != "Frozen control" || !reflect.DeepEqual(tool["parameters"], expectedSchema) {
						t.Error("forced target or frozen schema changed")
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					if body["tool_choice"] != "required" {
						// Reproduce a service that silently ignores the named dialect.
						fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Plain message instead of a call\"}]}}\n\n")
					} else {
						fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"function\",\"call_id\":\"ack\",\"name\":%q,\"arguments\":\"{\\\"marker\\\":\\\"ack\\\"}\"}}\n\n", tool["name"])
					}
					fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
				}))
				defer server.Close()
				client := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil)
				client.SetRequestGate(func(context.Context) error { gates++; return nil })
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				stream, err := client.Stream(ctx, model.ChatRequest{ForcedTool: name, RequireTool: alsoRequired, Tools: definitions})
				if err != nil {
					t.Fatal(err)
				}
				defer stream.Close()
				toolCalls, terminal := 0, ""
				for {
					event, err := stream.Recv()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if event.Type == model.EventToolCall {
						toolCalls++
						if event.ToolCall == nil || event.ToolCall.Name != name || string(event.ToolCall.Arguments) != `{"marker":"ack"}` {
							t.Fatal("native target or original arguments changed", event.ToolCall)
						}
					}
					if event.Type == model.EventDone {
						terminal = event.FinishReason
					}
				}
				after, _ := json.Marshal(definitions)
				if calls != 1 || gates != 1 || toolCalls != 1 || terminal != "tool_calls" || string(before) != string(after) {
					t.Fatal("forced singleton was ignored, retried or mutated", calls, gates, toolCalls, terminal)
				}
			})
		}
	}
}

func TestForcedMultiToolStillNamesTheExactTarget(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		choice, _ := body["tool_choice"].(map[string]any)
		tools, _ := body["tools"].([]any)
		if choice["type"] != "function" || choice["name"] != "submit_stage_result" || len(tools) != 2 || body["parallel_tool_calls"] != false {
			t.Error("multi-tool forced target was weakened into any-tool choice")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil)
	stream, err := client.Stream(context.Background(), model.ChatRequest{ForcedTool: "submit_stage_result", Tools: []model.ToolDefinition{{Name: "submit_stage_repair", InputSchema: json.RawMessage(`{"type":"object"}`)}, {Name: "submit_stage_result", InputSchema: json.RawMessage(`{"type":"object"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	stream.Close()
	if calls != 1 {
		t.Fatal(calls)
	}
}

func TestForcedSingletonInvalidTargetStillStopsBeforeIO(t *testing.T) {
	calls, gates := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil)
	client.SetRequestGate(func(context.Context) error { gates++; return nil })
	def := model.ToolDefinition{Name: "submit_stage_result", InputSchema: json.RawMessage(`{"type":"object"}`)}
	for _, request := range []model.ChatRequest{
		{ForcedTool: def.Name},
		{ForcedTool: "missing", Tools: []model.ToolDefinition{def}},
		{ForcedTool: def.Name, DisableTools: true, Tools: []model.ToolDefinition{def}},
		{ForcedTool: def.Name, Tools: []model.ToolDefinition{def, def}},
	} {
		if stream, err := client.Stream(context.Background(), request); err == nil {
			stream.Close()
			t.Fatal("invalid forced contract reached the network")
		}
	}
	if calls != 0 || gates != 0 {
		t.Fatal("invalid contract consumed a request", calls, gates)
	}
}

func TestOptionalSingletonDoesNotBecomeRequired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, present := body["tool_choice"]; present {
			t.Error("optional exploration became a forced control")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil)
	stream, err := client.Stream(context.Background(), model.ChatRequest{Tools: []model.ToolDefinition{{Name: "submit_stage_result", InputSchema: json.RawMessage(`{"type":"object"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	stream.Close()
}

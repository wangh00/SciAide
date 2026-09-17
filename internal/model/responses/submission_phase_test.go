package responses

import (
	"context"
	"encoding/json"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/model"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSubmissionToolChoiceOnWire(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		v, ok := body["tool_choice"]
		if !ok || v != "none" {
			t.Errorf("tool_choice=%v", v)
		}
		if len(body["tools"].([]any)) != 1 {
			t.Error("historical schema removed")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL + "/v1", ModelID: "fixture", TimeoutSeconds: 5}, []byte("secret"))
	stream, err := client.Stream(context.Background(), model.ChatRequest{DisableTools: true, Messages: []model.Message{{Role: model.RoleUser, Content: "submit"}}, Tools: []model.ToolDefinition{{Name: "builtin.resource.open", InputSchema: json.RawMessage(`{"type":"object"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
}

func TestForcedSubmissionToolOnWire(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		choice, ok := body["tool_choice"].(map[string]any)
		if !ok || choice["type"] != "function" || choice["name"] != "submit_stage_result" {
			t.Errorf("choice=%v", body["tool_choice"])
		}
		if body["parallel_tool_calls"] != false {
			t.Error("parallel submission not disabled")
		}
		if len(body["tools"].([]any)) != 1 {
			t.Error("exploration tools leaked into submission")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL + "/v1", ModelID: "fixture", TimeoutSeconds: 5}, []byte("secret"))
	stream, err := client.Stream(context.Background(), model.ChatRequest{ForcedTool: "submit_stage_result", Messages: []model.Message{{Role: model.RoleUser, Content: "submit"}}, Tools: []model.ToolDefinition{{Name: "submit_stage_result", InputSchema: json.RawMessage(`{"type":"object","required":["state"],"properties":{"state":{"type":"string"}}}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
}

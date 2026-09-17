package anthropic

import (
	"context"
	"encoding/json"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/httpua"
	"github.com/wangh00/SciAide/internal/model"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestShortToolNameCollisionIsRejectedBeforeHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("colliding tool names reached network")
		w.WriteHeader(400)
	}))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL + "/v1", ModelID: "fixture", TimeoutSeconds: 5}, []byte("secret"))
	stream, err := client.Stream(context.Background(), model.ChatRequest{Tools: []model.ToolDefinition{
		{Name: "builtin.knowledge.search", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "knowledge_search", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}})
	if stream != nil {
		stream.Close()
	}
	if err == nil {
		t.Fatal("ambiguous short alias accepted")
	}
}

func TestWireToolNamesMatchDefinitionsPromptsAndResourceHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.Host)
		if r.UserAgent() != httpua.ForHost(host) {
			t.Errorf("unexpected User-Agent: %s", r.UserAgent())
		}
		body, _ := io.ReadAll(r.Body)
		var value map[string]any
		if err := json.Unmarshal(body, &value); err != nil {
			t.Error(err)
			return
		}
		tools := value["tools"].([]any)
		def := tools[0].(map[string]any)
		alias, _ := def["name"].(string)
		if alias == "" || strings.Contains(string(body), "builtin.resource.open") {
			t.Errorf("mixed canonical/provider names on wire: %s", body)
		}
		if !strings.Contains(string(body), "Call "+alias) || !strings.Contains(string(body), "Use "+alias) {
			t.Errorf("host instructions did not use declared alias: %s", body)
		}
		// A successful second invocation should be able to copy its function name
		// directly from the previous tool result's action menu.
		if !strings.Contains(string(body), `\"tool\":\"`+alias+`\"`) {
			t.Errorf("resource menu differs from declared function: %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL + "/v1", ModelID: "fixture", TimeoutSeconds: 5}, []byte("secret"))
	result := `{"status":"success","text":"manual","structured":{"actionId":"res_a","kind":"menu","actions":[{"tool":"builtin.resource.open"}]}}`
	stream, err := client.Stream(context.Background(), model.ChatRequest{Tools: []model.ToolDefinition{{Name: "builtin.resource.open", Description: "Call builtin.resource.open", InputSchema: json.RawMessage(`{"type":"object"}`)}}, Messages: []model.Message{
		{Role: model.RoleSystem, Content: "Use builtin.resource.open/search"},
		{Role: model.RoleUser, Content: "Call builtin.resource.open", HostToolReferences: true},
		{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "previous", Name: "builtin.resource.open", Arguments: json.RawMessage(`{}`)}}},
		{Role: model.RoleTool, ToolCallID: "previous", Content: result},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
}

func TestWireKnowledgeNameIsShortAcrossPromptAndHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var value map[string]any
		if err := json.Unmarshal(body, &value); err != nil {
			t.Error(err)
			return
		}
		tools := value["tools"].([]any)
		def := tools[0].(map[string]any)
		alias, _ := def["name"].(string)
		if alias != "knowledge_search" || strings.Contains(string(body), "builtin.knowledge.search") {
			t.Errorf("mixed canonical/provider names on wire: %s", body)
		}
		if !strings.Contains(string(body), "Call "+alias) || !strings.Contains(string(body), "Use "+alias) {
			t.Errorf("host instructions did not use declared alias: %s", body)
		}
		// A successful second invocation should be able to copy its function name
		// directly from the previous tool result's action menu.
		if !strings.Contains(string(body), `\"tool\":\"`+alias+`\"`) {
			t.Errorf("resource menu differs from declared function: %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL + "/v1", ModelID: "fixture", TimeoutSeconds: 5}, []byte("secret"))
	result := `{"status":"success","text":"manual","structured":{"actionId":"res_a","kind":"menu","actions":[{"tool":"builtin.knowledge.search"}]}}`
	stream, err := client.Stream(context.Background(), model.ChatRequest{Tools: []model.ToolDefinition{{Name: "builtin.knowledge.search", Description: "Call builtin.knowledge.search", InputSchema: json.RawMessage(`{"type":"object"}`)}}, Messages: []model.Message{
		{Role: model.RoleSystem, Content: "Use builtin.knowledge.search"},
		{Role: model.RoleUser, Content: "Call builtin.knowledge.search", HostToolReferences: true},
		{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "previous", Name: "builtin.knowledge.search", Arguments: json.RawMessage(`{}`)}}},
		{Role: model.RoleTool, ToolCallID: "previous", Content: result},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
}

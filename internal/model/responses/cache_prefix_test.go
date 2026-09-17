package responses

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func TestDynamicHostStateFollowsWholeNativeProtocolGroup(t *testing.T) {
	var requests []struct {
		Instructions string
		Input        []json.RawMessage
		Tools        []json.RawMessage
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Instructions string
			Input        []json.RawMessage
			Tools        []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		requests = append(requests, body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil)
	raw := json.RawMessage(`{"type":"function_call","id":"fc","call_id":"c","name":"read","arguments":"{}"}`)
	for _, state := range []string{"menu A", "menu B"} {
		s, err := client.Stream(context.Background(), model.ChatRequest{Messages: []model.Message{{Role: model.RoleSystem, Content: "fixed rules"}, {Role: model.RoleUser, Content: "frozen task"}, {Role: model.RoleSystem, Content: state, ContextTail: true, HostToolReferences: true}}, ProviderTurns: []model.ProviderTurn{{TurnIndex: 1, Protocol: modelcap.ProtocolOpenAIResponses, Items: []model.ProviderItem{{Ordinal: 0, Type: "function_call", CallID: "c", Payload: raw}}, ToolResults: []model.Message{{Role: model.RoleTool, ToolCallID: "c", Content: "complete evidence"}}}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Recv(); err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	if len(requests) != 2 || len(requests[0].Input) != 4 {
		t.Fatal("unexpected wire structure")
	}
	if !reflect.DeepEqual(requests[0].Input[:3], requests[1].Input[:3]) || requests[0].Instructions != requests[1].Instructions {
		t.Fatal("dynamic state invalidated stable native prefix")
	}
	if string(requests[0].Input[1]) != string(raw) {
		t.Fatal("native item was rewritten")
	}
	var last struct{ Role string }
	json.Unmarshal(requests[1].Input[3], &last)
	if last.Role != "system" {
		t.Fatal("host state lost authority")
	}
}

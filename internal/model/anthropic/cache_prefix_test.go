package anthropic

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
)

func TestDynamicSystemStateKeepsStableCacheBreakpoint(t *testing.T) {
	var systems [][]contentBlock
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			System   []contentBlock
			Messages []message
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		systems = append(systems, body.System)
		for _, m := range body.Messages {
			for _, b := range m.Content {
				if b.Text == "menu A" || b.Text == "menu B" {
					t.Error("system authority lowered")
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil)
	for _, state := range []string{"menu A", "menu B"} {
		s, err := client.Stream(context.Background(), model.ChatRequest{PromptCacheKey: "stable", Messages: []model.Message{{Role: model.RoleSystem, Content: "fixed rules"}, {Role: model.RoleUser, Content: "frozen task"}, {Role: model.RoleSystem, Content: state, ContextTail: true}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Recv(); err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	if len(systems) != 2 || len(systems[0]) != 2 || !reflect.DeepEqual(systems[0][0], systems[1][0]) || systems[0][0].CacheControl == nil || systems[1][1].Text != "menu B" {
		t.Fatal("unstable system cache boundary", systems)
	}
}

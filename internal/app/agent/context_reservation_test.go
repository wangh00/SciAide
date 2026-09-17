package agent

import (
	"context"
	"encoding/json"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/model"
	"strings"
	"testing"
)

func TestMixedHistoryReservesAllToolEnvelopesBeforeResultText(t *testing.T) {
	payload := json.RawMessage(`{"type":"function_call","call_id":"native","name":"read","arguments":"{}"}`)
	calls := []tool.Call{
		{ProviderCallID: "native", ToolName: "read", Arguments: json.RawMessage(`{}`), Result: &tool.Result{Text: strings.Repeat("n", 10000)}},
		{ProviderCallID: "preload", ToolName: "load", Arguments: json.RawMessage(`{"name":"` + strings.Repeat("x", 800) + `"}`), Result: &tool.Result{Text: "loaded"}},
	}
	budget := len([]rune(fixedSystemRules)) + len(payload) + 1100
	req, info, err := NewContextBuilder(budget).BuildWithInfo(context.Background(), []conversation.Message{{ID: "u", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: "review"}}}}, "", nil, calls, model.ProviderTurn{TurnIndex: 1, Items: []model.ProviderItem{{Ordinal: 0, CallID: "native", Payload: payload}}})
	if err != nil {
		t.Fatal(err)
	}
	if info.EstimatedTokens > budget || len(req.ProviderTurns) != 1 || string(req.ProviderTurns[0].Items[0].Payload) != string(payload) {
		t.Fatal("budget/signature changed")
	}
	found := false
	for _, m := range req.Messages {
		if m.ToolCallID == "preload" {
			found = true
		}
	}
	if !found {
		t.Fatal("normalized call dropped")
	}
}

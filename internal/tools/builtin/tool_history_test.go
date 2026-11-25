package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/tool"
)

type ledgerTestReader struct {
	calls []tool.Call
	query string
}

func (r *ledgerTestReader) ListByRun(_ context.Context, id string) ([]tool.Call, error) {
	r.query = id
	if id != "run" {
		return nil, nil
	}
	return r.calls, nil
}

func TestToolHistoryReadsExactSavedFieldsWithoutPrivateResult(t *testing.T) {
	r := &ledgerTestReader{}
	for i := 0; i < 7; i++ {
		result := tool.Result{Status: tool.ResultError, Text: "PRIVATE_FULL_RESULT", ModelProjection: &tool.ModelResultProjection{Text: "original visible 中文🧪"}}
		args, _ := json.Marshal(map[string]string{"query": strings.Repeat("中文🧪\\\"", 300)})
		c := tool.Call{ID: fmt.Sprint("call-", i), RunID: "run", ProviderCallID: fmt.Sprint("p-", i), ToolName: "builtin.fixture.read", ToolVersion: "1", Status: tool.CallFailed, Arguments: args, Result: &result, ModelContextVersion: tool.ModelContextSnapshotVersion}
		c.ModelContext = tool.BuildModelContextSnapshot(c.ID, "ORIGINAL_ERROR", result)
		r.calls = append(r.calls, c)
	}
	ledger, err := contextmemory.BuildToolLedger(r.calls, "run", "call-6")
	if err != nil {
		t.Fatal(err)
	}
	implementation := NewToolHistory(r)
	invoke := func(values map[string]any) (tool.Result, error) {
		values["throughCallId"], values["ledgerSha256"] = ledger.Ref.ThroughCallID, ledger.Ref.SHA256
		args, _ := json.Marshal(values)
		return implementation.Invoke(context.Background(), tool.Invocation{CallID: "current-reader", ProjectID: "project", RunID: "run", Arguments: args})
	}
	index, err := invoke(map[string]any{"view": "index"})
	var directory struct {
		Entries    []contextmemory.ToolLedgerEntry
		NextOffset int
	}
	if err != nil || json.Unmarshal(index.Structured, &directory) != nil || len(directory.Entries) != 4 || directory.NextOffset != 4 || directory.Entries[0].Status != tool.CallFailed {
		t.Fatal("bad bounded index/status", err)
	}
	for _, view := range []string{"arguments", "model_context"} {
		var rebuilt strings.Builder
		for offset := 0; offset >= 0; {
			result, err := invoke(map[string]any{"view": view, "callId": "call-0", "offset": offset, "maxChars": 137})
			var page struct {
				Text        string
				NextOffset  int
				FieldSHA256 string
				Status      tool.CallStatus
			}
			if err != nil || json.Unmarshal(result.Structured, &page) != nil || page.Status != tool.CallFailed || strings.Contains(string(result.Structured), "PRIVATE_FULL_RESULT") {
				t.Fatal("private or incorrect history page", err)
			}
			rebuilt.WriteString(page.Text)
			offset = page.NextOffset
		}
		original := r.calls[0].ModelContext
		if view == "arguments" {
			original = string(r.calls[0].Arguments)
		}
		if rebuilt.String() != original || r.query != "run" {
			t.Fatal("page changed original bytes/scope")
		}
	}
	for _, values := range []map[string]any{
		{"view": "arguments", "callId": "outside"}, {"view": "model_context", "callId": "call-0", "offset": 100000},
		{"view": "index", "limit": 5}, {"view": "arguments", "callId": "call-0", "maxChars": 1001},
		{"view": "index", "runId": "foreign"}, {"view": "index", "offset": -1},
	} {
		if _, err := invoke(values); err == nil {
			t.Fatal("invalid/foreign request accepted", values)
		}
	}
	r.calls[0].ModelContext += "changed"
	if _, err := invoke(map[string]any{"view": "index"}); err == nil {
		t.Fatal("changed prefix accepted")
	}
	args, _ := json.Marshal(map[string]any{"view": "index", "throughCallId": ledger.Ref.ThroughCallID, "ledgerSha256": ledger.Ref.SHA256})
	for _, inv := range []tool.Invocation{{RunID: "foreign", ProjectID: "project", Arguments: args}, {RunID: "run", SubjectKind: tool.SubjectWorkflowRun, ProjectID: "project", Arguments: args}, {RunID: "run", Arguments: args}} {
		if _, err := implementation.Invoke(context.Background(), inv); err == nil {
			t.Fatal("foreign/workflow/unbound scope accepted")
		}
	}
}

func TestToolHistoryEscapedPageFitsExactModelEnvelope(t *testing.T) {
	result := tool.Result{Status: tool.ResultSuccess, Text: "private"}
	c := tool.Call{ID: "call", RunID: "run", ToolName: "builtin.fixture.read", Status: tool.CallCompleted, Result: &result, Arguments: json.RawMessage(`{}`), ModelContextVersion: tool.ModelContextSnapshotVersion, ModelContext: strings.Repeat("\x00\"\\中文🧪", 1000)}
	ledger, err := contextmemory.BuildToolLedger([]tool.Call{c}, "run", "call")
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"view": "model_context", "throughCallId": "call", "ledgerSha256": ledger.Ref.SHA256, "callId": "call", "maxChars": 1000})
	page, err := NewToolHistory(&ledgerTestReader{calls: []tool.Call{c}}).Invoke(context.Background(), tool.Invocation{RunID: "run", ProjectID: "project", Arguments: args})
	var check struct{ Structured json.RawMessage }
	if err != nil || json.Unmarshal([]byte(tool.BuildModelContextSnapshot("reader", "", page)), &check) != nil || string(check.Structured) != string(page.Structured) {
		t.Fatal("escaping made an allegedly complete page truncate", err)
	}
}

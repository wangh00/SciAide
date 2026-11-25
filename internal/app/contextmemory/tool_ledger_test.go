package contextmemory

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func ledgerCall(id string) tool.Call {
	r := tool.Result{Status: tool.ResultError, Text: "PRIVATE_RESULT", ModelProjection: &tool.ModelResultProjection{Text: "exposed 中文🧪"}}
	return tool.Call{ID: id, RunID: "run", ProviderCallID: "provider-" + id, ToolName: "builtin.fixture.read", ToolVersion: "1", Status: tool.CallFailed, Arguments: json.RawMessage(`{"key":"中文🧪"}`), Result: &r, ModelContextVersion: tool.ModelContextSnapshotVersion, ModelContext: tool.BuildModelContextSnapshot(id, "FIXTURE_ERROR", r)}
}

func TestToolLedgerSealsOnlyExactSameRunPrefix(t *testing.T) {
	calls := []tool.Call{ledgerCall("a"), ledgerCall("b"), ledgerCall("c")}
	first, err := BuildToolLedger(calls, "run", "b")
	if err != nil || first.Ref.Count != 2 || first.Entries[0].Status != tool.CallFailed || strings.Contains(first.Calls[0].ModelContext, "PRIVATE_RESULT") {
		t.Fatal("ledger did not preserve original model projection and failed status", err)
	}
	encoded, _ := json.Marshal(first)
	if strings.Contains(string(encoded), "PRIVATE_RESULT") || strings.Contains(string(encoded), "exposed 中文") {
		t.Fatal("ledger serialization exposed original field bodies/private Result")
	}
	calls[2].ModelContext = "later record changed"
	later, err := BuildToolLedger(append(calls, ledgerCall("d")), "run", "b")
	if err != nil || !reflect.DeepEqual(first.Ref, later.Ref) {
		t.Fatal("later calls changed an immutable prefix", err)
	}
	for _, change := range []func(*tool.Call){
		func(c *tool.Call) { c.Arguments = json.RawMessage(`{ "key":"中文🧪"}`) },
		func(c *tool.Call) { c.ModelContext += " " },
		func(c *tool.Call) { c.Status = tool.CallCompleted },
		func(c *tool.Call) { c.ToolVersion = "2" },
		func(c *tool.Call) { c.ProviderCallID = "other" },
	} {
		modified := []tool.Call{ledgerCall("a"), ledgerCall("b")}
		change(&modified[0])
		next, err := BuildToolLedger(modified, "run", "b")
		if err != nil || next.Ref.SHA256 == first.Ref.SHA256 {
			t.Fatal("changed prefix reused the original digest", err)
		}
	}
	for _, change := range []func(*tool.Call){
		func(c *tool.Call) { c.RunID = "other" },
		func(c *tool.Call) { c.SubjectKind = tool.SubjectWorkflowRun },
		func(c *tool.Call) { c.Result = nil },
		func(c *tool.Call) { c.Status = tool.CallRunning },
		func(c *tool.Call) { c.ModelContext = "" },
		func(c *tool.Call) { c.ModelContextVersion++ },
		func(c *tool.Call) { c.ModelContext = string([]byte{0xff}) },
	} {
		modified := []tool.Call{ledgerCall("a"), ledgerCall("b")}
		change(&modified[0])
		if _, err := BuildToolLedger(modified, "run", "b"); err == nil {
			t.Fatal("bad/missing original was reconstructed or crossed scope")
		}
	}
	if _, err = BuildToolLedger([]tool.Call{ledgerCall("a"), ledgerCall("a")}, "run", "b"); err == nil {
		t.Fatal("duplicate identity accepted")
	}
	if _, err = BuildToolLedger(calls, "run", "absent"); err == nil {
		t.Fatal("guessed boundary accepted")
	}
}

func TestToolLedgerUnicodeFieldPagesAreExact(t *testing.T) {
	value := strings.Repeat("中文🧪\"\\\x00", 900)
	var rebuilt strings.Builder
	for offset := 0; offset >= 0; {
		page, next, err := ToolLedgerSlice(value, offset, 173)
		if err != nil || next >= 0 && next <= offset {
			t.Fatal("page did not progress", err)
		}
		rebuilt.WriteString(page)
		offset = next
	}
	if rebuilt.String() != value {
		t.Fatal("Unicode/escaping changed original field")
	}
	for _, args := range [][2]int{{-1, 1}, {100000, 1}, {0, 0}, {0, ToolLedgerPageChars + 1}} {
		if _, _, err := ToolLedgerSlice(value, args[0], args[1]); err == nil {
			t.Fatal("out-of-range page accepted")
		}
	}
}

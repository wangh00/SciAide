package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

func millionCharacterMaterial(tb testing.TB, pages int) (*stageMaterialLedger, []tool.Call) {
	tb.Helper()
	input, _ := json.Marshal(map[string]string{"data": strings.Repeat("中", 1_000_000)})
	c := stageBudgetContract(input, "host")
	c.ID, c.NodeID, c.WorkflowStepID = "execution", "draft", "step"
	g := newStageMaterialLedger(c, "run")
	m, err := g.material("/data")
	if err != nil {
		tb.Fatal(err)
	}
	calls := []tool.Call{}
	for i := 0; i < pages; i++ {
		offset, end := i*6000, min(1_000_000, (i+1)*6000)
		data, _ := json.Marshal(map[string]any{"section": "stage_input", "nodeId": c.NodeID, "addressMode": workflow.StageMaterialAddressMode, "pointer": "/data", "encoding": m.Encoding, "sha256": c.InputSHA256, "valueSha256": m.ValueSHA256, "offset": offset, "nextOffset": end, "totalCharacters": 1_000_000, "content": strings.Repeat("中", end-offset)})
		calls = append(calls, tool.Call{ID: fmt.Sprint(i), RunID: "run", ToolName: workflow.ResearchTaskReadTool, Arguments: json.RawMessage(fmt.Sprintf(`{"section":"stage_input","pointer":"/data","offset":%d}`, offset)), Result: &tool.Result{Status: tool.ResultSuccess, Structured: data}})
	}
	return g, calls
}

func TestStageMaterialMillionCharacterIndexIsReusedAndBounded(t *testing.T) {
	g, calls := millionCharacterMaterial(t, 167)
	index := g.runeIndex["/data"]
	if len(index) != 1_000_000 || g.cacheBytes > 16_000_000 {
		t.Fatal("unbounded/missing material index")
	}
	for i := 0; i < 2; i++ {
		if credits := stageInputTransportTurns(g.contract, calls, g); credits != 167 || &g.runeIndex["/data"][0] != &index[0] {
			t.Fatal("historical pages rebuilt the full-value Unicode index", credits)
		}
	}
}

func BenchmarkStageMaterialMillionCharacterLedger(b *testing.B) {
	for _, pages := range []int{16, 160} {
		b.Run(fmt.Sprint(pages), func(b *testing.B) {
			g, calls := millionCharacterMaterial(b, pages)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if g.transportTurns(calls) != pages {
					b.Fatal("invalid material receipt")
				}
			}
		})
	}
}

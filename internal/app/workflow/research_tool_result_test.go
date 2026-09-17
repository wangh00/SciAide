package workflow

import (
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestResearchToolSuccessRequiresUsableFrozenResult(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any, *tool.Result)
		valid  bool
	}{
		{"valid", func(map[string]any, *tool.Result) {}, true},
		{"failed status", func(v map[string]any, _ *tool.Result) { v["status"] = "failed" }, false},
		{"missing value", func(v map[string]any, _ *tool.Result) { delete(v, "value") }, false},
		{"null value", func(v map[string]any, _ *tool.Result) { v["value"] = nil }, false},
		{"string value", func(v map[string]any, _ *tool.Result) { v["value"] = "analysis completed" }, false},
		{"list value", func(v map[string]any, _ *tool.Result) { v["value"] = []int{1, 2} }, false},
		{"empty value", func(v map[string]any, _ *tool.Result) { v["value"] = map[string]any{} }, false},
		{"different code", func(v map[string]any, _ *tool.Result) { v["codeSha256"] = strings.Repeat("b", 64) }, false},
		{"different environment", func(v map[string]any, _ *tool.Result) { v["environmentFingerprint"] = strings.Repeat("b", 64) }, false},
		{"missing inputs", func(v map[string]any, _ *tool.Result) { v["inputSha256"] = map[string]string{} }, false},
		{"missing artifact", func(_ map[string]any, r *tool.Result) { r.Artifacts = nil }, false},
		{"empty artifact", func(_ map[string]any, r *tool.Result) { r.Artifacts[0].SizeBytes = 0 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := strings.Repeat("a", 64)
			detail := RunDetail{Run: Run{ID: "run", Compilation: Compilation{Nodes: []CompiledNode{{OutputSchema: researchAcceptanceReviewSchema()}}}}}
			step := Step{NodeKind: NodePython, NodeID: "python_analysis", Attempt: 1}
			value := map[string]any{"status": "success", "value": map[string]any{"rows": 2}, "codeSha256": hashBytes([]byte("print(1)")), "inputSha256": map[string]string{"data.csv": h}, "outputSha256": map[string]string{"result-run-1.json": h}, "environmentFingerprint": h, "reproductionSha256": h}
			result := tool.Result{Status: tool.ResultSuccess, Artifacts: []tool.ArtifactRef{{WorkspacePath: "result-run-1.json", SHA256: h, SizeBytes: 20}}}
			tc.mutate(value, &result)
			result.Structured = rawObject(value)
			call := tool.Call{Arguments: rawObject(map[string]any{"code": "print(1)", "inputPaths": []string{"data.csv"}, "outputPaths": []string{"result-{{runId}}-{{attempt}}.json"}, "expectedEnvironmentFingerprint": h}), Result: &result}
			_, err := validateResearchToolResult(detail, step, call)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

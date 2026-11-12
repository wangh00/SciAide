package workflow

import (
	"context"
	"strings"
	"testing"
)

func TestExecutedPythonSourceUsesFrozenSuccessfulInput(t *testing.T) {
	s, f := stageMemoryService()
	code := "# 原始执行输入\r\nprint('<script>literal</script>')\n"
	f.detail.Run.Compilation = Compilation{Nodes: []CompiledNode{{ID: "python_analysis", Kind: NodePython}}}
	f.detail.Steps = []Step{{NodeID: "method_implementation", Status: StepCompleted, Output: raw(`{"analysis":{"code":"decoy"}}`)},
		{NodeID: "python_analysis", Attempt: 2, Status: StepCompleted, Input: rawObject(map[string]any{"code": code}), Output: rawObject(map[string]any{"structured": map[string]any{"status": "success", "codeSha256": hashBytes([]byte(code))}})}}
	v, err := s.ExecutedPythonCode(context.Background(), "p", "run", "python_analysis", 2)
	if err != nil || v.Code != code || v.SHA256 != hashBytes([]byte(code)) || !strings.HasSuffix(v.FileName, "-2.py") {
		t.Fatalf("source=%+v err=%v", v, err)
	}
	for _, tc := range []struct {
		project, run, node string
		attempt            int
	}{{"foreign", "run", "python_analysis", 2}, {"p", "foreign", "python_analysis", 2}, {"p", "run", "method_implementation", 2}, {"p", "run", "python_analysis", 1}} {
		if _, err := s.ExecutedPythonCode(context.Background(), tc.project, tc.run, tc.node, tc.attempt); err == nil {
			t.Fatal("accepted foreign source, draft or stale attempt")
		}
	}
	f.detail.Steps[1].Input = raw(`{"code":"tampered"}`)
	if _, err := s.ExecutedPythonCode(context.Background(), "p", "run", "python_analysis", 2); err == nil {
		t.Fatal("accepted changed code")
	}
	f.detail.Steps[1].Input = rawObject(map[string]any{"code": code})
	f.detail.Steps[1].Output = rawObject(map[string]any{"structured": map[string]any{"status": "error", "codeSha256": hashBytes([]byte(code))}})
	if _, err := s.ExecutedPythonCode(context.Background(), "p", "run", "python_analysis", 2); err == nil {
		t.Fatal("accepted failed Kernel as successful execution")
	}
}

func TestExecutedPythonSourceMatchesKernelWhitespaceNormalization(t *testing.T) {
	s, f := stageMemoryService()
	code := " \n\t# 精确执行源码\r\nprint('paired result')\n\t"
	executed := strings.TrimSpace(code)
	input := rawObject(map[string]any{"code": code})
	f.detail.Run.Compilation = Compilation{Nodes: []CompiledNode{{ID: "python_analysis", Kind: NodePython}}}
	f.detail.Steps = []Step{{NodeID: "python_analysis", Attempt: 1, Status: StepCompleted, Input: input, Output: rawObject(map[string]any{"structured": map[string]any{"status": "success", "codeSha256": hashBytes([]byte(executed))}})}}
	got, err := s.ExecutedPythonCode(context.Background(), "p", "run", "python_analysis", 1)
	if err != nil || got.Code != executed || got.SHA256 != hashBytes([]byte(got.Code)) || string(f.detail.Steps[0].Input) != string(input) {
		t.Fatal("export did not match actual Kernel bytes, or rewrote the frozen input", err)
	}
	f.detail.Steps[0].Input = rawObject(map[string]any{"code": code + "# different code"})
	if _, err := s.ExecutedPythonCode(context.Background(), "p", "run", "python_analysis", 1); err == nil {
		t.Fatal("normalization bypassed real code hash mismatch")
	}
}

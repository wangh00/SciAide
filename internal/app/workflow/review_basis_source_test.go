package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewCurrentSourceNavigationNeverUsesHistoryOrRewrites(t *testing.T) {
	current := strings.Repeat("Background unrelated material. ", 160) + "\n不过，共同热量处方不等于实际摄入相同。两项关键直接试验的实际能量摄入及匹配执行质量在本轮材料中无法充分核实。\n" + strings.Repeat("Other unrelated appendix. ", 160)
	old := "实际摄入匹配程度在当前摘要材料中不能完全确认"
	input := mustJSON(map[string]any{"context": map[string]any{"markdown": current}, "reviewHistory": map[string]any{"quote": old}, "_reviewCorrections": map[string]any{"priorOutput": old}})
	original := append([]byte(nil), input...)
	finding := trackedReviewFinding{ID: "issue-1-1", Basis: []reviewBasis{{"/context/markdown", old}}}
	err := reviewBasisDiagnostics([]trackedReviewFinding{finding}, input)
	var basis *ReviewBasisErrors
	if !errors.As(err, &basis) || len(basis.Sources) != 1 {
		t.Fatal("missing current source", err)
	}
	source := basis.Sources[0]
	if source.Pointer != "/context/markdown" || source.InputSHA256 != hashJSON(input) {
		t.Fatal("wrong binding", source)
	}
	found := false
	for _, segment := range source.Segments {
		if segment.Text != string([]rune(current)[segment.Start:segment.End]) || strings.Contains(segment.Text, old) {
			t.Fatal("not exact current text", segment)
		}
		found = found || strings.Contains(segment.Text, "两项关键直接试验")
	}
	if !found || !bytes.Equal(original, input) {
		t.Fatal("missed relevant current text or mutated input")
	}
	for _, pointer := range []string{"/reviewHistory/quote", "/_reviewCorrections/priorOutput", "/context", "/absent"} {
		finding.Basis[0].Pointer = pointer
		err = reviewBasisDiagnostics([]trackedReviewFinding{finding}, input)
		if !errors.As(err, &basis) || len(basis.Sources) != 0 {
			t.Fatal("history/non-scalar became a source", pointer, err)
		}
	}
	projected := reviewCurrentInputFirst(input)
	if !rawJSONEqual(projected, input) || strings.Index(string(projected), `"context"`) > strings.Index(string(projected), `"_reviewCorrections"`) {
		t.Fatal("history view lost or changed values")
	}
}

func TestReviewSourceNavigationVersionOffsetsAndBudgets(t *testing.T) {
	text := strings.Repeat("中文数字 123 café 😀\r\n", 90)
	input := raw(`{"context":{"v":1}}`)
	budget := 12000
	a := reviewBasisSource(input, "/reviewFindings/0/basis/0/quote", "/context/markdown", text, "中文数字", &budget)
	budget = 12000
	b := reviewBasisSource(raw(`{"context":{"v":2}}`), a.Cell, a.Pointer, text, "中文数字", &budget)
	budget = 12000
	c := reviewBasisSource(input, a.Cell, "/context/other", text, "中文数字", &budget)
	if a.Segments[0].ID == b.Segments[0].ID || a.Segments[0].ID == c.Segments[0].ID {
		t.Fatal("source identity not version/path bound")
	}
	end := 0
	var reconstructed strings.Builder
	for _, segment := range a.Segments {
		if segment.Start != end || segment.End-segment.Start != len([]rune(segment.Text)) {
			t.Fatal("bad Unicode offsets")
		}
		reconstructed.WriteString(segment.Text)
		end = segment.End
	}
	if reconstructed.String() != text {
		t.Fatal("short source not lossless")
	}
	budget = 12000
	for i := 0; i < 8; i++ {
		reviewBasisSource(input, a.Cell, a.Pointer, strings.Repeat(text, 40), "中文数字", &budget)
	}
	if budget < 0 {
		t.Fatal("menu exceeded total budget")
	}
	budget = 12000
	if got := reviewBasisSource(input, a.Cell, a.Pointer, strings.Repeat("abcdef", 2000), "不存在", &budget); got != nil {
		t.Fatal("arbitrary source selected without lexical navigation")
	}
}

// Real captured failing turns are read-only; derived repair artifacts go to a
// separately named output directory. This does not run a model or formal DB.
func TestCapturedReviewCurrentSourceNavigation(t *testing.T) {
	dir, out := os.Getenv("SCIAIDE_REVIEW_LOCATION_REPLAY"), os.Getenv("SCIAIDE_REVIEW_LOCATION_OUTPUT")
	if dir == "" || out == "" {
		t.Skip("no read-only capture/output supplied")
	}
	read := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	input, schema := read("input.json"), read("schema.json")
	node := CompiledNode{ID: "independent_review", OutputSchema: schema}
	base, _, err := normalizeWorkflowAIStageSubmissionForInput(string(read("turn-2.txt")), node, input)
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Findings []trackedReviewFinding `json:"reviewFindings"`
	}
	_ = json.Unmarshal(base, &output)
	var diagnostic *ReviewBasisErrors
	if err = validateWorkflowAIStageOutput(node, base, input); !errors.As(err, &diagnostic) || len(diagnostic.Sources) == 0 {
		t.Fatal("original failure/menu absent", err)
	}
	for _, name := range []string{"turn-3.txt", "turn-4.txt"} {
		var patch struct {
			Items []struct{ Value string } `json:"reviewFindingsPatch"`
		}
		if json.Unmarshal(read(name), &patch) != nil || len(patch.Items) != 1 {
			t.Fatal("bad captured patch")
		}
		findings := append([]trackedReviewFinding(nil), output.Findings...)
		findings[0].Basis = append([]reviewBasis(nil), findings[0].Basis...)
		findings[0].Basis[0].Quote = patch.Items[0].Value
		if err := reviewBasisDiagnostics(findings, input); err == nil {
			t.Fatal("old/paraphrased quote now accepted", name)
		}
	}
	if err := os.MkdirAll(out, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"basis-diagnostic.json": mustJSON(diagnostic), "base.json": base, "schema.json": schema} {
		if err := os.WriteFile(filepath.Join(out, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if merged, err := os.ReadFile(filepath.Join(out, "selected-review.json")); err == nil {
		if err := validateWorkflowAIStageOutput(node, merged, input); err != nil {
			t.Fatal("selected current text failed full frozen validation", err)
		}
		t.Log("derived selection passes full frozen validation; scientific approved=false preserved; not a real-model verdict")
	}
	t.Logf("original failure reproduced; %d bounded current-field menus", len(diagnostic.Sources))
}

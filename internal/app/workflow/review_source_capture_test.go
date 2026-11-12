package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Read-only incident replay. The scripted choices below are NOT evidence that
// a real model has used the new first-submission protocol.
func TestCapturedInitialReviewSourceSubmissionKeepsNegativeScientificDecision(t *testing.T) {
	dir := os.Getenv("SCIAIDE_INITIAL_REVIEW_SOURCE_REPLAY")
	if dir == "" {
		t.Skip("no read-only first-source incident capture supplied")
	}
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	input := read("review6-capture-input.json")
	var node CompiledNode
	if json.Unmarshal(read("review6-capture-node.json"), &node) != nil || !usesReviewSourceProtocol(node) {
		t.Fatal("capture does not match the new review protocol")
	}
	catalog, err := BuildReviewSourceCatalog(t.Context(), input, sourceSubmissionBinding(input))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	navigation := catalog.SubmissionMenu()
	cold := time.Since(started)
	started = time.Now()
	_ = catalog.SubmissionMenu()
	warm := time.Since(started)
	if len(navigation) >= len(input) || strings.Contains(string(navigation), "对象设想为该堂课自愿参加") {
		t.Fatal("source navigation duplicated original source text")
	}
	t.Logf("frozen input=%d bytes; source navigation=%d bytes; segments=%d; navigation cold=%s warm=%s (local measurement, not model latency)", len(input), len(navigation), len(catalog.Segments), cold, warm)
	for _, turn := range []string{"2", "3", "4"} {
		original := read("review6-capture-turn-" + turn + ".json")
		wire := decodeObject(original)
		findings := wire["reviewFindings"].([]any)
		if len(findings) != 1 || wire["approved"] != false {
			t.Fatal("incident shape changed")
		}
		for _, raw := range findings[0].(map[string]any)["basis"].([]any) {
			basis := raw.(map[string]any)
			pointer, quote := basis["pointer"].(string), basis["quote"].(string)
			// Explicit scripted field/passage choices, not a fuzzy quote repair.
			// The live design really contains 誉 and MUST NOT be changed.
			switch pointer {
			case "/_userRevision/changes/1", "/designContext/populationAndSampling/1":
				root, _ := decodeIsolationValue(input)
				value, _, lookupErr := reviewBasisAt(root, pointer)
				if lookupErr != nil {
					t.Fatal(lookupErr)
				}
				quote = value.(string)
			case "/context/markdown":
				quote = "对象设想为该堂课自愿参加、能当堂独立手写或誊写修改稿的本科生，规模为拟约36人的单课堂便利样本"
			default:
				t.Fatal("incident source field changed", pointer)
			}
			var full strings.Builder
			parts := []ReviewSourceSegment{}
			for _, s := range catalog.Segments {
				if s.Pointer == pointer {
					full.WriteString(s.Text)
					parts = append(parts, s)
				}
			}
			pos := strings.Index(full.String(), quote)
			if pos < 0 {
				t.Fatal("explicit fixture selection not present in current source", pointer)
			}
			a := utf8.RuneCountInString(full.String()[:pos])
			b := a + utf8.RuneCountInString(quote)
			ids := []string{}
			for _, s := range parts {
				if s.Start < b && s.End > a {
					ids = append(ids, s.ID)
				}
			}
			for k := range basis {
				delete(basis, k)
			}
			basis["sourceSegmentIds"], basis["startOffset"], basis["endOffset"] = ids, a, b
		}
		selected := mustJSON(wire)
		copied, err := catalog.ResolveSubmission(string(selected), ReviewSourceSubmissionSchema(ReviewSubmissionSchema(node.OutputSchema)))
		if err != nil {
			t.Fatal(err)
		}
		got, _, err := normalizeWorkflowAIStageSubmissionForInput(string(copied), node, input)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateWorkflowAIStageOutput(node, got, input); err != nil {
			t.Fatal("full original scientific/history validation still rejects the scripted source choices", err)
		}
		before, after := decodeObject(original), decodeObject(copied)
		delete(before["reviewFindings"].([]any)[0].(map[string]any), "basis")
		delete(after["reviewFindings"].([]any)[0].(map[string]any), "basis")
		if !equalRevisionEditBase(mustJSON(before), mustJSON(after)) || after["approved"] != false {
			t.Fatal("source resolution altered scientific decisions or correction text")
		}
		if strings.Contains(string(selected), `"quote"`) {
			t.Fatal("first source submission still needs copied quotes")
		}
		t.Logf("turn=%s: explicit source selections resolved; complete original validation passes for a NEGATIVE review; all non-basis scientific fields retained; no model/DB writes", turn)
	}
}

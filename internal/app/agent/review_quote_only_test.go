package agent

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
)

func TestCapturedReviewQuoteOnlyControlReplay(t *testing.T) {
	// Recompute the workflow capture test first on this checkout: receipts
	// bind the captured data, not every future version of the validator code.
	dir := os.Getenv("SCIAIDE_QUOTE_ONLY_REPLAY")
	if dir == "" {
		t.Skip("no read-only incident capture supplied")
	}
	schema, err := os.ReadFile(filepath.Join(dir, "review6-capture-schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.ReadFile(filepath.Join(dir, "review6-capture-input.json"))
	if err != nil {
		t.Fatal(err)
	}
	node, err := os.ReadFile(filepath.Join(dir, "review6-capture-node.json"))
	if err != nil {
		t.Fatal(err)
	}
	digest := func(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
	for _, turn := range []string{"2", "3", "4"} {
		base, err := os.ReadFile(filepath.Join(dir, "review6-capture-turn-"+turn+".json"))
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "derived-review6-quote-diagnostic-"+turn+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var proof struct {
			Version, InputSHA256, CandidateSHA256, NodeSHA256, SchemaSHA256 string
			FullValidationQuoteOnly                                         bool
			Diagnostic                                                      workflow.ReviewBasisErrors
		}
		if json.Unmarshal(data, &proof) != nil || proof.Version != "complete-review-replay-v1" || !proof.FullValidationQuoteOnly || proof.InputSHA256 != digest(input) || proof.CandidateSHA256 != digest(base) || proof.NodeSHA256 != digest(node) || proof.SchemaSHA256 != digest(schema) {
			t.Fatal("invalid derived diagnostic")
		}
		r := newReviewRepair(string(base), schema, &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: &proof.Diagnostic})
		if r == nil || !r.quoteOnly || strings.Contains(string(r.schema), "reviewIssueReplacements") {
			t.Fatal("actual captured quote-only rejection did not expose copying unit", turn)
		}
		t.Logf("turn=%s: bound current-source copying control available; full replacement still allowed, no model/DB", turn)
	}
}

func quoteOnlyIssueFixture(t *testing.T) (json.RawMessage, string, *workflow.ReviewBasisErrors) {
	t.Helper()
	schema, base := issueRepairFixture(t)
	base = strings.Replace(base, "Exact current report", "当前誉写文本", 1)
	var root map[string]any
	_ = json.Unmarshal([]byte(base), &root)
	fs := root["reviewFindings"].([]any)
	first := fs[0].(map[string]any)
	first["basis"] = append(first["basis"].([]any), map[string]any{"pointer": "/methodContext/value", "quote": "Unchanged second basis"})
	var other map[string]any
	_ = json.Unmarshal(mustAgentJSON(t, first), &other)
	other["id"], other["summary"] = "issue-1-2", "Unchanged second finding"
	other["basis"].([]any)[0].(map[string]any)["quote"] = "Unchanged quote"
	root["reviewFindings"] = append(fs, other)
	base = string(mustAgentJSON(t, root))
	diagnostic := &workflow.ReviewBasisErrors{QuoteOnly: true, Problems: []string{"quote does not match current source"}, RepairPaths: []string{"/reviewFindings/0/basis/0/pointer", "/reviewFindings/0/basis/0/quote"}, Sources: []workflow.ReviewBasisSource{{Cell: "/reviewFindings/0/basis/0/quote", Pointer: "/context/markdown", InputSHA256: "current-input", Segments: []workflow.ReviewBasisSegment{{ID: "source-1", Start: 0, End: 6, Text: "当前誊写文本"}}}}}
	return schema, base, diagnostic
}

func TestIssueQuoteOnlyUsesCopyingUnitWithoutChangingJudgment(t *testing.T) {
	schema, base, diagnostic := quoteOnlyIssueFixture(t)
	r := newReviewRepair(base, schema, diagnostic)
	if r == nil || !r.quoteOnly || strings.Contains(string(r.schema), "reviewIssueReplacements") || strings.Contains(string(r.schema), "reviewUpdates") || !strings.Contains(r.instruction(), "ONLY diagnosed defects") {
		t.Fatal("quote-only repair still asks for issue regeneration")
	}
	patch := issuePatch(r, map[string]any{"reviewBasisSelections": []any{map[string]any{"path": "/reviewFindings/0/basis/0/quote", "sourceSegmentIds": []string{"source-1"}}}})
	merged, err := r.merge(patch)
	if err != nil || !strings.Contains(merged, "当前誊写文本") || !strings.Contains(merged, `"approved":false`) || strings.Contains(merged, "当前誉写文本") {
		t.Fatal("quote copying changed judgment or regenerated the rare glyph", err)
	}
	var original, final map[string]any
	_ = json.Unmarshal(r.base, &original)
	_ = json.Unmarshal([]byte(merged), &final)
	final["reviewFindings"].([]any)[0].(map[string]any)["basis"].([]any)[0].(map[string]any)["quote"] = "当前誉写文本"
	if !reflect.DeepEqual(final, original) {
		t.Fatal("quote copying changed a decision, finding identity, plan, pointer or unselected basis")
	}
	if _, err := r.merge(issuePatch(r, map[string]any{"reviewUpdates": map[string]any{"approved": true}})); err == nil {
		t.Fatal("quote-only control accepted a decision update")
	}
	// The full-result control is still offered for substantive corrections.
	testNativeCorrectionControl(t, base, schema, &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: diagnostic}, patch, merged, r.schema)
}

func mustAgentJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestIssueQuoteOnlyNeverNarrowsMixedUncoveredOrTruncatedDiagnostics(t *testing.T) {
	schema, base, diagnostic := quoteOnlyIssueFixture(t)
	for _, mode := range []string{"mixed", "uncovered", "pointer", "truncated", "legacy_diagnostic", "joined_pointer_diagnostic"} {
		t.Run(mode, func(t *testing.T) {
			d := *diagnostic
			d.RepairPaths = append([]string(nil), diagnostic.RepairPaths...)
			var err error = &d
			switch mode {
			case "mixed":
				err = errors.Join(err, errors.New("scientific judgment needs correction"))
			case "uncovered":
				d.RepairPaths = append(d.RepairPaths, "/reviewFindings/0/basis/1/quote")
			case "pointer":
				d.QuoteOnly = false
				d.RepairPaths = append(d.RepairPaths, "/reviewFindings/0/basis/0/pointer")
			case "truncated":
				d.Truncated = true
			case "legacy_diagnostic":
				d.QuoteOnly = false
			case "joined_pointer_diagnostic":
				err = errors.Join(err, &workflow.ReviewBasisErrors{RepairPaths: append([]string(nil), d.RepairPaths...), Sources: d.Sources})
			}
			r := newReviewRepair(base, schema, err)
			if r == nil || r.quoteOnly || !strings.Contains(string(r.schema), "reviewIssueReplacements") || !strings.Contains(string(r.schema), "reviewUpdates") {
				t.Fatal("mixed or incomplete diagnostics lost their explicit issue repair")
			}
		})
	}
}

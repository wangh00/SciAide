package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func sourceSubmissionBinding(input json.RawMessage) ReviewSourceBinding {
	return ReviewSourceBinding{StageMaterialRequest: StageMaterialRequest{ProjectID: "project", ConversationID: "conversation", WorkflowRunID: "workflow", WorkflowStepID: "review", ChatRunID: "chat", Attempt: 1, InputSHA256: hashJSON(input)}, ExecutionID: "execution", PromptVersion: "dynamic-independent-review-v9|" + ReviewSourceProtocolVersion, OutputSchemaSHA256: "schema"}
}

func TestInitialReviewSourcesTranslateOnlySelectedCellsToCurrentChat(t *testing.T) {
	old := citation.KnowledgeReference("workflow", "index", "chunk", strings.Repeat("a", 64))
	current := citation.KnowledgeReference("chat", "index", "chunk", strings.Repeat("a", 64))
	pointer := "/context/key~1" + old
	input := mustJSON(map[string]any{"context": map[string]any{"key/" + old: "原誊写引用 " + old}})
	c, err := BuildReviewSourceCatalog(t.Context(), input, sourceSubmissionBinding(input))
	if err != nil {
		t.Fatal(err)
	}
	c.CitationAliases = map[string]string{current: old}
	before := string(mustJSON(c))
	_, schema := sourceCatalogFixture(t)
	wire := sourcePayload(t, map[string]any{"sourceSegmentIds": []string{sourceSegmentFor(t, c, pointer).ID}})
	got, err := c.ResolveSubmission(wire, ReviewSourceSubmissionSchema(schema))
	if err != nil || !strings.Contains(string(got), `"pointer":"/context/key~1`+current+`"`) || !strings.Contains(string(got), `"quote":"原誊写引用 `+current+`"`) {
		t.Fatal("selected quote/key not in current Chat citation domain", err, string(got))
	}
	if before != string(mustJSON(c)) || !strings.Contains(string(input), old) || strings.Contains(wire, current) {
		t.Fatal("alias conversion rewrote original input/catalog/provider payload")
	}
	canonical := revisionEditAliasReplacer(c.CitationAliases, false).Replace(string(got))
	if !strings.Contains(canonical, pointer) || !strings.Contains(canonical, "原誊写引用 "+old) {
		t.Fatal("original host citation domain cannot be restored")
	}
}

func sourceCatalogFixture(t *testing.T) (*ReviewSourceCatalog, json.RawMessage) {
	t.Helper()
	input := raw(`{"context":{"markdown":"原文誊写😀不得改字","n":9007199254740993,"exp":1.25e+30,"negative":-3,"no":false,"yes":true,"null":null,"empty":"  ","a/~b":"escaped"},"_userRevision":{"changes":["要求誊写"],"priorOutput":{"secret":"不开放历史"}},"reviewHistory":{"findings":[{"quote":"不开放旧意见"}]},"_reviewCorrections":{"priorOutput":{"content":"旧稿"}}}`)
	c, err := BuildReviewSourceCatalog(t.Context(), input, sourceSubmissionBinding(input))
	if err != nil {
		t.Fatal(err)
	}
	return c, raw(`{"type":"object","additionalProperties":false,"required":["approved","reviewFindings","n"],"properties":{"approved":{"type":"boolean"},"n":{"type":"integer"},"reviewFindings":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","basis"],"properties":{"id":{"type":"string"},"basis":{"type":"array","minItems":1,"maxItems":4,"items":{"type":"object","additionalProperties":false,"required":["pointer","quote"],"properties":{"pointer":{"type":"string"},"quote":{"type":"string","maxLength":2000}}}}}}}}}`)
}

func sourceSegmentFor(t *testing.T, c *ReviewSourceCatalog, pointer string) ReviewSourceSegment {
	t.Helper()
	for _, s := range c.Segments {
		if s.Pointer == pointer {
			return s
		}
	}
	t.Fatal("missing source", pointer)
	return ReviewSourceSegment{}
}

func sourcePayload(t *testing.T, selection map[string]any) string {
	t.Helper()
	return string(mustJSON(map[string]any{"approved": false, "n": json.Number("9007199254740993"), "reviewFindings": []any{map[string]any{"id": "issue-1-1", "basis": []any{selection}}}}))
}

func TestInitialReviewSourcesCopyRareRuneAndPreserveAllOtherValues(t *testing.T) {
	c, schema := sourceCatalogFixture(t)
	seg := sourceSegmentFor(t, c, "/context/markdown")
	wire := ReviewSourceSubmissionSchema(schema)
	if err := (tool.JSONSchemaValidator{}).ValidateSchema(wire); err != nil {
		t.Fatal(err)
	}
	text := sourcePayload(t, map[string]any{"sourceSegmentIds": []string{seg.ID}, "startOffset": 2, "endOffset": 4})
	got, err := c.ResolveSubmission(text, wire)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"quote":"誊写"`) || !strings.Contains(string(got), "9007199254740993") || !strings.Contains(string(got), `"approved":false`) || strings.Contains(string(got), "sourceSegmentIds") {
		t.Fatal("wrong source copy or metadata drift", string(got))
	}
	if err := (tool.JSONSchemaValidator{}).Validate(schema, got); err != nil {
		t.Fatal(err)
	}
	for _, s := range c.Segments {
		if strings.Contains(s.Pointer, "reviewHistory") || strings.Contains(s.Pointer, "priorOutput") || strings.Contains(s.Pointer, "_reviewCorrections") {
			t.Fatal("history entered current copying catalog")
		}
	}
	if !sourceSegmentFor(t, c, "/_userRevision/changes/0").Requirement || sourceSegmentFor(t, c, "/context/markdown").Requirement {
		t.Fatal("requirement/evidence role changed")
	}
	if sourceSegmentFor(t, c, "/context/a~1~0b").Text != "escaped" {
		t.Fatal("RFC6901 escaping changed")
	}
}

func TestInitialReviewSourcesKeepNumberAndBooleanEvidenceWhole(t *testing.T) {
	c, schema := sourceCatalogFixture(t)
	wire := ReviewSourceSubmissionSchema(schema)
	for pointer, want := range map[string]string{"/context/n": "9007199254740993", "/context/exp": "1.25e+30", "/context/negative": "-3", "/context/no": "false", "/context/yes": "true"} {
		s := sourceSegmentFor(t, c, pointer)
		got, err := c.ResolveSubmission(sourcePayload(t, map[string]any{"sourceSegmentIds": []string{s.ID}}), wire)
		if err != nil || !strings.Contains(string(got), fmt.Sprintf(`"quote":%q`, want)) {
			t.Fatal("scalar evidence changed", pointer, err, string(got))
		}
		if _, err := c.ResolveSubmission(sourcePayload(t, map[string]any{"sourceSegmentIds": []string{s.ID}, "startOffset": 1, "endOffset": len(want)}), wire); err == nil {
			t.Fatal("partial non-text evidence accepted")
		}
	}
}

func TestInitialReviewSourcesRejectMixedStaleGappedAndInvalidRanges(t *testing.T) {
	c, schema := sourceCatalogFixture(t)
	s := sourceSegmentFor(t, c, "/context/markdown")
	other := sourceSegmentFor(t, c, "/context/n")
	wire := ReviewSourceSubmissionSchema(schema)
	for _, selection := range []map[string]any{
		{"sourceSegmentIds": []string{s.ID}, "quote": "guessed"},
		{"pointer": s.Pointer, "quote": s.Text},
		{"sourceSegmentIds": []string{s.ID, s.ID}},
		{"sourceSegmentIds": []string{s.ID, other.ID}},
		{"sourceSegmentIds": []string{"r_" + strings.Repeat("a", 64) + "_1"}},
		{"sourceSegmentIds": []string{s.ID}, "startOffset": 0},
		{"sourceSegmentIds": []string{s.ID}, "startOffset": 0, "endOffset": int64(999999999999999999)},
		{"sourceSegmentIds": []string{s.ID}, "startOffset": 4, "endOffset": 3},
	} {
		if got, err := c.ResolveSubmission(sourcePayload(t, selection), wire); err == nil || got != nil {
			t.Fatal("invalid source selection accepted", selection)
		}
	}
	input := raw(`{"context":{"markdown":"same"}}`)
	a, _ := BuildReviewSourceCatalog(t.Context(), input, sourceSubmissionBinding(input))
	b := sourceSubmissionBinding(input)
	b.Attempt++
	newer, _ := BuildReviewSourceCatalog(t.Context(), input, b)
	if a.Segments[0].ID == newer.Segments[0].ID {
		t.Fatal("old-attempt IDs were reused")
	}
	bad := strings.Replace(sourcePayload(t, map[string]any{"sourceSegmentIds": []string{s.ID}}), `"approved":false`, `"approved":true,"approved":false`, 1)
	if _, err := c.ResolveSubmission(bad, wire); err == nil {
		t.Fatal("duplicate JSON keys accepted")
	}
}

func TestInitialReviewSourcesCoverLongTextAndAllowExactContinuousSubstrings(t *testing.T) {
	text := strings.Repeat("甲", 398) + "😀誊写" + strings.Repeat("乙", 600)
	input := mustJSON(map[string]any{"context": map[string]any{"markdown": text}})
	c, err := BuildReviewSourceCatalog(t.Context(), input, sourceSubmissionBinding(input))
	if err != nil {
		t.Fatal(err)
	}
	var joined strings.Builder
	for _, s := range c.Segments {
		joined.WriteString(s.Text)
	}
	if joined.String() != text || len(c.Segments) != 3 {
		t.Fatal("source was previewed, lost or not fully covered")
	}
	_, schema := sourceCatalogFixture(t)
	wire := ReviewSourceSubmissionSchema(schema)
	selection := map[string]any{"sourceSegmentIds": []string{c.Segments[0].ID, c.Segments[1].ID}, "startOffset": 398, "endOffset": 401}
	got, err := c.ResolveSubmission(sourcePayload(t, selection), wire)
	if err != nil || !strings.Contains(string(got), `"quote":"😀誊写"`) {
		t.Fatal("Unicode selection across adjacent segments changed", string(got), err)
	}
	for _, ids := range [][]string{{c.Segments[0].ID, c.Segments[2].ID}, {c.Segments[1].ID, c.Segments[0].ID}} {
		if _, err := c.ResolveSubmission(sourcePayload(t, map[string]any{"sourceSegmentIds": ids}), wire); err == nil {
			t.Fatal("gapped/reordered source copied")
		}
	}
}

func TestInitialReviewSourcesEmptyCatalogDoesNotInventAProblem(t *testing.T) {
	input := raw(`{"context":{"markdown":"  ","nothing":null},"reviewHistory":{"text":"old"}}`)
	c, err := BuildReviewSourceCatalog(t.Context(), input, sourceSubmissionBinding(input))
	if err != nil || len(c.Segments) != 0 {
		t.Fatal("empty source catalog was fabricated", err)
	}
	_, schema := sourceCatalogFixture(t)
	text := `{"approved":true,"n":0,"reviewFindings":[]}`
	if got, err := c.ResolveSubmission(text, ReviewSourceSubmissionSchema(schema)); err != nil || !strings.Contains(string(got), `"approved":true`) {
		t.Fatal("empty honest result was blocked by source transport", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := BuildReviewSourceCatalog(ctx, input, sourceSubmissionBinding(input)); err == nil {
		t.Fatal("cancelled source build continued")
	}
}

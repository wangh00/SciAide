package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
)

func sourceSegmentPatch(r *citationRepair, reference string, ids ...string) string {
	data, _ := json.Marshal(map[string]any{"baseSHA256": r.hash, "citationQuoteSelections": []any{map[string]any{"reference": reference, "sourceSegmentIds": ids}}})
	return string(data)
}

func citationSegmentFixture(t *testing.T) (*citationRepair, workflow.CitationQuoteError) {
	t.Helper()
	source := "First result is 1. " + strings.Repeat("Intervening source explanation. ", 35) + "Last result is 2."
	issue := workflow.CitationQuoteError{Reference: "[K-A]", SupportingQuote: "invented prefix. Exact evidence.", CandidateQuote: source}
	r := newCitationRepair(quoteRepairBase, quoteRepairSchema, &issue)
	if r == nil || len(r.sources) != 1 || len(r.sources[0].Segments) < 3 {
		t.Fatal("missing full source segment menu")
	}
	return r, issue
}

func TestCitationSourceSelectionIsExactScopedAndAtomic(t *testing.T) {
	r, issue := citationSegmentFixture(t)
	segments := r.sources[0].Segments
	joined := ""
	for _, segment := range segments {
		joined += segment.Text
	}
	if joined != issue.CandidateQuote {
		t.Fatal("source segmentation omitted or modified original text")
	}
	patch := sourceSegmentPatch(r, r.reference, segments[0].ID)
	for _, schema := range []json.RawMessage{r.schema, r.schema} {
		if err := (tool.JSONSchemaValidator{}).Validate(schema, json.RawMessage(patch)); err != nil {
			t.Fatal("selector patch not valid in transport contract", err)
		}
	}
	merged, err := r.merge("```json\n" + patch + "\n```")
	if err != nil {
		t.Fatal(err)
	}
	decode := func(text string) map[string]any {
		var value map[string]any
		d := json.NewDecoder(bytes.NewBufferString(text))
		d.UseNumber()
		if err := d.Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before, after := decode(quoteRepairBase), decode(merged)
	wanted := strings.TrimSpace(segments[0].Text)
	before["citationAssessments"].([]any)[0].(map[string]any)["supportingQuote"] = wanted
	if !reflect.DeepEqual(before, after) || strings.Contains(merged, "sourceSegmentIds") || !strings.Contains(issue.CandidateQuote, wanted) {
		t.Fatal("selector changed non-quote fields or escaped into frozen result", merged)
	}
	for name, candidate := range map[string]string{
		"skipping":  sourceSegmentPatch(r, r.reference, segments[0].ID, segments[2].ID),
		"duplicate": sourceSegmentPatch(r, r.reference, segments[0].ID, segments[0].ID),
		"reversed":  sourceSegmentPatch(r, r.reference, segments[1].ID, segments[0].ID),
		"unknown":   sourceSegmentPatch(r, r.reference, "q_not_issued"),
		"wrong ref": sourceSegmentPatch(r, "[K-B]", segments[0].ID),
		"stale":     strings.Replace(patch, r.hash, "stale", 1),
		"both":      strings.Replace(patch, `"reference":`, `"supportingQuote":"Exact evidence.","reference":`, 1),
		"neither":   strings.Replace(patch, `"sourceSegmentIds":["`+segments[0].ID+`"]`, `"sourceSegmentIds":[]`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := r.merge(candidate); err == nil {
				t.Fatal("invalid selector patch accepted", candidate)
			}
			if string(r.base) != quoteRepairBase {
				t.Fatal("invalid patch partially mutated base")
			}
		})
	}
	// Same excerpt does not make two citation markers interchangeable.
	other := issue
	other.Reference = "[K-B]"
	other.SupportingQuote = "Other evidence."
	multi := newCitationRepairs(quoteRepairBase, quoteRepairSchema, []workflow.CitationQuoteError{issue, other})
	if multi.sources[0].Segments[0].ID == multi.sources[1].Segments[0].ID {
		t.Fatal("segment IDs are not bound to the citation marker")
	}
	if _, err := multi.merge(sourceSegmentPatch(multi, "[K-A]", multi.sources[1].Segments[0].ID)); err == nil {
		t.Fatal("another citation source's selector was accepted")
	}
	atomicBad, _ := json.Marshal(map[string]any{"baseSHA256": multi.hash, "citationQuoteSelections": []any{
		map[string]any{"reference": "[K-A]", "sourceSegmentIds": []string{multi.sources[0].Segments[0].ID}},
		map[string]any{"reference": "[K-B]", "sourceSegmentIds": []string{multi.sources[0].Segments[0].ID}},
	}})
	if _, err := multi.merge(string(atomicBad)); err == nil || string(multi.base) != quoteRepairBase {
		t.Fatal("one valid selection partially applied before a foreign source failed")
	}
	// Updating the source invalidates its old selectors even with a fresh base.
	changed := issue
	changed.CandidateQuote += " Source changed."
	fresh := newCitationRepair(quoteRepairBase, quoteRepairSchema, &changed)
	if _, err := fresh.merge(sourceSegmentPatch(fresh, fresh.reference, segments[0].ID)); err == nil {
		t.Fatal("selector survived a source change")
	}
}

func TestCitationSourceSelectionRetainsLengthAndFullResultFallback(t *testing.T) {
	r, _ := citationSegmentFixture(t)
	segments := r.sources[0].Segments
	if _, err := r.merge(sourceSegmentPatch(r, r.reference, segments[0].ID, segments[1].ID)); err == nil || !strings.Contains(err.Error(), "700-character") {
		t.Fatal("long contiguous selection bypassed frozen quote limit", err)
	}
	schema := bytes.Replace(quoteRepairSchema, []byte(`"supportingQuote":{"type":"string"}`), []byte(`"supportingQuote":{"type":"string","maxLength":40}`), 1)
	narrow := newCitationRepair(quoteRepairBase, schema, &workflow.CitationQuoteError{Reference: "[K-A]", CandidateQuote: "Long continuous evidence sentence exceeding forty characters."})
	if narrow == nil || narrow.quoteLimit != 40 {
		t.Fatal("frozen smaller limit not preserved")
	}
	if _, err := narrow.merge(sourceSegmentPatch(narrow, narrow.reference, narrow.sources[0].Segments[0].ID)); err != nil {
		t.Fatal("smaller frozen limit has no usable segment", err)
	}
	if _, err := narrow.merge(sourceSegmentPatch(narrow, narrow.reference, narrow.sources[0].Segments[0].ID, narrow.sources[0].Segments[1].ID)); err == nil {
		t.Fatal("combined selection bypassed smaller original quote limit")
	}
	if got, err := r.merge(quoteRepairBase); err != nil || got != quoteRepairBase {
		t.Fatal("complete scientific correction fallback changed")
	}
	large := newCitationRepair(quoteRepairBase, quoteRepairSchema, &workflow.CitationQuoteError{Reference: "[K-A]", CandidateQuote: strings.Repeat("Source text. ", 10000)})
	if large == nil || len(large.sources) != 0 || strings.Contains(large.instruction(), "sourceSegmentIds") {
		t.Fatal("oversized source created a truncated or unbounded menu")
	}
	if _, err := large.merge(quotePatch(large, "Source text.")); err != nil {
		t.Fatal("bounded menu fallback blocked exact text repair", err)
	}
	// A multi-source menu stays bounded and never silently presents a truncated
	// source, even when the individual source bodies each fit the byte ceiling.
	body := strings.Repeat("Complete source sentence. ", 800)
	multi := newCitationRepairs(quoteRepairBase, quoteRepairSchema, []workflow.CitationQuoteError{{Reference: "[K-A]", CandidateQuote: body}, {Reference: "[K-B]", CandidateQuote: body}})
	menu, _ := json.Marshal(multi.sources)
	if len(menu) > 32<<10 || len(multi.sources) != 1 {
		t.Fatal("aggregate menu exceeds its bounded presentation allowance")
	}
	var joined strings.Builder
	for _, segment := range multi.sources[0].Segments {
		joined.WriteString(segment.Text)
	}
	if joined.String() != body {
		t.Fatal("aggregate budget silently truncated an offered source")
	}
}

func TestCitationSourceSelectionConsecutiveSegmentsKeepInterveningText(t *testing.T) {
	source := strings.Repeat("a", 278) + " 1. \r\n" + strings.Repeat("b", 278) + " −1.5.\n" + strings.Repeat("c", 278) + "."
	r := newCitationRepair(quoteRepairBase, quoteRepairSchema, &workflow.CitationQuoteError{Reference: "[K-A]", CandidateQuote: source})
	if r == nil || len(r.sources[0].Segments) != 3 {
		t.Fatal("missing three consecutive source segments")
	}
	segments := r.sources[0].Segments
	merged, err := r.merge(sourceSegmentPatch(r, r.reference, segments[0].ID, segments[1].ID))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Assessments []struct {
			Quote string `json:"supportingQuote"`
		} `json:"citationAssessments"`
	}
	if json.Unmarshal([]byte(merged), &out) != nil || out.Assessments[0].Quote != strings.TrimSpace(segments[0].Text+segments[1].Text) || !strings.Contains(out.Assessments[0].Quote, "1. \r\n") || !strings.Contains(out.Assessments[0].Quote, "−1.5") {
		t.Fatal("consecutive merge changed intervening bytes or scientific symbols", merged)
	}
}

func TestCitationSourceSelectionRepeatingRejectedQuoteUsesOriginalBudget(t *testing.T) {
	r, issue := citationSegmentFixture(t)
	noChange := quotePatch(r, issue.SupportingQuote)
	_, err := r.merge(noChange)
	var quoteErr *workflow.CitationQuoteError
	if err == nil || !strings.Contains(err.Error(), "unchanged rejectedSupportingQuote") || !errors.As(err, &quoteErr) {
		t.Fatal("unchanged invalid quote treated as corrected candidate", err)
	}
	good := sourceSegmentPatch(r, r.reference, r.sources[0].Segments[0].ID)
	merged, err := r.merge(good)
	if err != nil {
		t.Fatal(err)
	}
	for _, succeed := range []bool{true, false} {
		last := good
		if !succeed {
			last = noChange
		}
		loop, state, provider := newLoopFixture(t, nil, submissionScript(quoteRepairBase), submissionScript(noChange), submissionScript(last))
		state.workflowAI = true
		host := &submissionHostFixture{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: quoteRepairSchema}}, accepted: json.RawMessage(merged), errors: []error{&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: &issue}}}
		loop.research = host
		got := loop.Run(context.Background(), state.run.ID)
		if len(provider.Requests()) != 3 || state.journalDrafts[2] != noChange || len(state.calls) != 0 {
			t.Fatal("no-op repair enlarged budget, lost audit or reran a tool")
		}
		feedback := ""
		for _, m := range provider.Requests()[2].Messages {
			feedback += m.Content + "\n"
		}
		if !strings.Contains(feedback, "unchanged rejectedSupportingQuote") || !strings.Contains(feedback, "sourceSegmentIds") || !strings.Contains(feedback, r.hash) {
			t.Fatal("second correction lost no-op diagnosis or original base", feedback)
		}
		if succeed && (got != OutcomeCompleted || len(host.validated) != 2 || host.validated[1][3] != merged) {
			t.Fatal("selector did not reach full host validation")
		}
		if !succeed && (got != OutcomeFailed || len(host.validated) != 1 || state.run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || state.messages[1].Parts[0].Text != "") {
			t.Fatal("unchanged invalid result accepted or continued beyond budget")
		}
	}
}

func TestCitationSourceSelectionDedicatedSubmissionStillUsesHostValidator(t *testing.T) {
	r, issue := citationSegmentFixture(t)
	patch := sourceSegmentPatch(r, r.reference, r.sources[0].Segments[0].ID)
	merged, _ := r.merge(patch)
	correction := submissionToolScript(patch)
	correction[0].Event.ToolCall.Name = stageRepairTool
	loop, state, provider := newLoopFixture(t, nil, submissionToolScript(quoteRepairBase), correction)
	state.workflowAI = true
	state.run.ModelTurns = 12
	loop.resources = &resourceViewFixture{}
	guidance := &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: quoteRepairSchema, SkillDiscovery: true, AllowedToolNames: []string{"builtin.resource.open"}}}
	host := &submissionHostFixture{staticResearchGuidance: guidance, accepted: json.RawMessage(merged), errors: []error{&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: &issue}}}
	loop.research = host
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeCompleted || len(provider.Requests()) != 2 || len(host.validated) != 2 || host.validated[1][3] != merged {
		t.Fatal("dedicated selector bypassed full validation", got)
	}
	req := provider.Requests()[1]
	if req.ForcedTool != "" || len(req.Tools) != 2 || state.journalDrafts[state.run.ModelTurns] != patch {
		t.Fatal("selector escaped dedicated submission or lost raw audit")
	}
	if err := (tool.JSONSchemaValidator{}).Validate(req.Tools[1].InputSchema, json.RawMessage(patch)); err != nil {
		t.Fatal("actual dedicated tool schema omitted selector", err)
	}
}

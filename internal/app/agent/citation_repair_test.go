package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
)

// Captured records and manually selected replay spans are never checked in.
// This verifies the correction protocol, not a real model's scientific choices.
func TestCapturedBatchQuoteRepairProtocol(t *testing.T) {
	dir := os.Getenv("SCIAIDE_MULTI_QUOTE_REPLAY_DIR")
	if dir == "" {
		t.Skip("no selected multi-quote capture")
	}
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	base, schema := string(read("derived-merged-turn-3.json")), json.RawMessage(read("schema.json"))
	var issues workflow.CitationQuoteErrors
	if err := json.Unmarshal(read("derived-issues-derived-merged-turn-3.json.json"), &issues); err != nil {
		t.Fatal(err)
	}
	r := newCitationRepairs(base, schema, issues.Issues)
	if r == nil {
		t.Fatal("capture could not bind all rejected references")
	}
	var quotes []map[string]any
	if err := json.Unmarshal(read("manual-replay-quotes.json"), &quotes); err != nil {
		t.Fatal(err)
	}
	patch, _ := json.Marshal(map[string]any{"baseSHA256": r.hash, "citationAssessmentsPatch": quotes})
	merged, err := r.merge("```json\n" + string(patch) + "\n```")
	if err != nil {
		t.Fatal(err)
	}
	if err := (tool.JSONSchemaValidator{}).Validate(schema, json.RawMessage(merged)); err != nil {
		t.Fatal(err)
	}
	decode := func(s string) map[string]any {
		var value map[string]any
		d := json.NewDecoder(bytes.NewBufferString(s))
		d.UseNumber()
		if err := d.Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before, after := decode(base), decode(merged)
	byRef := map[string]string{}
	for _, issue := range issues.Issues {
		byRef[issue.Reference] = issue.CandidateQuote
	}
	changed := 0
	for i, item := range before["citationAssessments"].([]any) {
		a, b := item.(map[string]any), after["citationAssessments"].([]any)[i].(map[string]any)
		if a["supportingQuote"] != b["supportingQuote"] {
			changed++
			if !strings.Contains(byRef[b["reference"].(string)], b["supportingQuote"].(string)) {
				t.Fatal("non-continuous or wrong-source replay quote")
			}
			a["supportingQuote"] = b["supportingQuote"]
		}
	}
	if changed != len(issues.Issues) || !reflect.DeepEqual(before, after) {
		t.Fatal("batch merge changed fields outside the rejected quotes")
	}
	loop, state, provider := newLoopFixture(t, nil, submissionScript(base), submissionScript("```json\n"+string(patch)+"\n```"))
	state.workflowAI = true
	host := &submissionHostFixture{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: schema}}, accepted: json.RawMessage(merged), errors: []error{&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Details: issues.Error(), Cause: &issues}}}
	loop.research = host
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeCompleted || len(provider.Requests()) != 2 || len(host.validated) != 2 || host.validated[1][3] != merged {
		t.Fatalf("captured batch loop: %s %s", got, state.run.ErrorMessage)
	}
	if err := os.WriteFile(filepath.Join(dir, "derived-batch-replay-result.json"), []byte(merged), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("captured final candidate repaired in one fixture turn; %d exact source spans manually selected only for replay; other scientific fields unchanged; no live model/DB", changed)
}

var quoteRepairSchema = json.RawMessage(`{"type":"object","required":["citationAssessments","count"],"properties":{"count":{"type":"integer"},"citationAssessments":{"type":"array","items":{"type":"object","required":["reference","supportingQuote"],"properties":{"reference":{"type":"string"},"supportingQuote":{"type":"string"}}}}}}`)

const quoteRepairBase = `{"count":9007199254740993,"citationAssessments":[{"reference":"[K-A]","supportingQuote":"invented prefix. Exact evidence."},{"reference":"[K-B]","supportingQuote":"Other evidence."}]}`

func quotePatch(r *citationRepair, quote string) string {
	return fmt.Sprintf(`{"baseSHA256":%q,"citationAssessmentsPatch":[{"reference":%q,"supportingQuote":%q}]}`, r.hash, r.reference, quote)
}

func TestCitationRepairStrictScope(t *testing.T) {
	r := newCitationRepair(quoteRepairBase, quoteRepairSchema, &workflow.CitationQuoteError{Reference: "[K-A]"})
	if r == nil {
		t.Fatal("missing repair")
	}
	patch := quotePatch(r, "Exact evidence.")
	if err := (tool.JSONSchemaValidator{}).ValidateSchema(r.submissionSchema(quoteRepairSchema)); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{patch, quoteRepairBase} {
		if err := (tool.JSONSchemaValidator{}).Validate(r.submissionSchema(quoteRepairSchema), json.RawMessage(candidate)); err != nil {
			t.Fatal(err)
		}
	}
	merged, err := r.merge(patch)
	if err != nil || !strings.Contains(merged, "9007199254740993") || !strings.Contains(merged, "Other evidence.") || strings.Contains(merged, "invented") {
		t.Fatalf("%s %v", merged, err)
	}
	for _, bad := range []string{
		strings.Replace(patch, r.hash, "wrong", 1), strings.Replace(patch, "[K-A]", "[K-B]", 1),
		strings.Replace(patch, `"supportingQuote":`, `"decision":"exclude","supportingQuote":`, 1),
		strings.Replace(patch, `}]}`, `},{"reference":"[K-A]","supportingQuote":"duplicate"}]}`, 1),
		strings.Replace(patch, `"baseSHA256":`, `"count":0,"baseSHA256":`, 1),
	} {
		for _, text := range []string{bad, "已修正摘录。\n```json\n" + bad + "\n```"} {
			if _, err := r.merge(text); err == nil {
				t.Fatalf("accepted %s", text)
			}
		}
	}
	if got, err := r.merge(quoteRepairBase); err != nil || got != quoteRepairBase {
		t.Fatal("full fallback changed")
	}
}

func TestCitationRepairAcceptsExistingStageJSONEnvelopes(t *testing.T) {
	r := newCitationRepair(quoteRepairBase, quoteRepairSchema, &workflow.CitationQuoteError{Reference: "[K-A]"})
	patch := quotePatch(r, "Exact evidence.")
	expected, err := r.merge(patch)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		"```json\n" + patch + "\n```",
		"已修正原句，其他结果不变。\n\n``` json\n" + patch + "\n```\n",
		"已修正原句。\n" + patch,
		"```json\n" + patch + "\n```\n摘录已修正，其他字段不变。",
	} {
		merged, err := r.merge(text)
		if err != nil || merged != expected {
			t.Fatalf("stage envelope was not merged: %s %v", merged, err)
		}
	}
	full := "完整结果。\n```json\n" + quoteRepairBase + "\n```"
	if got, err := r.merge(full); err != nil || got != full {
		t.Fatal("full-result fallback lost its original envelope")
	}
	for _, text := range []string{
		"```json\n" + patch + "\nunexpected suffix before closing fence\n```",
		"```json\n" + strings.TrimSuffix(patch, "}") + "\n```",
	} {
		if got, err := r.merge(text); err == nil && got == expected {
			t.Fatalf("malformed envelope accepted as patch: %q", text)
		}
	}
}

func TestCitationRepairFencedPatchAfterEarlierContentCorrection(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary","count","citationAssessments"],"properties":{"summary":{"type":"string","minLength":1},"count":{"type":"integer"},"citationAssessments":{"type":"array","items":{"type":"object","required":["reference","supportingQuote"],"properties":{"reference":{"type":"string"},"supportingQuote":{"type":"string"}}}}}}`)
	base := strings.Replace(quoteRepairBase, `{"count":`, `{"summary":"Already synthesized evidence.","count":`, 1)
	r := newCitationRepair(base, schema, &workflow.CitationQuoteError{Reference: "[K-A]"})
	patch := "```json\n" + quotePatch(r, "Exact evidence.") + "\n```"
	merged, err := r.merge(quotePatch(r, "Exact evidence."))
	if err != nil {
		t.Fatal(err)
	}
	loop, state, provider := newLoopFixture(t, nil, submissionScript(base), submissionScript(base), submissionScript(patch))
	state.workflowAI = true
	host := &submissionHostFixture{
		staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{WorkflowStepID: "step", StructuredOutputRequired: true, StructuredOutputSchema: schema}},
		accepted:               json.RawMessage(merged),
		errors: []error{
			&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Details: "reference belongs to another document"},
			&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: &workflow.CitationQuoteError{Reference: "[K-A]", CandidateQuote: "Exact evidence.", SupportingQuote: "invented prefix. Exact evidence."}},
		},
	}
	loop.research = host
	if out := loop.Run(context.Background(), state.run.ID); out != OutcomeCompleted {
		t.Fatalf("fenced quote patch failed after prior repair: %s %s", out, state.run.ErrorMessage)
	}
	if len(provider.Requests()) != 3 || len(host.validated) != 3 || host.validated[2][3] != merged || state.messages[1].Parts[0].Text != merged {
		t.Fatal("partial patch reached full host validator or completion")
	}
	if state.journalDrafts[3] != patch || len(state.calls) != 0 {
		t.Fatal("raw fenced patch was lost or research tools were repeated")
	}
	if err := (tool.JSONSchemaValidator{}).Validate(schema, json.RawMessage(host.validated[2][3])); err != nil {
		t.Fatalf("merged summary/full contract invalid: %v", err)
	}
}

func TestCitationRepairLoopAuditsPatchAndValidatesFullResult(t *testing.T) {
	r := newCitationRepair(quoteRepairBase, quoteRepairSchema, &workflow.CitationQuoteError{Reference: "[K-A]"})
	patch := quotePatch(r, "Exact evidence.")
	merged, _ := r.merge(patch)
	for _, mode := range []struct{ dedicated, fenced bool }{{false, false}, {false, true}, {true, false}} {
		dedicated, fenced := mode.dedicated, mode.fenced
		rawPatch := patch
		if fenced {
			rawPatch = "```json\n" + patch + "\n```"
		}
		loop, state, provider := newLoopFixture(t, nil, submissionScript(quoteRepairBase), submissionScript(rawPatch))
		state.workflowAI = true
		guidance := &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{WorkflowStepID: "step", StructuredOutputRequired: true, StructuredOutputSchema: quoteRepairSchema}}
		if dedicated {
			state.run.ModelTurns = 12
			loop.resources = &resourceViewFixture{}
			guidance.value.SkillDiscovery = true
			guidance.value.AllowedToolNames = []string{resource.OpenTool}
		}
		host := &submissionHostFixture{staticResearchGuidance: guidance, accepted: json.RawMessage(merged), errors: []error{&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: &workflow.CitationQuoteError{Reference: "[K-A]", CandidateQuote: "Exact evidence.", SupportingQuote: "invented prefix. Exact evidence."}}}}
		loop.research = host
		if out := loop.Run(context.Background(), state.run.ID); out != OutcomeCompleted {
			t.Fatalf("%s %s", out, state.run.ErrorMessage)
		}
		if len(host.validated) != 2 || host.validated[1][3] != merged || state.messages[1].Parts[0].Text != merged {
			t.Fatal("host/final did not receive merged result")
		}
		if state.journalDrafts[state.run.ModelTurns] != rawPatch {
			t.Fatal("raw patch lost from audit")
		}
		req := provider.Requests()[1]
		if dedicated {
			if req.ForcedTool != stageSubmissionTool || len(req.Tools) != 1 {
				t.Fatal("repair escaped submission interface")
			}
			if err := (tool.JSONSchemaValidator{}).Validate(req.Tools[0].InputSchema, json.RawMessage(patch)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestCitationRepairCannotBypassLaterBusinessFailure(t *testing.T) {
	r := newCitationRepair(quoteRepairBase, quoteRepairSchema, &workflow.CitationQuoteError{Reference: "[K-A]"})
	patch := quotePatch(r, "Exact evidence.")
	merged, _ := r.merge(patch)
	loop, state, provider := newLoopFixture(t, nil, submissionScript(quoteRepairBase), submissionScript(patch), submissionScript(merged))
	state.workflowAI = true
	bad := &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Details: "other source level is invalid"}
	host := &submissionHostFixture{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: quoteRepairSchema}}, errors: []error{&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: &workflow.CitationQuoteError{Reference: "[K-A]"}}, bad, bad}}
	loop.research = host
	if out := loop.Run(context.Background(), state.run.ID); out != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || len(provider.Requests()) != 3 {
		t.Fatalf("%s %+v", out, state.run)
	}
	if host.validated[1][3] != merged {
		t.Fatal("partial patch bypassed full host validation")
	}
}

func TestCitationBatchRepairChangesOnlyRejectedQuotes(t *testing.T) {
	base := strings.Replace(quoteRepairBase, `}]}`, `},{"reference":"[K-C]","supportingQuote":"Already correct."}]}`, 1)
	issues := []workflow.CitationQuoteError{{Reference: "[K-A]"}, {Reference: "[K-B]"}}
	r := newCitationRepairs(base, quoteRepairSchema, issues)
	if r == nil || len(r.references) != 2 {
		t.Fatal("missing batch repair")
	}
	patch := fmt.Sprintf(`{"baseSHA256":%q,"citationAssessmentsPatch":[{"reference":"[K-B]","supportingQuote":"Correct B."},{"reference":"[K-A]","supportingQuote":"Correct A."}]}`, r.hash)
	merged, err := r.merge("```json\n" + patch + "\n```")
	if err != nil || !strings.Contains(merged, "9007199254740993") || !strings.Contains(merged, "Already correct.") || !strings.Contains(merged, "Correct A.") || !strings.Contains(merged, "Correct B.") {
		t.Fatalf("batch merge: %s %v", merged, err)
	}
	if err := (tool.JSONSchemaValidator{}).Validate(r.submissionSchema(quoteRepairSchema), json.RawMessage(patch)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(patch, "[K-B]", "[K-C]", 1),
		strings.Replace(patch, "[K-B]", "[K-A]", 1),
		strings.Replace(patch, r.hash, "stale-base", 1),
		strings.Replace(patch, `"supportingQuote":"Correct B."`, `"supportingQuote":"Correct B.","decision":"exclude"`, 1),
	} {
		if _, err := r.merge(bad); err == nil {
			t.Fatalf("out-of-scope/stale/duplicate batch accepted: %s", bad)
		}
	}
	partial := quotePatch(r, "Correct A.")
	if got, err := r.merge(partial); err != nil || !strings.Contains(got, "Other evidence.") {
		t.Fatal("partial batch must preserve unrepaired quotes for full validation")
	}
	if got, err := r.merge(base); err != nil || got != base {
		t.Fatal("full result fallback lost")
	}
	for _, badIssues := range [][]workflow.CitationQuoteError{
		{{Reference: "[K-A]"}, {Reference: "[K-A]"}},
		{{Reference: "[K-NOT-OFFERED]"}},
	} {
		if newCitationRepairs(base, quoteRepairSchema, badIssues) != nil {
			t.Fatal("ambiguous/unknown repair binding accepted")
		}
	}
	if !strings.Contains(r.instruction(), "[K-B]") || !strings.Contains(r.instruction(), "Repair ALL") {
		t.Fatal("batch prompt still asks for only one quote")
	}
}

func TestCitationBatchRepairLoopRepairsAllInOneTurn(t *testing.T) {
	issues := &workflow.CitationQuoteErrors{Issues: []workflow.CitationQuoteError{
		{Reference: "[K-A]", SupportingQuote: "invented prefix. Exact evidence.", CandidateQuote: "Exact evidence."},
		{Reference: "[K-B]", SupportingQuote: "Other evidence.", CandidateQuote: "Exact second evidence."},
	}}
	r := newCitationRepairs(quoteRepairBase, quoteRepairSchema, issues.Issues)
	patch := fmt.Sprintf(`{"baseSHA256":%q,"citationAssessmentsPatch":[{"reference":"[K-A]","supportingQuote":"Exact evidence."},{"reference":"[K-B]","supportingQuote":"Exact second evidence."}]}`, r.hash)
	merged, err := r.merge(patch)
	if err != nil {
		t.Fatal(err)
	}
	for _, dedicated := range []bool{false, true} {
		first, next := submissionScript(quoteRepairBase), submissionScript("```json\n"+patch+"\n```")
		if dedicated {
			first, next = submissionToolScript(quoteRepairBase), submissionToolScript(patch)
		}
		loop, state, provider := newLoopFixture(t, nil, first, next)
		state.workflowAI = true
		guidance := &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{WorkflowStepID: "step", StructuredOutputRequired: true, StructuredOutputSchema: quoteRepairSchema}}
		if dedicated {
			state.run.ModelTurns = 12
			loop.resources = &resourceViewFixture{}
			guidance.value.SkillDiscovery = true
			guidance.value.AllowedToolNames = []string{resource.OpenTool}
		}
		host := &submissionHostFixture{staticResearchGuidance: guidance, accepted: json.RawMessage(merged), errors: []error{&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Details: issues.Error(), Cause: issues}}}
		loop.research = host
		if got := loop.Run(context.Background(), state.run.ID); got != OutcomeCompleted || len(provider.Requests()) != 2 || len(host.validated) != 2 || host.validated[1][3] != merged {
			t.Fatalf("batch did not finish in one correction: %s %s", got, state.run.ErrorMessage)
		}
		expectedRaw := "```json\n" + patch + "\n```"
		if dedicated {
			expectedRaw = patch
		}
		if state.messages[1].Parts[0].Text != merged || len(state.calls) != 0 || state.journalDrafts[state.run.ModelTurns] != expectedRaw {
			t.Fatal("lost raw patch, committed partial output or repeated tools")
		}
		req := provider.Requests()[1]
		feedback := ""
		for _, message := range req.Messages {
			feedback += message.Content + "\n"
		}
		for _, issue := range issues.Issues {
			if !strings.Contains(feedback, issue.Reference) || !strings.Contains(feedback, issue.CandidateQuote) {
				t.Fatal("batch feedback omitted a remaining issue")
			}
		}
		if dedicated && (req.ForcedTool != stageSubmissionTool || len(req.Tools) != 1) {
			t.Fatal("batch repair escaped dedicated submission")
		}
		if dedicated {
			if err := (tool.JSONSchemaValidator{}).Validate(req.Tools[0].InputSchema, json.RawMessage(patch)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestCitationBatchRepairPartialPatchDoesNotAcceptRemainingIssue(t *testing.T) {
	issues := &workflow.CitationQuoteErrors{Issues: []workflow.CitationQuoteError{
		{Reference: "[K-A]", CandidateQuote: "Exact evidence."},
		{Reference: "[K-B]", CandidateQuote: "Exact second evidence."},
	}}
	r := newCitationRepairs(quoteRepairBase, quoteRepairSchema, issues.Issues)
	partialPatch := quotePatch(r, "Exact evidence.")
	partial, err := r.merge(partialPatch)
	if err != nil {
		t.Fatal(err)
	}
	remaining := &workflow.CitationQuoteError{Reference: "[K-B]", CandidateQuote: "Exact second evidence."}
	next := newCitationRepair(partial, quoteRepairSchema, remaining)
	finalPatch := quotePatch(next, remaining.CandidateQuote)
	final, err := next.merge(finalPatch)
	if err != nil {
		t.Fatal(err)
	}
	for _, succeed := range []bool{true, false} {
		last := finalPatch
		hostErrors := []error{
			&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: issues},
			&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: remaining},
		}
		if !succeed {
			last = partial
			hostErrors = append(hostErrors, &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: remaining})
		}
		loop, state, provider := newLoopFixture(t, nil, submissionScript(quoteRepairBase), submissionScript(partialPatch), submissionScript(last))
		state.workflowAI = true
		host := &submissionHostFixture{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: quoteRepairSchema}}, accepted: json.RawMessage(final), errors: hostErrors}
		loop.research = host
		got := loop.Run(context.Background(), state.run.ID)
		if len(provider.Requests()) != 3 || len(host.validated) != 3 || host.validated[1][3] != partial {
			t.Fatal("partial batch bypassed full validation or changed the two-correction budget")
		}
		if succeed && (got != OutcomeCompleted || host.validated[2][3] != final) {
			t.Fatal("remaining issue could not be corrected against the latest full base")
		}
		if !succeed && (got != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || state.messages[1].Parts[0].Text != "") {
			t.Fatal("unrepaired partial batch was accepted or retried beyond the budget")
		}
	}
}

func TestCitationBatchFailureBannerKeepsDetailsOutOfConversation(t *testing.T) {
	issues := &workflow.CitationQuoteErrors{Issues: []workflow.CitationQuoteError{
		{Reference: "[K-A]", CandidateQuote: "PRIVATE SOURCE ONE", SupportingQuote: "REJECTED ONE"},
		{Reference: "[K-B]", CandidateQuote: "PRIVATE SOURCE TWO", SupportingQuote: "REJECTED TWO"},
	}}
	err := &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Details: issues.Error(), Cause: issues}
	banner, diagnostic := researchSubmissionFailureMessage(err), researchSubmissionDiagnostic(err)
	if !strings.Contains(banner, "多条引用") || !strings.Contains(banner, "无需新建任务") || len([]rune(banner)) > 200 {
		t.Fatal("batch banner is not concise and actionable")
	}
	for _, issue := range issues.Issues {
		if strings.Contains(banner, issue.CandidateQuote) || strings.Contains(banner, issue.SupportingQuote) || !strings.Contains(diagnostic, issue.CandidateQuote) || !strings.Contains(diagnostic, issue.SupportingQuote) {
			t.Fatal("source diagnostics leaked into banner or were lost from correction feedback")
		}
	}
}

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
)

func issueRepairFixture(t *testing.T) (json.RawMessage, string) {
	t.Helper()
	schema, err := os.ReadFile("../../bootstrap/testdata/review-kernel/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	base := `{"approved":false,"reviewedInputSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","verifiedClaims":[],"confidence":"low","limitations":[],"acceptanceChecks":[{"criterionId":"criterion-1","status":"not_met","basis":"Not verified"}],"suggestions":[],"reviewFindings":[{"id":"issue-1-1","status":"open","category":"methodIssues","summary":"Unverified","location":"Report","basis":[{"pointer":"/context/markdown","quote":"Exact current report"}],"reason":"Unverified method","origin":"existing","changeReason":"Still unverified","correction":"Verify method","revision":{"nodeId":"","reason":"Source needed","whyNotLater":"No repair stage","needsUserInput":true,"requiredInput":"Source"}}]}`
	return schema, base
}

func issuePatch(r *reviewRepair, fields map[string]any) string {
	fields["baseSHA256"] = r.hash
	b, _ := json.Marshal(fields)
	return string(b)
}

func TestIssueRepairDoesNotRequireModelToCopyHostInputDigest(t *testing.T) {
	schema, base := issueRepairFixture(t)
	var root map[string]any
	_ = json.Unmarshal([]byte(base), &root)
	delete(root, "reviewedInputSha256")
	text, _ := json.Marshal(root)
	r := newReviewRepair(string(text), schema, &workflow.ReviewMissingFindingError{ID: "issue-1-2"})
	if r == nil || !r.issues {
		t.Fatal("omitted host hash disabled local repair")
	}
	if strings.Contains(string(r.base), "reviewedInputSha256") {
		t.Fatal("repair invented host identity")
	}
	merged, err := r.merge(issuePatch(r, map[string]any{"reviewUpdates": map[string]any{"approved": false}}))
	if err != nil || strings.Contains(merged, "reviewedInputSha256") {
		t.Fatal("local repair cannot preserve omitted host field", err)
	}
}

func TestIssueRepairMissingIdentityPreservesOtherFindings(t *testing.T) {
	schema, base := issueRepairFixture(t)
	r := newReviewRepair(base, schema, &workflow.ReviewMissingFindingError{ID: "issue-1-2"})
	if r == nil || !r.issues {
		t.Fatal("missing issue cannot be repaired locally")
	}
	var root map[string]json.RawMessage
	_ = json.Unmarshal([]byte(base), &root)
	var fs []map[string]any
	_ = json.Unmarshal(root["reviewFindings"], &fs)
	added := fs[0]
	added["id"] = "issue-1-2"
	added["status"] = "withdrawn"
	delete(added, "correction")
	delete(added, "revision")
	patch := issuePatch(r, map[string]any{"reviewIssueReplacements": []any{added}})
	merged, err := r.merge(patch)
	if err != nil {
		t.Fatal(err)
	}
	var after map[string]json.RawMessage
	_ = json.Unmarshal([]byte(merged), &after)
	var beforeList, afterList []json.RawMessage
	_ = json.Unmarshal(root["reviewFindings"], &beforeList)
	_ = json.Unmarshal(after["reviewFindings"], &afterList)
	if len(afterList) != 2 || string(beforeList[0]) != string(afterList[0]) {
		t.Fatal("unchanged finding regenerated")
	}
	if _, _, err := workflow.NormalizeAIStageSubmission(merged, schema); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(patch, r.hash, "stale", 1), strings.Replace(patch, "issue-1-2", "issue-9-99", 1), issuePatch(r, map[string]any{"reviewIssueReplacements": []any{added, added}}), issuePatch(r, map[string]any{"reviewUpdates": map[string]any{"reviewFindings": []any{}}})} {
		if _, err := r.merge(bad); err == nil {
			t.Fatal("invalid patch accepted")
		}
	}
}

func TestIssueRepairMergeReceiptRequiresAppliedPatch(t *testing.T) {
	schema, base := issueRepairFixture(t)
	r := newReviewRepair(base, schema, &workflow.ReviewMissingFindingError{ID: "issue-1-2"})
	if r == nil || !r.issues {
		t.Fatal("issue repair unavailable")
	}
	patch := issuePatch(r, map[string]any{"reviewUpdates": map[string]any{"limitations": []string{"Scope retained"}}})
	for _, tt := range []struct {
		name, text       string
		applied, invalid bool
	}{
		{name: "bare_patch", text: patch, applied: true},
		{name: "fenced_patch", text: "```json\n" + patch + "\n```", applied: true},
		{name: "crlf_fenced_patch", text: " \r\n``` JSON\r\n" + patch + "\r\n```\t ", applied: true},
		{name: "full_result", text: base},
		{name: "fenced_full_result", text: "```json\n" + base + "\n```"},
		{name: "malformed", text: strings.TrimSuffix(patch, "}")},
		{name: "stale_hash", text: strings.Replace(patch, r.hash, strings.Repeat("b", 64), 1), invalid: true},
		{name: "empty_patch", text: issuePatch(r, map[string]any{}), invalid: true},
		{name: "mixed_full_field", text: issuePatch(r, map[string]any{"reviewUpdates": map[string]any{"approved": false}, "limitations": []string{}}), invalid: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := string(r.base)
			merged, applied, err := r.mergeIssues(tt.text)
			if applied != tt.applied || (err != nil) != tt.invalid || before != string(r.base) {
				t.Fatal("incorrect merge provenance or mutated base", applied, err)
			}
			if !applied {
				if merged != tt.text {
					t.Fatal("unapplied submission was rewritten")
				}
				return
			}
			if _, _, err := workflow.NormalizeAIStageSubmission(merged, schema); err != nil {
				t.Fatal("merged output violates frozen schema", err)
			}
			var oldRoot, newRoot map[string]json.RawMessage
			_ = json.Unmarshal(r.base, &oldRoot)
			_ = json.Unmarshal([]byte(merged), &newRoot)
			if string(oldRoot["reviewFindings"]) != string(newRoot["reviewFindings"]) || string(newRoot["approved"]) != "false" || newRoot["baseSHA256"] != nil {
				t.Fatal("merge regenerated findings, approved a review, or retained patch metadata")
			}
		})
	}
}

type nativeReviewControlHost struct {
	*staticResearchGuidance
	initial   error
	validated []string
}

func (h *nativeReviewControlHost) ValidateResearchSubmission(_ context.Context, _, _, _, text string) (json.RawMessage, error) {
	h.validated = append(h.validated, text)
	if len(h.validated) == 1 {
		return nil, h.initial
	}
	normalized, _, err := workflow.NormalizeAIStageSubmission(text, h.value.StructuredOutputSchema)
	if err != nil {
		return nil, &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: err}
	}
	return normalized, nil
}

func TestNativeReviewCorrectionKeepsControlIdentityAndBoundBase(t *testing.T) {
	schema, base := issueRepairFixture(t)
	diagnostic := &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: &workflow.ReviewMissingFindingError{ID: "issue-1-2"}}
	r := newReviewRepair(base, schema, diagnostic)
	good := issuePatch(r, map[string]any{"reviewUpdates": map[string]any{"limitations": []string{"Scope retained"}}})
	merged, err := r.merge(good)
	if err != nil {
		t.Fatal(err)
	}
	testNativeCorrectionControl(t, base, schema, diagnostic, good, merged, r.schema)
}

func TestNativeIdentityCitationAndLegacyReviewCorrectionKeepBoundBase(t *testing.T) {
	for _, kind := range []string{"identity", "citation", "legacy_review"} {
		t.Run(kind, func(t *testing.T) {
			var base, patch, merged string
			var schema, patchSchema json.RawMessage
			var diagnostic, err error
			switch kind {
			case "identity":
				base, schema, diagnostic = identityRepairFixture()
				r := newLiteratureIdentityRepair(base, schema, diagnostic)
				patch, patchSchema = identityPatch(r), r.schema
				merged, err = r.merge(patch)
			case "citation":
				base, schema = quoteRepairBase, quoteRepairSchema
				diagnostic = &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: &workflow.CitationQuoteError{Reference: "[K-A]"}}
				r := newCitationRepairForError(base, schema, diagnostic)
				patch, patchSchema = quotePatch(r, "Exact evidence."), r.schema
				merged, err = r.merge(patch)
			case "legacy_review":
				base, schema, diagnostic = reviewRepairTestBase, reviewRepairTestSchema, reviewRepairDiagnostic()
				r := newReviewRepair(base, schema, diagnostic)
				patch, patchSchema = reviewRepairValidPatch(r), r.schema
				merged, err = r.merge(patch)
			}
			if err != nil {
				t.Fatal(err)
			}
			testNativeCorrectionControl(t, base, schema, diagnostic, patch, merged, patchSchema)
		})
	}
}

func testNativeCorrectionControl(t *testing.T, base string, schema json.RawMessage, diagnostic error, good, merged string, patchSchema json.RawMessage) {
	t.Helper()
	for _, mode := range []string{"missing_hash", "malformed", "full_in_repair", "patch_in_full"} {
		t.Run(mode, func(t *testing.T) {
			bad, final := good, good
			badScript, finalScript := submissionToolScript(bad), submissionToolScript(final)
			badScript[0].Event.ToolCall.Name = stageRepairTool
			finalScript[0].Event.ToolCall.Name = stageRepairTool
			switch mode {
			case "missing_hash":
				var fields map[string]json.RawMessage
				_ = json.Unmarshal([]byte(good), &fields)
				delete(fields, "baseSHA256")
				encoded, _ := json.Marshal(fields)
				bad = string(encoded)
			case "malformed":
				bad = strings.TrimSuffix(good, "}")
			case "full_in_repair":
				bad = base
			case "patch_in_full":
				badScript[0].Event.ToolCall.Name = stageSubmissionTool
				finalScript[0].Event.ToolCall.Name = stageSubmissionTool
				final = merged
			}
			badScript[0].Event.ToolCall.Arguments = json.RawMessage(bad)
			finalScript[0].Event.ToolCall.Arguments = json.RawMessage(final)
			loop, state, provider := newLoopFixture(t, nil, submissionScript(base), badScript, finalScript)
			state.workflowAI = true
			host := &nativeReviewControlHost{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: schema}}, initial: diagnostic}
			loop.research = host
			if out := loop.Run(context.Background(), state.run.ID); out != OutcomeCompleted || len(provider.Requests()) != 3 {
				t.Fatal("bad correction accepted or bound repair lost", out, len(provider.Requests()), state.run.ErrorCode, state.run.ErrorDetails)
			}
			wantValidated := 2
			if mode == "patch_in_full" {
				wantValidated = 3 // Explicit full control is checked as a full candidate, never merged.
			}
			if len(host.validated) != wantValidated || host.validated[len(host.validated)-1] != merged || state.journalDrafts[2] != bad || state.journalDrafts[3] != final || len(state.calls) != 0 {
				t.Fatal("control, raw journal, or merged result not preserved", len(host.validated))
			}
			if mode != "patch_in_full" {
				request := provider.Requests()[2]
				if len(request.Tools) != 2 || request.Tools[1].Name != stageRepairTool || string(request.Tools[1].InputSchema) != string(patchSchema) {
					t.Fatal("invalid repair discarded or changed the bound base")
				}
			}
		})
	}
}

func TestIssueRepairProgressAllowsOnlyOneBoundedExtraTurn(t *testing.T) {
	schema, base := issueRepairFixture(t)
	errFor := func(ids ...string) error {
		es := []error{}
		for _, id := range ids {
			es = append(es, &workflow.ReviewMissingFindingError{ID: id})
		}
		return &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: errors.Join(es...)}
	}
	for _, envelope := range []string{"bare", "fenced", "crlf_fenced"} {
		for _, mode := range []string{"progress", "repeat", "regression", "scientific", "full_result", "bad_patch", "fourth_fails"} {
			t.Run(envelope+"/"+mode, func(t *testing.T) {
				e1 := errFor("issue-1-2", "issue-1-3", "issue-1-4")
				e2 := errFor("issue-1-3", "issue-1-4")
				e3 := errFor("issue-1-4")
				if mode == "repeat" {
					e3 = e2
				}
				if mode == "regression" {
					e3 = errFor("issue-9-9")
				}
				if mode == "scientific" {
					e3 = &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: errors.New("scientific judgment remains unsupported")}
				}
				e4 := e3
				if mode == "fourth_fails" {
					e1 = errFor("issue-1-2", "issue-1-3", "issue-1-4", "issue-1-5")
					e2 = errFor("issue-1-3", "issue-1-4", "issue-1-5")
					e3 = errFor("issue-1-4", "issue-1-5")
					e4 = errFor("issue-1-5") // Still shrinking: a second extension MUST NOT be allowed.
				}
				r1 := newReviewRepair(base, schema, e1)
				p1 := issuePatch(r1, map[string]any{"reviewUpdates": map[string]any{"limitations": []string{"first update"}}})
				b1, _ := r1.merge(p1)
				r2 := newReviewRepair(b1, schema, e2)
				p2 := issuePatch(r2, map[string]any{"reviewUpdates": map[string]any{"limitations": []string{"second update"}}})
				b2, _ := r2.merge(p2)
				r3 := newReviewRepair(b2, schema, e3)
				p3 := issuePatch(r3, map[string]any{"reviewUpdates": map[string]any{"limitations": []string{"third update"}}})
				b3, _ := r3.merge(p3)
				if mode == "full_result" {
					p1, p2, p3 = b1, b2, b3
				}
				if mode == "bad_patch" {
					p2 = strings.Replace(p2, r2.hash, strings.Repeat("b", 64), 1)
				}
				wrap := func(text string) string {
					switch envelope {
					case "fenced":
						return "```json\n" + text + "\n```"
					case "crlf_fenced":
						return " \r\n``` JSON\r\n" + text + "\r\n```\t "
					default:
						return text
					}
				}
				p1, p2, p3 = wrap(p1), wrap(p2), wrap(p3)
				loop, state, provider := newLoopFixture(t, nil, submissionScript(base), submissionScript(p1), submissionScript(p2), submissionScript(p3))
				state.workflowAI = true
				es := []error{e1, e2, e3}
				if mode == "fourth_fails" {
					es = append(es, e4)
				}
				host := &submissionHostFixture{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: schema}}, errors: es, accepted: json.RawMessage(b3)}
				loop.research = host
				out := loop.Run(context.Background(), state.run.ID)
				want := 3
				if mode == "progress" || mode == "fourth_fails" {
					want = 4
				}
				if len(provider.Requests()) != want || len(state.calls) != 0 || state.journalDrafts[2] != p1 {
					t.Fatal("budget/audit/tools incorrect", len(provider.Requests()))
				}
				if (mode == "progress") != (out == OutcomeCompleted) {
					t.Fatal("wrong terminal state", out, state.run.ErrorDetails)
				}
				if mode == "fourth_fails" && !strings.Contains(state.run.ErrorMessage, "三次") {
					t.Fatal("extra-turn banner inaccurate")
				}
				if out != OutcomeCompleted && state.run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" {
					t.Fatal("unexpected rejection classification", state.run.ErrorCode)
				}
			})
		}
	}
	if reviewProgressKeys(errors.New("scientific failure")) != nil || strictReviewProgress(map[string]bool{"a": true}, map[string]bool{"b": true}) {
		t.Fatal("unknown/regression extended budget")
	}
}

func TestCapturedReviewDriftFixedByAddingOnlyMissingIssue(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REVIEW_DRIFT_CAPTURE")
	if dir == "" {
		t.Skip("no scoped capture")
	}
	read := func(name string) []byte {
		b, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	var turns []struct {
		Text string `json:"draft_text"`
	}
	_ = json.Unmarshal(read("model_turn_journal.json"), &turns)
	r := newReviewRepair(turns[1].Text, read("schema.json"), &workflow.ReviewMissingFindingError{ID: "issue-1-2"})
	if r == nil || !r.issues {
		t.Fatal("actual second draft cannot be locally repaired")
	}
	var first map[string]any
	start := strings.Index(turns[0].Text, "{")
	end := strings.LastIndex(turns[0].Text, "}")
	_ = json.Unmarshal([]byte(turns[0].Text[start:end+1]), &first)
	var addition map[string]any
	for _, raw := range first["reviewFindings"].([]any) {
		f := raw.(map[string]any)
		if f["id"] == "issue-1-2" {
			addition = f
		}
	}
	delete(addition, "correctionIndex")
	patch := issuePatch(r, map[string]any{"reviewIssueReplacements": []any{addition}})
	merged, err := r.merge(patch)
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{"issue-patch.json": []byte(patch), "issue-merged.json": []byte(merged), "issue-base.json": r.base} {
		if err := os.WriteFile(filepath.Join(dir, "derived", name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(merged, "其二手评论是背景讨论") || !strings.Contains(merged, "讨论该试验的评论不是另一项试验，也不应替代原始试验来源") {
		t.Fatal("correct quote regressed")
	}
	t.Log("derived sparse patch adds the originally supplied missing issue; second-draft text and TREATY quote retained, no formal DB changes")
}

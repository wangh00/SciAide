package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
)

var reviewRepairTestSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["approved","reviewedInputSha256","reviewFindings","requiredCorrections","count"],"properties":{"approved":{"type":"boolean"},"reviewedInputSha256":{"type":"string"},"count":{"type":"integer"},"requiredCorrections":{"type":"array","items":{"type":"string"}},"reviewFindings":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","basis","changeReason","summary"],"properties":{"id":{"type":"string"},"summary":{"type":"string"},"changeReason":{"type":"string"},"basis":{"type":"array","items":{"type":"object","required":["pointer","quote"],"properties":{"pointer":{"type":"string"},"quote":{"type":"string"}}}}}}}}}`)

const reviewRepairTestBase = `{"approved":false,"reviewedInputSha256":"unchanged-frozen-input","count":9007199254740993,"requiredCorrections":["Verify the reported finding"],"reviewFindings":[{"id":"issue-1-1","summary":"Key result not verified","basis":[{"pointer":"/evidenceScreening/evidenceGaps/0","quote":"Exact missing field"},{"pointer":"/context/markdown","quote":"Unchanged quoted claim"}],"changeReason":""}]}`

func reviewRepairDiagnostic() error {
	return &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: errors.Join(
		&workflow.ReviewBasisErrors{Problems: []string{"basis points to object"}, RepairPaths: []string{"/reviewFindings/0/basis/0/pointer", "/reviewFindings/0/basis/0/quote"}},
		&workflow.ReviewFindingStateErrors{Problems: []string{"changeReason missing"}, RepairPaths: []string{"/reviewFindings/0/changeReason"}},
	)}
}

func reviewRepairPatch(r *reviewRepair, cells ...map[string]string) string {
	data, _ := json.Marshal(map[string]any{"baseSHA256": r.hash, "reviewFindingsPatch": cells})
	return string(data)
}

func reviewRepairValidPatch(r *reviewRepair) string {
	return reviewRepairPatch(r,
		map[string]string{"path": "/reviewFindings/0/basis/0/pointer", "value": "/evidenceScreening/evidenceGaps/0/field"},
		map[string]string{"path": "/reviewFindings/0/changeReason", "value": "First detection from current evidence"},
	)
}

func TestReviewRepairChangesOnlyDiagnosedCells(t *testing.T) {
	r := newReviewRepair(reviewRepairTestBase, reviewRepairTestSchema, reviewRepairDiagnostic())
	if r == nil {
		t.Fatal("no local correction offered")
	}
	if err := (tool.JSONSchemaValidator{}).ValidateSchema(r.schema); err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), r.base...)
	got, err := r.merge("```json\n" + reviewRepairValidPatch(r) + "\n```")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(reviewRepairTestBase, `"/evidenceScreening/evidenceGaps/0"`, `"/evidenceScreening/evidenceGaps/0/field"`, 1)
	want = strings.Replace(want, `"changeReason":""`, `"changeReason":"First detection from current evidence"`, 1)
	// Comparing RawMessage after whitespace compaction also checks the exact
	// integer beyond JavaScript precision; neither decoder may use float64.
	var actual, expected map[string]json.RawMessage
	_ = json.Unmarshal([]byte(got), &actual)
	_ = json.Unmarshal([]byte(want), &expected)
	for _, key := range []string{"approved", "reviewedInputSha256", "count", "requiredCorrections"} {
		if string(actual[key]) != string(expected[key]) {
			t.Fatal("changed unrelated scientific value", key, got)
		}
	}
	var a, b any
	_ = json.Unmarshal(actual["reviewFindings"], &a)
	_ = json.Unmarshal(expected["reviewFindings"], &b)
	if !reflect.DeepEqual(a, b) || string(before) != string(r.base) {
		t.Fatal("altered other finding data or mutated rejected base", got)
	}
	if _, _, err := workflow.NormalizeAIStageSubmission(got, reviewRepairTestSchema); err != nil {
		t.Fatal("merged result bypassed its frozen schema", err)
	}
	full, err := r.merge(got)
	if err != nil || full != got {
		t.Fatal("full-review fallback changed", err)
	}
}

func TestReviewRepairRejectsWrongBaseDuplicateUnauthorizedAndMalformedPatches(t *testing.T) {
	r := newReviewRepair(reviewRepairTestBase, reviewRepairTestSchema, reviewRepairDiagnostic())
	valid := reviewRepairValidPatch(r)
	for _, candidate := range []string{
		strings.Replace(valid, r.hash, strings.Repeat("a", 64), 1),
		strings.Replace(valid, "/reviewFindings/0/basis/0/pointer", "/approved", 1),
		strings.Replace(valid, "/reviewFindings/0/basis/0/pointer", "/reviewFindings/0/basis/1/pointer", 1),
		strings.Replace(valid, "/reviewFindings/0/basis/0/pointer", "/reviewFindings/00/basis/0/pointer", 1),
		strings.Replace(valid, `"value":"/evidenceScreening/evidenceGaps/0/field"`, `"value":true`, 1),
		strings.Replace(valid, `"reviewFindingsPatch":`, `"approved":true,"reviewFindingsPatch":`, 1),
		reviewRepairPatch(r, map[string]string{"path": "/reviewFindings/0/changeReason", "value": "one"}, map[string]string{"path": "/reviewFindings/0/changeReason", "value": "two"}),
	} {
		if _, err := r.merge(candidate); err == nil {
			t.Fatal("bad local repair accepted", candidate)
		}
	}
	// A nested valid patch must not conceal malformed outer stage output.
	for _, candidate := range []string{"```json\n" + valid + "\nunexpected suffix before closing fence\n```", "```json\n" + strings.TrimSuffix(valid, "}") + "\n```", "{\"wrapper\":" + valid + "}"} {
		got, err := r.merge(candidate)
		if err == nil && got != candidate {
			t.Fatal("invalid envelope became a local repair", candidate)
		}
	}
}

func TestReviewRepairUnknownOrMixedErrorsRequireFullResult(t *testing.T) {
	for _, diagnostic := range []error{
		errors.New("not a typed host diagnostic"),
		errors.Join(reviewRepairDiagnostic(), errors.New("missing semantic field")),
		&workflow.ReviewBasisErrors{Problems: []string{"old diagnostic without exact output cell"}},
		&workflow.ReviewBasisErrors{Truncated: true, RepairPaths: []string{"/reviewFindings/0/basis/0/pointer"}},
		&workflow.ReviewFindingStateErrors{Problems: []string{"wrong origin", "missing reason"}, RepairPaths: []string{"/reviewFindings/0/changeReason"}},
		&workflow.ReviewBasisErrors{RepairPaths: []string{"/reviewFindings/0/summary"}},
		&apperr.Error{Code: "WORKFLOW_BINDING_INVALID", Cause: reviewRepairDiagnostic()},
	} {
		if newReviewRepair(reviewRepairTestBase, reviewRepairTestSchema, diagnostic) != nil {
			t.Fatal("guessed patch for unknown or mixed defect", diagnostic)
		}
	}
}

func TestReviewLocalRepairKeepsJournalAndRevalidatesCompleteResult(t *testing.T) {
	for _, dedicated := range []bool{false, true} {
		r := newReviewRepair(reviewRepairTestBase, reviewRepairTestSchema, reviewRepairDiagnostic())
		patch := reviewRepairValidPatch(r)
		merged, _ := r.merge(patch)
		loop, state, provider := newLoopFixture(t, nil, submissionScript(reviewRepairTestBase), submissionScript(patch))
		state.workflowAI = true
		guidance := &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: reviewRepairTestSchema}}
		if dedicated {
			loop.resources = &resourceViewFixture{}
			guidance.value.AllowedToolNames = []string{"builtin.resource.open"}
		}
		host := &submissionHostFixture{staticResearchGuidance: guidance, errors: []error{reviewRepairDiagnostic()}, accepted: json.RawMessage(merged)}
		loop.research = host
		if out := loop.Run(context.Background(), state.run.ID); out != OutcomeCompleted || len(provider.Requests()) != 2 || len(host.validated) != 2 {
			t.Fatal("same-run correction failed", out, state.run.ErrorMessage)
		}
		if host.validated[1][3] != merged || state.journalDrafts[2] != patch || state.messages[1].Parts[0].Text != merged {
			t.Fatal("audit/merged-result boundary lost", host.validated, state.journalDrafts)
		}
		if len(state.calls) != 0 || provider.Requests()[1].ForcedTool != "" || len(provider.Requests()[1].Tools) != 2 {
			t.Fatal("correction repeated research tools or escaped submission interface")
		}
	}
}

func TestReviewLocalRepairGatewayFenceStillUsesFullValidator(t *testing.T) {
	r := newReviewRepair(reviewRepairTestBase, reviewRepairTestSchema, reviewRepairDiagnostic())
	patch := "```json\n" + reviewRepairValidPatch(r) + "\n```"
	merged, _ := r.merge(patch)
	loop, state, provider := newLoopFixture(t, nil, submissionScript(reviewRepairTestBase), submissionScript(patch))
	state.workflowAI = true
	host := &submissionHostFixture{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: reviewRepairTestSchema}}, errors: []error{reviewRepairDiagnostic()}, accepted: json.RawMessage(merged)}
	loop.research = host
	if out := loop.Run(context.Background(), state.run.ID); out != OutcomeCompleted || len(provider.Requests()) != 2 || host.validated[1][3] != merged || state.journalDrafts[2] != patch {
		t.Fatal("gateway fence stopped a valid local repair", out, state.run.ErrorMessage)
	}
	for _, text := range []string{"<tool_call>" + reviewRepairValidPatch(r) + "</tool_call>", "```json\n" + reviewRepairValidPatch(r) + "\ninvalid suffix\n```", "I will correct it."} {
		if stageSubmissionEnvelope(text) {
			t.Fatal("pseudo-tool/progress/malformed text became a repair", text)
		}
	}
}

func TestReviewLocalRepairStillStopsAfterTwoCorrections(t *testing.T) {
	r := newReviewRepair(reviewRepairTestBase, reviewRepairTestSchema, reviewRepairDiagnostic())
	patch := reviewRepairPatch(r, map[string]string{"path": "/reviewFindings/0/basis/0/pointer", "value": "/evidenceScreening/evidenceGaps/0"})
	stillInvalid, _ := r.merge(patch)
	next := newReviewRepair(stillInvalid, reviewRepairTestSchema, reviewRepairDiagnostic())
	second := reviewRepairPatch(next, map[string]string{"path": "/reviewFindings/0/basis/0/pointer", "value": "/evidenceScreening/evidenceGaps/0"})
	loop, state, provider := newLoopFixture(t, nil, submissionScript(reviewRepairTestBase), submissionScript(patch), submissionScript(second))
	state.workflowAI = true
	host := &submissionHostFixture{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: reviewRepairTestSchema}}, errors: []error{reviewRepairDiagnostic(), reviewRepairDiagnostic(), reviewRepairDiagnostic()}}
	loop.research = host
	if out := loop.Run(context.Background(), state.run.ID); out != OutcomeFailed || len(provider.Requests()) != 3 || len(host.validated) != 3 || state.run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || state.messages[1].Parts[0].Text != "" {
		t.Fatalf("local correction bypassed rejection or acquired another budget: out=%s requests=%d validated=%d code=%s text=%q detail=%s", out, len(provider.Requests()), len(host.validated), state.run.ErrorCode, state.messages[1].Parts[0].Text, state.run.ErrorDetails)
	}
}

func TestReviewSecondCorrectionKeepsFirstCorrectedBasis(t *testing.T) {
	r := newReviewRepair(reviewRepairTestBase, reviewRepairTestSchema, reviewRepairDiagnostic())
	first := reviewRepairPatch(r, map[string]string{"path": "/reviewFindings/0/basis/0/pointer", "value": "/evidenceScreening/evidenceGaps/0/field"})
	partlyCorrect, _ := r.merge(first)
	reasonErr := &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: &workflow.ReviewFindingStateErrors{Problems: []string{"missing changeReason"}, RepairPaths: []string{"/reviewFindings/0/changeReason"}}}
	next := newReviewRepair(partlyCorrect, reviewRepairTestSchema, reasonErr)
	last := reviewRepairPatch(next, map[string]string{"path": "/reviewFindings/0/changeReason", "value": "First detection from current evidence"})
	final, _ := next.merge(last)
	loop, state, provider := newLoopFixture(t, nil, submissionScript(reviewRepairTestBase), submissionScript(first), submissionScript(last))
	state.workflowAI = true
	host := &submissionHostFixture{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: reviewRepairTestSchema}}, errors: []error{reviewRepairDiagnostic(), reasonErr}, accepted: json.RawMessage(final)}
	loop.research = host
	if out := loop.Run(context.Background(), state.run.ID); out != OutcomeCompleted || len(provider.Requests()) != 3 || host.validated[2][3] != final || !strings.Contains(final, "/evidenceScreening/evidenceGaps/0/field") {
		t.Fatal("second correction regressed previously fixed pointer", out, state.run.ErrorMessage)
	}
	var properties map[string]json.RawMessage
	var schema map[string]json.RawMessage
	_ = json.Unmarshal(provider.Requests()[2].Tools[0].InputSchema, &schema)
	_ = json.Unmarshal(schema["properties"], &properties)
	if strings.Contains(string(properties["reviewFindingsPatch"]), "/basis/") {
		t.Fatal("already corrected basis remained writable in local second repair")
	}
}

func TestCapturedReviewLocalPatchUsesOnlyActualModelCorrections(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REVIEW_BASIS_REPLAY")
	if dir == "" {
		t.Skip("no read-only incident capture supplied")
	}
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var first, pointers, reasons struct {
		Findings []struct {
			ChangeReason string                            `json:"changeReason"`
			Basis        []struct{ Pointer, Quote string } `json:"basis"`
		} `json:"reviewFindings"`
	}
	_ = json.Unmarshal(read("submission-turn-1.json"), &first)
	_ = json.Unmarshal(read("submission-turn-2.json"), &pointers)
	_ = json.Unmarshal(read("submission-turn-3.json"), &reasons)
	// Diagnosed cells are derived from this incident, but replacement strings
	// are taken ONLY from the actual model's two previously recorded repairs.
	basisErr := &workflow.ReviewBasisErrors{Problems: []string{"four object paths"}}
	stateErr := &workflow.ReviewFindingStateErrors{}
	cells := []map[string]string{}
	for i, finding := range first.Findings {
		statePath := "/reviewFindings/" + strconv.Itoa(i) + "/changeReason"
		stateErr.Problems = append(stateErr.Problems, "missing changeReason")
		stateErr.RepairPaths = append(stateErr.RepairPaths, statePath)
		cells = append(cells, map[string]string{"path": statePath, "value": reasons.Findings[i].ChangeReason})
		for j, basis := range finding.Basis {
			if basis.Pointer == pointers.Findings[i].Basis[j].Pointer {
				continue
			}
			path := "/reviewFindings/" + strconv.Itoa(i) + "/basis/" + strconv.Itoa(j) + "/"
			basisErr.RepairPaths = append(basisErr.RepairPaths, path+"pointer", path+"quote")
			cells = append(cells, map[string]string{"path": path + "pointer", "value": pointers.Findings[i].Basis[j].Pointer})
		}
	}
	r := newReviewRepair(string(read("submission-turn-1.json")), read("schema.json"), errors.Join(basisErr, stateErr))
	if r == nil {
		t.Fatal("captured review does not qualify for local repair")
	}
	merged, err := r.merge(reviewRepairPatch(r, cells...))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "derived-local-repair.json"), []byte(merged), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("one local correction, %d changed cells, all scientific findings/quotes/decisions retained; no live model or DB", len(cells))
}

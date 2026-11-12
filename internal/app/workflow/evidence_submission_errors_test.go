package workflow

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestEvidenceSubmissionCollectsScopeAndAllQuoteIssues(t *testing.T) {
	input := raw(`{"_selectedEvidence":{"phase":"coverage","documents":[]},"candidates":[{"reference":"[K-111111111111]","quote":"Source &gt; result. Restudy before test."},{"reference":"[K-222222222222]","quote":"The advantage oftesting is reported."}]}`)
	output := raw(`{"recommendation":"proceed_limited","recommendedReferences":["[K-111111111111]","[K-222222222222]"],"coverage":{"sufficientForClaimedScope":false},"citationAssessments":[{"reference":"[K-111111111111]","decision":"support","supportingQuote":"Source > result. Restudying before a test."},{"reference":"[K-222222222222]","decision":"support","supportingQuote":"The advantage of testing is reported."}],"evidenceGaps":[{"field":"effect","status":"not_discussed_in_checked_scope","required":true,"assessment":"Claimed read","checkedScope":[{"documentId":"doc","locators":["lines:1-21"]}]},{"field":"followup","status":"not_retrieved","required":false}]}`)
	err := validateSelectedEvidence(output, input)
	var mixed *EvidenceSubmissionErrors
	var quotes *CitationQuoteErrors
	if !errors.As(err, &mixed) || !errors.As(err, &quotes) || len(quotes.Issues) != 2 {
		t.Fatal("independent quote defects hidden behind scope error", err)
	}
	if !strings.Contains(err.Error(), "没有实际原文核读记录") || !strings.Contains(err.Error(), "followup") {
		t.Fatal("scope diagnostics were dropped", err)
	}
	v := decodeObject(output)
	v["evidenceGaps"] = []any{}
	quoteOnly := validateSelectedEvidence(mustJSON(v), input)
	if !errors.As(quoteOnly, &quotes) {
		t.Fatal("quote-only error type changed", quoteOnly)
	}
	if errors.As(quoteOnly, &mixed) {
		t.Fatal("stale mixed error survived scope correction")
	}
}

func TestCapturedTestingEffectDiagnosticsAndMergedScopeValidation(t *testing.T) {
	dir := os.Getenv("SCIAIDE_SCOPE_REPAIR_REPLAY_DIR")
	if dir == "" {
		t.Skip("no task-scoped failure capture")
	}
	read := func(name string) json.RawMessage {
		b, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	input := read("input-model-domain.json")
	err := validateSelectedEvidence(canonicalSupportingQuotes(read("submission-5.json"), input), input)
	var mixed *EvidenceSubmissionErrors
	var quotes *CitationQuoteErrors
	if !errors.As(err, &mixed) || !errors.As(err, &quotes) || len(quotes.Issues) != 2 {
		t.Fatal("real second submission still hides quote errors", err)
	}
	if !strings.Contains(err.Error(), "延迟保持效应量") || !strings.Contains(err.Error(), "四份无摘要") || !strings.Contains(err.Error(), "即时测验") {
		t.Fatal("did not collect all three scope issues", err)
	}
	merged := read("merged-selected.json")
	if err := validateSelectedEvidence(merged, input); err != nil {
		t.Fatal("actual agent selected patch fails full business validation", err)
	}
	t.Log("Real second submission yields all 3 scope diagnoses + both quote mismatches at once; agent source-segment/gap patch passes the same business validator. Not proof of scientific approval or whole workflow completion.")
}

func TestMixedEvidenceDiagnosticRemapsOnlyCitationCopies(t *testing.T) {
	refs := []tool.CitationRef{{Reference: "[K-111111111111]", IndexVersionID: "index", ChunkID: "a", QuoteSHA256: strings.Repeat("a", 64)}, {Reference: "[K-222222222222]", IndexVersionID: "index", ChunkID: "b", QuoteSHA256: strings.Repeat("b", 64)}}
	err := evidenceSubmissionErrors(errors.New("scope must remain"), &CitationQuoteErrors{Issues: []CitationQuoteError{{Reference: refs[0].Reference, CandidateQuote: "A"}, {Reference: refs[1].Reference, CandidateQuote: "B"}}})
	mapped := modelVisibleEvidenceSubmissionError(err, refs, "chat")
	var mixed *EvidenceSubmissionErrors
	var quotes *CitationQuoteErrors
	if !errors.As(mapped, &mixed) || !errors.As(mapped, &quotes) || mixed.Scope.Error() != "scope must remain" {
		t.Fatal("mapping lost diagnostics", mapped)
	}
	for i, issue := range quotes.Issues {
		if issue.Reference != citation.KnowledgeReference("chat", refs[i].IndexVersionID, refs[i].ChunkID, refs[i].QuoteSHA256) {
			t.Fatal("provider marker was not reissued")
		}
	}
	var original *CitationQuoteErrors
	_ = errors.As(err, &original)
	if original.Issues[0].Reference != refs[0].Reference {
		t.Fatal("mapping changed frozen original")
	}
}

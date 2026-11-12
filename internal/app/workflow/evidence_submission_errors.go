package workflow

import (
	"errors"
	"github.com/wangh00/SciAide/internal/app/tool"
)

// Independent scope and citation defects must reach the same correction turn.
// This is diagnostic composition, never acceptance of either invalid part.
type EvidenceSubmissionErrors struct {
	Scope     error
	Citations error
}

func (e *EvidenceSubmissionErrors) Error() string {
	return "证据范围与引用须在同一次提交中修正：\n" + e.Scope.Error() + "\n" + e.Citations.Error()
}

func (e *EvidenceSubmissionErrors) Unwrap() []error { return []error{e.Scope, e.Citations} }

func evidenceSubmissionErrors(scope, citations error) error {
	if scope == nil {
		return citations
	}
	if citations == nil {
		return scope
	}
	return &EvidenceSubmissionErrors{Scope: scope, Citations: citations}
}

func modelVisibleEvidenceSubmissionError(err error, refs []tool.CitationRef, chatRunID string) error {
	var mixed *EvidenceSubmissionErrors
	if errors.As(err, &mixed) {
		return &EvidenceSubmissionErrors{Scope: mixed.Scope, Citations: modelVisibleEvidenceSubmissionError(mixed.Citations, refs, chatRunID)}
	}
	var assessments *CitationAssessmentErrors
	var quotes *CitationQuoteErrors
	var quote *CitationQuoteError
	if errors.As(err, &assessments) {
		return modelVisibleAssessmentErrors(assessments, refs, chatRunID)
	}
	if errors.As(err, &quotes) {
		return modelVisibleQuoteErrors(quotes, refs, chatRunID)
	}
	if errors.As(err, &quote) {
		return modelVisibleQuoteError(quote, refs, chatRunID)
	}
	return err
}

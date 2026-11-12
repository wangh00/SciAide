package workflow

import (
	"errors"

	"github.com/wangh00/SciAide/internal/apperr"
)

// aiStageOutputFormatError marks the output envelope/schema boundary only.
// Input-aware evidence and scientific validation retain their own error types.
type aiStageOutputFormatError struct{ cause error }

func (e *aiStageOutputFormatError) Error() string { return e.cause.Error() }
func (e *aiStageOutputFormatError) Unwrap() error { return e.cause }

// IsAIStageOutputFormatError remains valid through the host's submission-error
// wrapper. It does not imply that the candidate passed scientific validation.
func IsAIStageOutputFormatError(err error) bool {
	var format *aiStageOutputFormatError
	return errors.As(err, &format)
}

func markAIStageOutputFormatError(err error) error {
	if err == nil || IsAIStageOutputFormatError(err) {
		return err
	}
	var host *apperr.Error
	if errors.As(err, &host) && host.Code == aiSchemaInvalidCode {
		return err // A broken frozen host schema cannot be repaired by the model.
	}
	return &aiStageOutputFormatError{cause: err}
}

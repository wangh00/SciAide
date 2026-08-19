package apperr

import (
	"errors"
	"fmt"
	"strings"
)

type Error struct {
	Code          string
	UserMessage   string
	Details       string
	Retryable     bool
	CorrelationID string
	Cause         error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Code, e.Cause)
	}
	return e.Code
}

func (e *Error) Unwrap() error { return e.Cause }

type PublicError struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	Details       string `json:"details,omitempty"`
	Retryable     bool   `json:"retryable"`
	CorrelationID string `json:"correlationId,omitempty"`
}

func Public(err error) PublicError {
	var appErr *Error
	if errors.As(err, &appErr) {
		return PublicError{
			Code:          appErr.Code,
			Message:       appErr.UserMessage,
			Details:       boundedDetails(appErr.Details),
			Retryable:     appErr.Retryable,
			CorrelationID: appErr.CorrelationID,
		}
	}
	return PublicError{
		Code:    "INTERNAL_ERROR",
		Message: "SciAide 遇到内部错误，请使用错误编号查看诊断信息。",
	}
}

func boundedDetails(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > 8_192 {
		value = string(runes[:8_189]) + "..."
	}
	return value
}

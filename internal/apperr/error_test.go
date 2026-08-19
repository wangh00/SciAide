package apperr

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPublicErrorDoesNotExposeCause(t *testing.T) {
	err := &Error{
		Code:          "MODEL_AUTH_FAILED",
		UserMessage:   "模型认证失败，请检查 API Key。",
		Details:       "HTTP status: 401",
		CorrelationID: "correlation-1",
		Cause:         errors.New("secret upstream detail"),
	}
	contents, marshalErr := json.Marshal(Public(err))
	if marshalErr != nil {
		t.Fatalf("Marshal() error = %v", marshalErr)
	}
	text := string(contents)
	if strings.Contains(text, "secret upstream detail") {
		t.Fatalf("internal cause leaked: %s", text)
	}
	if !strings.Contains(text, "correlation-1") || !strings.Contains(text, "MODEL_AUTH_FAILED") || !strings.Contains(text, "HTTP status: 401") {
		t.Fatalf("public error lacks traceable fields: %s", text)
	}
}

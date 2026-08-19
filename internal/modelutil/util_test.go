package modelutil

import (
	"errors"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/apperr"
)

func TestProviderErrorDetailsPreservesPayloadAndRedactsSecrets(t *testing.T) {
	body := []byte(`{"error":{"code":"invalid_request","message":"API key is sk-super-secret-value","authorization":"Bearer hidden-token"},"request_id":"req-1"}`)
	err := ClassifyStatus(400, body)
	var appErr *apperr.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("error = %#v", err)
	}
	if !strings.Contains(appErr.UserMessage, "API key is") || !strings.Contains(appErr.Details, "invalid_request") || !strings.Contains(appErr.Details, "req-1") {
		t.Fatalf("classified error = %#v", appErr)
	}
	if strings.Contains(appErr.Details, "sk-super-secret-value") || strings.Contains(appErr.Details, "hidden-token") || !strings.Contains(appErr.Details, "[REDACTED]") {
		t.Fatalf("provider details were not redacted: %s", appErr.Details)
	}
}

func TestProviderErrorMessageReadsResponsesFailure(t *testing.T) {
	body := []byte(`{"type":"response.failed","response":{"error":{"code":"context_length_exceeded","message":"context is too long"}}}`)
	if message := ProviderErrorMessage(body); message != "context is too long" {
		t.Fatalf("message = %q", message)
	}
}

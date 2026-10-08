package modelutil

import (
	"errors"
	"testing"

	"github.com/wangh00/SciAide/internal/apperr"
)

func TestStreamErrorRetryable(t *testing.T) {
	for _, test := range []struct {
		name      string
		errorType string
		code      string
		want      bool
	}{
		{"observed upstream read failure", "upstream_error", "stream_read_error", true},
		{"code only", "", "stream_read_error", true},
		{"type only", "stream_read_error", "", true},
		{"normalized", " UPSTREAM_ERROR ", " STREAM_READ_ERROR ", true},
		{"observed gateway concurrency", "", "gateway_concurrency_limit", true},
		{"gateway concurrency normalized", " UPSTREAM_ERROR ", " GATEWAY_CONCURRENCY_LIMIT ", true},
		{"gateway concurrency type only", "gateway_concurrency_limit", "", true},
		{"not concurrency substring", "upstream_error", "not_gateway_concurrency_limit", false},
		{"concurrency wrapper with permanent code", "gateway_concurrency_limit", "invalid_api_key", false},
		{"overload", "overloaded_error", "", true},
		{"rate limit", "error", "rate_limit_exceeded", true},
		{"server error", "server_error", "", true},
		{"timeout", "error", "timeout_error", true},
		{"unavailable", "service_unavailable", "", true},
		{"unspecified upstream", "upstream_error", "", false},
		{"upstream authentication", "upstream_error", "invalid_api_key", false},
		{"upstream context limit", "upstream_error", "context_length_exceeded", false},
		{"upstream invalid request", "upstream_error", "invalid_request_error", false},
		{"authentication", "authentication_error", "invalid_api_key", false},
		{"permission", "permission_error", "", false},
		{"invalid parameter", "invalid_request_error", "invalid_parameter", false},
		{"unknown code", "upstream_error", "unknown_error", false},
		{"not substring matching", "upstream_error", "not_stream_read_error", false},
		{"specific code wins", "stream_read_error", "invalid_request_error", false},
		{"transient wrapper cannot override permanent", "server_error", "invalid_api_key", false},
		{"transient wrapper cannot override unknown", "server_error", "unknown_error", false},
		{"permanent type cannot override specific transient", "permission_error", "service_unavailable", false},
		{"billing is not rate throttling", "rate_limit_error", "insufficient_quota", false},
		{"empty", "", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := StreamErrorRetryable(test.errorType, test.code); got != test.want {
				t.Fatalf("StreamErrorRetryable(%q, %q) = %v, want %v", test.errorType, test.code, got, test.want)
			}
		})
	}
}

func TestStreamErrorClassificationUsesSpecificIdentityBeforeMessage(t *testing.T) {
	for _, tc := range []struct {
		name, typ, code, message string
		retryable                bool
	}{
		{"observed generic upstream outage", "upstream_error", "upstream_error", "The service is temporarily unavailable. Please retry later.", true},
		{"generic type only", "upstream_error", "", "The service is temporarily unavailable.", true},
		{"missing identity", "", "", "Service is temporarily unavailable. Please retry later.", true},
		{"normalized message", " UPSTREAM_ERROR ", " UPSTREAM_ERROR ", "  THE service IS temporarily unavailable.\n Please retry later.  ", true},
		{"bare upstream is ambiguous", "upstream_error", "upstream_error", "Something went wrong", false},
		{"retry alone is not evidence", "upstream_error", "upstream_error", "Please retry later", false},
		{"unknown code wins", "upstream_error", "unknown_error", "The service is temporarily unavailable. Please retry later.", false},
		{"permanent code wins over message", "upstream_error", "invalid_api_key", "The service is temporarily unavailable. Please retry later.", false},
		{"permanent type wins over message", "authentication_error", "upstream_error", "The service is temporarily unavailable. Please retry later.", false},
		{"transient wrapper must not hide invalid request", "server_error", "invalid_request_error", "Please retry later", false},
		{"permanent wrapper must not become transient", "permission_error", "service_unavailable", "Please retry later", false},
		{"quota is not a short-lived outage", "rate_limit_error", "insufficient_quota", "Please retry later", false},
		{"negated temporary phrase", "upstream_error", "upstream_error", "The service is not temporarily unavailable. Invalid request.", false},
		{"long misleading message", "upstream_error", "upstream_error", "Invalid key. The service is temporarily unavailable. Please retry later.", false},
		{"provider specific outage", "error", "service_unavailable", "fixture unavailable", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ClassifyStreamError(tc.typ, tc.code, tc.message, "original diagnostic")
			var appErr *apperr.Error
			if !errors.As(err, &appErr) || appErr.Retryable != tc.retryable || appErr.Details != "original diagnostic" {
				t.Fatalf("classification=%#v", err)
			}
		})
	}
}

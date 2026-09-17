package modelutil

import "testing"

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
		{"empty", "", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := StreamErrorRetryable(test.errorType, test.code); got != test.want {
				t.Fatalf("StreamErrorRetryable(%q, %q) = %v, want %v", test.errorType, test.code, got, test.want)
			}
		})
	}
}

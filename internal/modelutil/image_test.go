package modelutil

import (
	"testing"

	"github.com/wangh00/SciAide/internal/apperr"
)

func TestIsImageInputUnsupportedRequiresExplicitModelCapabilityRejection(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "text only", err: &apperr.Error{Code: "MODEL_REQUEST_REJECTED", HTTPStatus: 400, UserMessage: "Model only supports text input; received unsupported content type 'image_url'."}, want: true},
		{name: "invalid image", err: &apperr.Error{Code: "MODEL_REQUEST_REJECTED", HTTPStatus: 400, UserMessage: "invalid image_url: image too large"}},
		{name: "rate limit", err: &apperr.Error{Code: "MODEL_RATE_LIMITED", HTTPStatus: 429, UserMessage: "image requests are rate limited", Retryable: true}},
		{name: "generic request", err: &apperr.Error{Code: "MODEL_REQUEST_REJECTED", HTTPStatus: 400, UserMessage: "unknown field"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsImageInputUnsupported(test.err); got != test.want {
				t.Fatalf("IsImageInputUnsupported() = %v, want %v", got, test.want)
			}
		})
	}
}

package modelutil

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/apperr"
)

func TestHTTPGatewayIdentityErrorPrecedesStatusAndRedactsBody(t *testing.T) {
	const body = `{"error":{"type":"rate_limit_error","code":"no_userid","message":"PRIVATE_SENTINEL unsupported parameter reasoning_effort"}}`
	for _, status := range []int{400, 422, 429, 503} {
		err := ClassifyStatusWithHeaders(status, http.Header{"Retry-After": []string{"3"}}, []byte(body))
		var p *apperr.Error
		if !errors.As(err, &p) || p.Code != "MODEL_REQUEST_REJECTED" || p.Retryable || p.HTTPStatus != status || p.RetryAfter != 0 || !strings.Contains(p.UserMessage, "no_userid") || !strings.Contains(p.Details, fmt.Sprintf("%x", sha256.Sum256([]byte(body)))) || !strings.Contains(p.Details, fmt.Sprintf("HTTP status: %d", status)) {
			t.Fatal("HTTP wrapper hid permanent identity or original metadata", status, err)
		}
		if strings.Contains(p.UserMessage+p.Details, "PRIVATE_SENTINEL") {
			t.Fatal("provider body leaked into public identity error", p)
		}
	}
}

func TestHTTPGatewayIdentityErrorDoesNotGuessMalformedOrConflictingIdentity(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"some_unknown_specific_code","type":"no_userid"}}`,
		`{"error":{"code":"other","code":"no_userid"}}`,
		`{"error":{"code":"other","\u0063ode":"no_userid"}}`,
		`{"error":{"code":"other","CODE":"no_userid"}}`,
		`{"ERROR":{"code":"other"},"error":{"code":"no_userid"}}`,
		`{"error":{"code":"other"},"error":{"code":"no_userid"}}`,
		`{"error":{"code":"no_userid_private"}}`,
		`{"error":{"code":["no_userid"]}}`,
		`{"summary":"no_userid"}`, `[error: no_userid]`,
		`{"error":{"code":"no_userid"}} trailing`,
	} {
		p := apperr.Public(ClassifyStatus(429, []byte(body)))
		if !p.Retryable || p.Code != "MODEL_RATE_LIMITED" {
			t.Fatal("unknown/malformed HTTP identity changed original status behavior", body, p)
		}
	}
}

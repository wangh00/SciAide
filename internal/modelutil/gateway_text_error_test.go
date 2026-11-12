package modelutil

import (
	"crypto/sha256"
	"fmt"
	"github.com/wangh00/SciAide/internal/apperr"
	"strings"
	"testing"
)

func TestStandaloneGatewayQuotaMarkerIsNarrowAndPermanent(t *testing.T) {
	for _, value := range []string{"[error: usage_limit_reached]", " \n[error:quota_exceeded]\n", "[error: insufficient_quota]"} {
		e := StandaloneGatewayError(value)
		p := apperr.Public(e)
		if e == nil || p.Retryable || p.Code != "MODEL_REQUEST_REJECTED" || !strings.Contains(p.Details, "usage_limit_reached") && strings.Contains(value, "usage_limit_reached") {
			t.Fatal(value, p)
		}
	}
	for _, value := range []string{"error usage_limit_reached", "The error is [error: usage_limit_reached].", "\"[error: usage_limit_reached]\"", "```\n[error: usage_limit_reached]\n```", `{"summary":"[error: usage_limit_reached]"}`, "[error: arbitrary_private_message]"} {
		if StandaloneGatewayError(value) != nil {
			t.Fatal("ordinary content classified as quota", value)
		}
	}
	if StreamErrorRetryable("rate_limit_error", "usage_limit_reached") {
		t.Fatal("permanent quota retried as rate limiting")
	}
}

func TestStandaloneGatewayIdentityMarkerIsNarrowAndPermanent(t *testing.T) {
	for _, text := range []string{"[error: no_userid]", " \r\n[error:no_userid]\n"} {
		p := apperr.Public(StandaloneGatewayError(text))
		if p.Code != "MODEL_REQUEST_REJECTED" || p.Retryable || !strings.Contains(p.Message, "no_userid") || !strings.Contains(p.Details, "no_userid") || !strings.Contains(p.Details, fmt.Sprintf("%x", sha256.Sum256([]byte(text)))) {
			t.Fatal("standalone service identity error lost classification or original fingerprint", p)
		}
	}
	for _, text := range []string{
		"The service returned [error: no_userid].", `"[error: no_userid]"`, "```\n[error: no_userid]\n```",
		`{"summary":"[error: no_userid]"}`, "[error: no_userid_private]", "[error: no_userid\nPRIVATE_SENTINEL]", "[error: no_userid] extra",
	} {
		if StandaloneGatewayError(text) != nil {
			t.Fatal("ordinary content or unknown error identity classified as service failure", text)
		}
	}
	for _, wrapper := range []string{"", "rate_limit_error", "service_unavailable"} {
		p := apperr.Public(ClassifyStreamError(wrapper, "no_userid", "", ""))
		if p.Code != "MODEL_REQUEST_REJECTED" || p.Retryable || !strings.Contains(p.Message, "no_userid") {
			t.Fatal("native identity error became a retryable outage", wrapper, p)
		}
	}
}

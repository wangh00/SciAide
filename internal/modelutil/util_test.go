package modelutil

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/apperr"
)

func TestStreamingHTTPClientDoesNotApplyResponseTimeoutToBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(80 * time.Millisecond)
		_, _ = io.WriteString(w, "data: done\n\n")
	}))
	defer server.Close()

	response, err := NewStreamingHTTPClient(20 * time.Millisecond).Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "data: done\n\n" {
		t.Fatalf("body = %q, error = %v", body, err)
	}
}

func TestStreamingHTTPClientTimesOutWaitingForResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(80 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, err := NewStreamingHTTPClient(20 * time.Millisecond).Get(server.URL)
	if err == nil {
		t.Fatal("expected response header timeout")
	}
	var timeout interface{ Timeout() bool }
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("error = %#v", err)
	}
}

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

func TestClassifyNetworkRetriesTimeoutButNotCertificateFailure(t *testing.T) {
	timeoutErr := ClassifyNetwork(context.DeadlineExceeded)
	var appErr *apperr.Error
	if !errors.As(timeoutErr, &appErr) || !appErr.Retryable || appErr.Code != "MODEL_TIMEOUT" {
		t.Fatalf("timeout error = %#v", timeoutErr)
	}

	certificateErr := ClassifyNetwork(x509.UnknownAuthorityError{})
	appErr = nil
	if !errors.As(certificateErr, &appErr) || appErr.Retryable || appErr.Code != "MODEL_TLS_INVALID" {
		t.Fatalf("certificate error = %#v", certificateErr)
	}
}

func TestClassifyStatusCarriesRetryAfter(t *testing.T) {
	err := ClassifyStatusWithHeaders(http.StatusTooManyRequests, http.Header{"Retry-After": []string{"3"}}, nil)
	if !IsRetryable(err) || RetryAfter(err) != 3*time.Second {
		t.Fatalf("classified error = %#v, retry after = %s", err, RetryAfter(err))
	}
}

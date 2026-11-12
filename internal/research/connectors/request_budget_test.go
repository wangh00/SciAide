package connectors

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	research "github.com/wangh00/SciAide/internal/app/research"
)

type requestBudgetFixture struct {
	calls    int
	status   int
	redirect bool
}

func (f *requestBudgetFixture) RoundTripSingleAttempt(r *http.Request) (*http.Response, error) {
	return f.RoundTrip(r)
}

type opaqueBudgetFixture struct{ calls int }

func (f *opaqueBudgetFixture) RoundTrip(r *http.Request) (*http.Response, error) {
	f.calls++
	return nil, io.EOF
}

func TestSourceRequestBudgetRejectsOpaqueTransportBeforeSending(t *testing.T) {
	c := NewClient()
	next := &opaqueBudgetFixture{}
	c.http.Transport = next
	ctx, b, _ := research.WithSourceHTTPBudget(context.Background(), 2)
	_, _, err := c.get(ctx, "https://example.test/search", requestOptions{SourceID: "fixture", Host: "example.test"})
	var sourceErr *research.SourceError
	if !errors.As(err, &sourceErr) || sourceErr.Code != research.FailureInvalidData || next.calls != 0 {
		t.Fatal(err, next.calls)
	}
	if used, denied := b.Usage(); used != 0 || denied != 0 {
		t.Fatal(used, denied)
	}
}

func (f *requestBudgetFixture) RoundTrip(r *http.Request) (*http.Response, error) {
	f.calls++
	h := http.Header{}
	if f.redirect {
		h.Set("Location", "/redirected")
	}
	return &http.Response{StatusCode: f.status, Header: h, Body: io.NopCloser(strings.NewReader(`{"public":true}`)), Request: r}, nil
}

func TestSourceRequestBudgetCountsRetriesAndStopsWithoutRetryingBudgetError(t *testing.T) {
	client := NewClient()
	next := &requestBudgetFixture{status: 503}
	client.http.Transport = next
	ctx, b, _ := research.WithSourceHTTPBudget(context.Background(), 2)
	_, _, err := client.get(ctx, "https://example.test/search", requestOptions{SourceID: "fixture", Host: "example.test"})
	var sourceErr *research.SourceError
	if !errors.As(err, &sourceErr) || sourceErr.Code != research.FailureRequestBudget || sourceErr.Retryable || next.calls != 2 {
		t.Fatal(err, next.calls)
	}
	if used, denied := b.Usage(); used != 2 || denied != 1 {
		t.Fatal(used, denied)
	}
}

func TestSourceRequestBudgetCountsRedirectsAndDoesNotChargeCacheHits(t *testing.T) {
	client := NewClient()
	next := &requestBudgetFixture{status: 302, redirect: true}
	client.http.Transport = next
	ctx, b, _ := research.WithSourceHTTPBudget(context.Background(), 1)
	_, _, err := client.get(ctx, "https://example.test/search", requestOptions{SourceID: "fixture", Host: "example.test"})
	var sourceErr *research.SourceError
	if !errors.As(err, &sourceErr) || sourceErr.Code != research.FailureRequestBudget || next.calls != 1 {
		t.Fatal(err, next.calls)
	}
	if used, _ := b.Usage(); used != 1 {
		t.Fatal(used)
	}
	client = NewClient()
	next = &requestBudgetFixture{status: 200}
	client.http.Transport = next
	ctx, b, _ = research.WithSourceHTTPBudget(context.Background(), 1)
	for i := 0; i < 2; i++ {
		if err := client.getJSON(ctx, "https://example.test/search", requestOptions{SourceID: "fixture", Host: "example.test", Cache: true}, &syntaxFixtureResponse{}); err != nil {
			t.Fatal(err)
		}
	}
	if used, denied := b.Usage(); used != 1 || denied != 0 || next.calls != 1 {
		t.Fatal(used, denied, next.calls)
	}
}

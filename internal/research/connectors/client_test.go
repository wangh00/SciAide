package connectors

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appresearch "github.com/wangh00/SciAide/internal/app/research"
)

func TestClientRetriesCachesAndClassifiesFailures(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if calls.Add(1) == 1 {
			writer.Header().Set("Retry-After", "0")
			writer.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	client := newTestClient(server.Client())
	client.attempts = 2
	host := endpointHost(server.URL)
	var value struct {
		OK bool `json:"ok"`
	}
	if err := client.getJSON(context.Background(), server.URL, requestOptions{SourceID: "fixture", Host: host, Cache: true}, &value); err != nil {
		t.Fatal(err)
	}
	if !value.OK || calls.Load() != 2 {
		t.Fatalf("value=%#v calls=%d", value, calls.Load())
	}
	value.OK = false
	if err := client.getJSON(context.Background(), server.URL, requestOptions{SourceID: "fixture", Host: host, Cache: true}, &value); err != nil {
		t.Fatal(err)
	}
	if !value.OK || calls.Load() != 2 {
		t.Fatalf("cache value=%#v calls=%d", value, calls.Load())
	}
}

func TestClientRejectsHostEscapeOversizeAndHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/slow" {
			<-request.Context().Done()
			return
		}
		_, _ = writer.Write([]byte(strings.Repeat("x", 128)))
	}))
	defer server.Close()
	client := newTestClient(server.Client())
	client.maxResponse = 32
	if _, _, err := client.get(context.Background(), server.URL, requestOptions{SourceID: "fixture", Host: "wrong.test"}); err == nil {
		t.Fatal("host escape was accepted")
	}
	if _, _, err := client.get(context.Background(), server.URL, requestOptions{SourceID: "fixture", Host: endpointHost(server.URL)}); err == nil {
		t.Fatal("oversized response was accepted")
	}
	client.maxResponse = 1024
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := client.get(ctx, server.URL+"/slow", requestOptions{SourceID: "fixture", Host: endpointHost(server.URL)})
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestClientDoesNotCollapseNotFoundIntoEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	client := newTestClient(server.Client())
	client.attempts = 1
	_, _, err := client.get(context.Background(), server.URL, requestOptions{SourceID: "fixture", Host: endpointHost(server.URL)})
	var sourceErr *appresearch.SourceError
	if !errors.As(err, &sourceErr) || sourceErr.Code != appresearch.FailureNotFound {
		t.Fatalf("error = %#v", err)
	}
}

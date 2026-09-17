package agent

import (
	"context"
	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"testing"
	"time"
)

func TestDefaultRetryDelayStartsSlowAndGrowsGently(t *testing.T) {
	bases := []time.Duration{4 * time.Second, 6 * time.Second, 9 * time.Second, 13500 * time.Millisecond, 20250 * time.Millisecond}
	for index, base := range bases {
		for n := 0; n < 100; n++ {
			got := defaultRetryDelay(index)
			if got < base-base/10 || got > base+base/10 {
				t.Fatalf("delay[%d]=%s outside jitter range", index, got)
			}
		}
	}
	if maxRequestRetries != 5 || maxStreamRetries != 5 {
		t.Fatal("both retry budgets must default to five")
	}
}
func TestOpeningRequestStopsAfterFiveRetries(t *testing.T) {
	provider := &openingFailureModel{inner: fake.New(), remaining: 20, err: &apperr.Error{Code: "MODEL_UNAVAILABLE", Retryable: true}}
	waits := 0
	loop := &Loop{observer: NopObserver{}, retryDelay: func(int) time.Duration { return 4 * time.Second }, sleep: func(context.Context, time.Duration) error { waits++; return nil }}
	_, err := loop.openModelStream(context.Background(), chat.Run{ID: "run"}, provider, model.ChatRequest{})
	if err == nil || provider.Attempts() != 6 || waits != 5 {
		t.Fatalf("attempts=%d waits=%d err=%v", provider.Attempts(), waits, err)
	}
}
func TestRetryWaitHonorsLongerServerDelay(t *testing.T) {
	var waited time.Duration
	loop := &Loop{observer: NopObserver{}, retryDelay: func(int) time.Duration { return 4 * time.Second }, sleep: func(_ context.Context, d time.Duration) error { waited = d; return nil }}
	if err := loop.retryWait(context.Background(), chat.Run{ID: "run"}, "request", 1, 5, &apperr.Error{Retryable: true, RetryAfter: 30 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if waited != 30*time.Second {
		t.Fatalf("wait=%s", waited)
	}
}

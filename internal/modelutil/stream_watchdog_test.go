package modelutil

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/apperr"
)

func TestStreamInactivityDefaultsAndHostCeiling(t *testing.T) {
	for _, seconds := range []int{0, 5, 60, 300} {
		if got := StreamInactivityTimeout(seconds, 0); got != 5*time.Minute {
			t.Fatal(seconds, got)
		}
	}
	if StreamInactivityTimeout(600, 0) != 10*time.Minute || StreamInactivityTimeout(60, time.Minute) != time.Minute {
		t.Fatal("configured grace or recovery ceiling lost")
	}
}

func TestStreamWatchdogDoesNotTreatKeepaliveAsProgress(t *testing.T) {
	for _, value := range []string{"", ": ping\n\n", "data: {\"type\":\"response.in_progress\"}\n\n", "data: {\"type\":\"response.output_text.delta\",\"delta\":\""} {
		t.Run(value, func(t *testing.T) {
			reader, writer := io.Pipe()
			decoder, body := NewProgressSSEDecoder(context.Background(), reader, 4096, 90*time.Millisecond)
			defer body.Close()
			defer writer.Close()
			go func() {
				for {
					if _, err := io.WriteString(writer, value); err != nil {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
			}()
			started := time.Now()
			for decoder.Scan() {
			} // metadata deliberately does not MarkProgress
			var appErr *apperr.Error
			err := ClassifySSEReadError(decoder.Err())
			if !errors.As(err, &appErr) || appErr.Code != "MODEL_STREAM_STALLED" || !appErr.Retryable || time.Since(started) > 2*time.Second {
				t.Fatal(err, time.Since(started))
			}
		})
	}
}

func TestStreamWatchdogAllowsLongHealthyStreamAndStopsOnClose(t *testing.T) {
	reader, writer := io.Pipe()
	decoder, body := NewProgressSSEDecoder(context.Background(), reader, 4096, 150*time.Millisecond)
	defer body.Close()
	defer writer.Close()
	go func() {
		defer writer.Close()
		for i := 0; i < 6; i++ {
			if _, err := io.WriteString(writer, "data: increment\n\n"); err != nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	started := time.Now()
	count := 0
	for decoder.Scan() {
		if decoder.Text() != "increment" {
			t.Fatal(decoder.Text())
		}
		decoder.MarkProgress()
		count++
	}
	if decoder.Err() != nil || count != 6 || time.Since(started) < 250*time.Millisecond {
		t.Fatal(count, decoder.Err())
	}
	guard := body.(*progressBody)
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(160 * time.Millisecond)
	guard.mu.Lock()
	expired := guard.expired
	guard.mu.Unlock()
	if expired {
		t.Fatal("timer survived closed stream")
	}
}

func TestStreamWatchdogPreservesUserCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	decoder, body := NewProgressSSEDecoder(ctx, reader, 4096, time.Second)
	defer writer.Close()
	defer body.Close()
	cancel()
	_ = body.Close()
	for decoder.Scan() {
	}
	if err := ClassifySSEReadError(decoder.Err()); !errors.Is(err, context.Canceled) || IsRetryable(err) {
		t.Fatal(err)
	}
}

func TestStreamWatchdogDoesNotReturnLateBytesAfterExpiration(t *testing.T) {
	body := newProgressBody(context.Background(), io.NopCloser(strings.NewReader("late result")), 50*time.Millisecond)
	defer body.Close()
	time.Sleep(100 * time.Millisecond)
	buf := make([]byte, 64)
	n, err := body.Read(buf)
	if n != 0 || !errors.Is(err, errStreamStalled) {
		t.Fatal(n, err)
	}
}

func TestStreamWatchdogQueuedTimerRechecksRecentProgress(t *testing.T) {
	body := newProgressBody(context.Background(), io.NopCloser(strings.NewReader("")), time.Second)
	defer body.Close()
	body.progress()
	body.expire() // simulate a callback already queued before Reset
	body.mu.Lock()
	expired := body.expired
	body.mu.Unlock()
	if expired {
		t.Fatal("stale timer callback aborted a progressing response")
	}
}

func TestFirstIdleLeaseDeductsEstablishmentAndProgressRenewsNormalLease(t *testing.T) {
	for _, progress := range []bool{false, true} {
		reader, writer := io.Pipe()
		started := time.Now()
		decoder, body := NewProgressSSEDecoder(context.Background(), reader, 4096, 180*time.Millisecond, started.Add(60*time.Millisecond))
		if progress {
			decoder.MarkProgress()
		}
		for decoder.Scan() {
		}
		_ = body.Close()
		_ = writer.Close()
		elapsed := time.Since(started)
		if !errors.Is(decoder.Err(), errStreamStalled) || (!progress && elapsed > 150*time.Millisecond) || (progress && elapsed < 150*time.Millisecond) {
			t.Fatal(progress, elapsed, decoder.Err())
		}
	}
}

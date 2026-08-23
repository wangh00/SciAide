package agent

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/modelcap"
	"github.com/wangh00/SciAide/internal/modelutil"
)

const (
	maxRequestRetries = 4
	maxStreamRetries  = 5
)

type RetryStatus struct {
	Phase       string `json:"phase"`
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"maxAttempts"`
	DelayMillis int64  `json:"delayMillis"`
	Message     string `json:"message"`
}

type streamAttempt struct {
	turn              modelTurn
	checkpointSummary string
	usages            []model.Usage
}

type streamReceiver func(model.Stream) (streamAttempt, error)

func defaultRetryDelay(retryIndex int) time.Duration {
	if retryIndex < 0 {
		retryIndex = 0
	}
	if retryIndex > 8 {
		retryIndex = 8
	}
	base := 200 * time.Millisecond * time.Duration(1<<retryIndex)
	if base > 60*time.Second {
		base = 60 * time.Second
	}
	jitter := base / 10
	if jitter <= 0 {
		return base
	}
	return base - jitter + time.Duration(rand.Int64N(int64(jitter*2)+1))
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (l *Loop) retryWait(ctx context.Context, run chat.Run, phase string, attempt, maxAttempts int, err error) error {
	delay := l.retryDelay(attempt - 1)
	if retryAfter := modelutil.RetryAfter(err); retryAfter > delay {
		delay = retryAfter
	}
	message := fmt.Sprintf("连接中断，正在重连 %d/%d", attempt, maxAttempts)
	if phase == "request" {
		message = fmt.Sprintf("请求未建立，正在重试 %d/%d", attempt, maxAttempts)
	}
	l.observer.Retrying(run, RetryStatus{Phase: phase, Attempt: attempt, MaxAttempts: maxAttempts, DelayMillis: delay.Milliseconds(), Message: message})
	return l.sleep(ctx, delay)
}

func (l *Loop) openModelStream(ctx context.Context, run chat.Run, chatModel model.ChatModel, request model.ChatRequest) (model.Stream, error) {
	retried := false
	for retry := 0; ; retry++ {
		stream, err := chatModel.Stream(ctx, request)
		if err == nil {
			if retried {
				l.observer.RetryRecovered(run)
			}
			return stream, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !modelutil.IsRetryable(err) || retry >= maxRequestRetries {
			return nil, err
		}
		retried = true
		if err := l.retryWait(ctx, run, "request", retry+1, maxRequestRetries, err); err != nil {
			return nil, err
		}
	}
}

func (l *Loop) runModelStream(ctx context.Context, run chat.Run, chatModel model.ChatModel, request model.ChatRequest, receive streamReceiver) (streamAttempt, modelcap.ReasoningLevel, error) {
	streamRetries := 0
	reconnecting := false
	for {
		stream, err := l.openModelStream(ctx, run, chatModel, request)
		if err != nil {
			return streamAttempt{}, "", err
		}
		if reconnecting {
			l.observer.RetryRecovered(run)
			reconnecting = false
		}
		actualReasoningLevel := request.ResolvedReasoningLevel
		if reporter, ok := stream.(model.ReasoningResolutionReporter); ok {
			actualReasoningLevel = reporter.ReasoningResolution().Resolved
		}
		result, receiveErr := receive(stream)
		_ = stream.Close()
		if receiveErr == nil {
			return result, actualReasoningLevel, nil
		}
		if ctx.Err() != nil {
			return result, actualReasoningLevel, ctx.Err()
		}
		if !modelutil.IsRetryable(receiveErr) || streamRetries >= maxStreamRetries {
			return result, actualReasoningLevel, receiveErr
		}
		streamRetries++
		if err := l.retryWait(ctx, run, "stream", streamRetries, maxStreamRetries, receiveErr); err != nil {
			return streamAttempt{}, "", err
		}
		reconnecting = true
	}
}

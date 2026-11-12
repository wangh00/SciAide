package agent

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/modelcap"
	"github.com/wangh00/SciAide/internal/modelutil"
)

const (
	maxRequestRetries          = 5
	maxStreamRetries           = 5
	defaultModelRecoveryBudget = 12 * time.Minute
)

type RetryStatus = chat.ModelRetryStatus

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
	// Start around four seconds, then grow gently instead of rapid-fire retries.
	base := 4 * time.Second
	for i := 0; i < retryIndex && base < 60*time.Second; i++ {
		base = base * 3 / 2
		if base > 60*time.Second {
			base = 60 * time.Second
		}
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

func (l *Loop) retryWait(ctx context.Context, run chat.Run, phase string, attempt, maxAttempts int, err error, recovery ...*recoveryAllowance) error {
	delay := l.retryDelay(attempt - 1)
	if retryAfter := modelutil.RetryAfter(err); retryAfter > delay {
		delay = retryAfter
	}
	message := fmt.Sprintf("连接中断，正在重连 %d/%d", attempt, maxAttempts)
	if phase == "request" {
		message = fmt.Sprintf("请求未建立，正在重试 %d/%d", attempt, maxAttempts)
	}
	reason := ""
	var appErr *apperr.Error
	if errors.As(err, &appErr) {
		switch appErr.Code {
		case "MODEL_RATE_LIMITED":
			reason = "rate_limit"
			message = fmt.Sprintf("模型服务限流，正在等待后重试 %d/%d", attempt, maxAttempts)
		case "MODEL_UNAVAILABLE":
			reason = "service_unavailable"
			message = fmt.Sprintf("模型服务暂时不可用，正在等待后重试 %d/%d", attempt, maxAttempts)
		case "MODEL_STREAM_STALLED":
			reason = "stream_stalled"
			message = fmt.Sprintf("模型流长时间没有有效进展，正在重连 %d/%d", attempt, maxAttempts)
		}
	}
	if len(recovery) > 0 && recovery[0].reserved() {
		reason = "bounded_recovery"
		attempt, maxAttempts = 1, 1
		message = "长响应首次失败，正在进行唯一一次限时恢复；已完成工具不会重跑"
	}
	l.observer.Retrying(run, RetryStatus{Phase: phase, Reason: reason, Attempt: attempt, MaxAttempts: maxAttempts, DelayMillis: delay.Milliseconds(), Message: message})
	return l.sleep(ctx, delay)
}

func (l *Loop) openModelStream(ctx context.Context, run chat.Run, chatModel model.ChatModel, request model.ChatRequest, activityPhase ...string) (model.Stream, error) {
	stream, _, err := l.openModelStreamAttempt(ctx, run, chatModel, request, nil, activityPhase...)
	return stream, err
}

type recoveryAllowance struct {
	limit, spent                      time.Duration
	opening, waiting, failedReceiving time.Duration
	failedIdleTail                    time.Duration
	requestAttempts, streamAttempts   int
	reserveDeadline                   time.Time
	lastError                         error
}

func (a *recoveryAllowance) reserved() bool { return a != nil && !a.reserveDeadline.IsZero() }

// A long, productive first response may fail only after consuming the normal
// allowance. Preserve one recovery attempt, not a fresh retry loop. Its deadline
// includes backoff, establishment AND generation and can never be renewed.
func (a *recoveryAllowance) reserveFirstLongResponse(started, ended, lastProgress time.Time) bool {
	if a.reserved() || a.streamAttempts != 1 || a.remaining() > 0 || ended.Sub(started) < a.limit ||
		lastProgress.Before(started) || lastProgress.After(ended) {
		return false
	}
	a.reserveDeadline = ended.Add(a.limit)
	return true
}

func (l *Loop) retryReceiving(run chat.Run, phase string, attempt, maxAttempts int) {
	if observer, ok := l.observer.(interface{ RetryReceiving(chat.Run, RetryStatus) }); ok {
		observer.RetryReceiving(run, RetryStatus{Phase: phase, State: "receiving", Attempt: attempt, MaxAttempts: maxAttempts, Message: "已重新连接，等待本次模型结果完成"})
	}
}

func (a *recoveryAllowance) remaining() time.Duration {
	if a.reserved() {
		return max(0, time.Until(a.reserveDeadline))
	}
	return max(0, a.limit-a.spent)
}

type cancelOnCloseStream struct {
	model.Stream
	cancel context.CancelCauseFunc
}

func (s *cancelOnCloseStream) Close() error { s.cancel(context.Canceled); return s.Stream.Close() }

func (s *cancelOnCloseStream) LastProgressAt() time.Time { return model.LastStreamProgress(s.Stream) }

func (s *cancelOnCloseStream) PendingToolArgumentChars() int {
	return model.PendingToolArgumentChars(s.Stream)
}

// Bound only establishment by the remaining allowance; cancel ownership follows
// the returned body so a healthy streaming response has no total deadline.
func openWithinRecoveryAllowance(ctx context.Context, chatModel model.ChatModel, request model.ChatRequest, allowance *recoveryAllowance) (model.Stream, error) {
	if allowance == nil {
		return chatModel.Stream(ctx, request)
	}
	remaining := allowance.remaining()
	request.StreamRecoveryDeadline = time.Now().Add(remaining)
	openCtx, cancel := context.WithCancelCause(ctx)
	var mu sync.Mutex
	established := false
	timer := time.AfterFunc(remaining, func() {
		mu.Lock()
		defer mu.Unlock()
		if !established {
			cancel(context.DeadlineExceeded)
		}
	})
	stream, err := chatModel.Stream(openCtx, request)
	mu.Lock()
	established = true
	mu.Unlock()
	timer.Stop()
	if err != nil || openCtx.Err() != nil {
		cause := context.Cause(openCtx)
		cancel(context.Canceled)
		if stream != nil {
			_ = stream.Close()
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(cause, context.DeadlineExceeded) {
			// WithCancelCause exposes context.Canceled as Err even when the host
			// timer supplied DeadlineExceeded. Do not let this internal budget
			// expiration masquerade as an explicit user cancellation at Run.
			if errors.Is(err, context.Canceled) {
				err = context.DeadlineExceeded
			}
			return nil, recoveryTimeExhausted(err, allowance)
		}
		return nil, err
	}
	wrapped := model.Stream(&cancelOnCloseStream{Stream: stream, cancel: cancel})
	if reporter, ok := stream.(model.ReasoningResolutionReporter); ok {
		r := reporter.ReasoningResolution()
		wrapped = model.WithReasoningResolution(wrapped, r.Requested, r.Resolved)
	}
	return wrapped, nil
}

func recoveryTimeExhausted(err error, allowance *recoveryAllowance) error {
	if err == nil {
		err = allowance.lastError
	}
	details := allowance.diagnostics()
	var original *apperr.Error
	if errors.As(err, &original) && original.Details != "" {
		details += "\n" + original.Details
	}
	message := fmt.Sprintf("模型连接恢复时间已用尽（本轮实际重试 %d 次）。%s已完成的阶段和工具结果保留，可稍后重试当前阶段。", max(0, allowance.requestAttempts-1), recoveryFailureReason(allowance.lastError))
	if allowance.reserved() {
		message = fmt.Sprintf("长响应的一次限时恢复已超时（本轮实际重试 %d 次）。%s已完成的阶段和工具结果保留，可稍后重试当前阶段。", max(0, allowance.requestAttempts-1), recoveryFailureReason(allowance.lastError))
		// Some adapters expose a cancelled child context for an internal
		// deadline. The caller has already checked the user's context; do not
		// let Loop.Run mistake this internal stop for a manual cancellation.
		if errors.Is(err, context.Canceled) {
			err = context.DeadlineExceeded
		}
	}
	return modelutil.ErrorWithDetails("MODEL_RECOVERY_TIMEOUT", message, details, true, err)
}

func (a *recoveryAllowance) diagnostics() string {
	details := fmt.Sprintf("Recovery allowance=%s; spent=%s; connection=%s; backoff=%s; failed_receive=%s; failed_idle_tail=%s (included in failed_receive); request_attempts=%d; stream_attempts=%d; bounded_reserve=%t; no incomplete result was accepted.", a.limit, a.spent, a.opening, a.waiting, a.failedReceiving, a.failedIdleTail, a.requestAttempts, a.streamAttempts, a.reserved())
	if a.reserved() {
		details += " reserve_deadline=" + a.reserveDeadline.Format(time.RFC3339Nano)
	}
	if a.lastError != nil {
		public := apperr.Public(a.lastError)
		details += "\nLast failure: " + public.Code + "\n" + public.Details
	}
	return details
}

func recoveryFailureReason(err error) string {
	var e *apperr.Error
	if errors.As(err, &e) {
		switch e.Code {
		case "MODEL_STREAM_STALLED":
			return "最后原因：响应流长时间无有效进展。"
		case "MODEL_STREAM_INTERRUPTED":
			return "最后原因：响应流未正常结束。"
		case "MODEL_RATE_LIMITED":
			return "最后原因：模型服务限流。"
		case "MODEL_UNAVAILABLE":
			return "最后原因：模型连接或服务不可用。"
		case "MODEL_TIMEOUT":
			return "最后原因：模型请求超时。"
		}
	}
	return "当前请求已停止。"
}

func recoveryReserveFailed(err error, allowance *recoveryAllowance) error {
	var e *apperr.Error
	if !errors.As(err, &e) {
		return err
	}
	copy := *e
	copy.Cause = err
	copy.UserMessage = fmt.Sprintf("长响应的一次限时恢复未完成（本轮实际重试 %d 次）。%s已完成阶段和工具结果保留，可稍后重试当前阶段。", max(0, allowance.requestAttempts-1), recoveryFailureReason(err))
	copy.Details = allowance.diagnostics() + "\n" + e.Details
	return &copy
}

func (l *Loop) boundedRetryWait(ctx context.Context, run chat.Run, phase string, attempt, maxAttempts int, cause error, allowance *recoveryAllowance) error {
	if allowance == nil {
		return l.retryWait(ctx, run, phase, attempt, maxAttempts, cause)
	}
	if allowance.remaining() <= 0 {
		return recoveryTimeExhausted(cause, allowance)
	}
	waitCtx, cancel := context.WithTimeout(ctx, allowance.remaining())
	defer cancel()
	started := time.Now()
	err := l.retryWait(waitCtx, run, phase, attempt, maxAttempts, cause, allowance)
	elapsed := time.Since(started)
	allowance.spent += elapsed
	allowance.waiting += elapsed
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if waitCtx.Err() != nil || allowance.remaining() <= 0 {
		return recoveryTimeExhausted(cause, allowance)
	}
	return err
}

func (l *Loop) openModelStreamAttempt(ctx context.Context, run chat.Run, chatModel model.ChatModel, request model.ChatRequest, allowance *recoveryAllowance, activityPhase ...string) (model.Stream, bool, error) {
	retried := false
	for retry := 0; ; retry++ {
		if allowance != nil && allowance.remaining() <= 0 {
			return nil, retried, recoveryTimeExhausted(nil, allowance)
		}
		phase := "model_wait"
		if len(activityPhase) > 0 {
			phase = activityPhase[0]
		}
		l.publishRuntimeActivity(run, phase)
		started := time.Now()
		if allowance != nil && (request.StreamInactivityTimeout <= 0 || request.StreamInactivityTimeout > allowance.remaining()) {
			request.StreamInactivityTimeout = allowance.remaining()
		}
		if allowance != nil {
			allowance.requestAttempts++
		}
		stream, err := openWithinRecoveryAllowance(ctx, chatModel, request, allowance)
		if allowance != nil {
			elapsed := time.Since(started)
			allowance.spent += elapsed
			allowance.opening += elapsed
		}
		if err == nil {
			if retried {
				l.retryReceiving(run, "request", retry, maxRequestRetries)
			}
			return stream, retried, nil
		}
		l.recordRecoveryFailure(run, "request", retry+1, started, time.Now(), streamAttempt{}, err)
		if ctx.Err() != nil {
			return nil, retried, ctx.Err()
		}
		if allowance != nil {
			allowance.lastError = err
			if allowance.reserved() {
				return nil, retried, recoveryReserveFailed(err, allowance)
			}
		}
		if !modelutil.IsRetryable(err) || retry >= maxRequestRetries {
			if modelutil.IsRetryable(err) && retry >= maxRequestRetries {
				err = retryExhaustedError(err, "request", maxRequestRetries)
			}
			return nil, retried, err
		}
		retried = true
		if err := l.boundedRetryWait(ctx, run, "request", retry+1, maxRequestRetries, err, allowance); err != nil {
			return nil, retried, err
		}
	}
}

func (l *Loop) runModelStream(ctx context.Context, run chat.Run, chatModel model.ChatModel, request model.ChatRequest, receive streamReceiver, activityPhase ...string) (streamAttempt, modelcap.ReasoningLevel, error) {
	streamRetries := 0
	reconnecting := false
	retried := false
	limit := l.modelRecoveryBudget
	if limit <= 0 {
		limit = defaultModelRecoveryBudget
	}
	allowance := &recoveryAllowance{limit: limit}
	for {
		if allowance.remaining() <= 0 {
			return streamAttempt{}, "", recoveryTimeExhausted(nil, allowance)
		}
		attemptRequest := request
		if attemptRequest.StreamInactivityTimeout <= 0 || attemptRequest.StreamInactivityTimeout > allowance.remaining() {
			attemptRequest.StreamInactivityTimeout = allowance.remaining()
		}
		attemptCtx := ctx
		cancelAttempt := func() {}
		if allowance.reserved() {
			attemptCtx, cancelAttempt = context.WithDeadline(ctx, allowance.reserveDeadline)
		}
		stream, openingRetried, err := l.openModelStreamAttempt(attemptCtx, run, chatModel, attemptRequest, allowance, activityPhase...)
		retried = retried || openingRetried
		if err != nil {
			attemptExpired := attemptCtx.Err() != nil
			cancelAttempt()
			if ctx.Err() != nil {
				return streamAttempt{}, "", ctx.Err()
			}
			if allowance.reserved() && (attemptExpired || allowance.remaining() <= 0) {
				return streamAttempt{}, "", recoveryTimeExhausted(err, allowance)
			}
			return streamAttempt{}, "", err
		}
		if reconnecting {
			if allowance.reserved() {
				if observer, ok := l.observer.(interface{ RetryReceiving(chat.Run, RetryStatus) }); ok {
					observer.RetryReceiving(run, RetryStatus{Phase: "stream", State: "receiving", Reason: "bounded_recovery", Attempt: 1, MaxAttempts: 1, Message: "已重新连接，正在限时恢复未完成的回答；此次不会再次自动重连"})
				}
			} else {
				l.retryReceiving(run, "stream", streamRetries, maxStreamRetries)
			}
			reconnecting = false
		}
		actualReasoningLevel := request.ResolvedReasoningLevel
		if reporter, ok := stream.(model.ReasoningResolutionReporter); ok {
			actualReasoningLevel = reporter.ReasoningResolution().Resolved
		}
		started := time.Now()
		allowance.streamAttempts++
		result, receiveErr := receive(stream)
		ended := time.Now()
		lastProgress := model.LastStreamProgress(stream)
		attemptExpired := attemptCtx.Err() != nil
		result.turn.pendingToolArgumentChars = model.PendingToolArgumentChars(stream)
		_ = stream.Close()
		cancelAttempt()
		if ctx.Err() != nil {
			return result, actualReasoningLevel, ctx.Err()
		}
		if allowance.reserved() && (attemptExpired || !ended.Before(allowance.reserveDeadline)) {
			allowance.failedReceiving += max(0, ended.Sub(started))
			allowance.spent += max(0, ended.Sub(started))
			allowance.failedIdleTail += failedIdleTail(started, ended, lastProgress)
			err := recoveryTimeExhausted(receiveErr, allowance)
			l.recordRecoveryFailure(run, "stream", streamRetries+1, started, ended, result, err)
			return result, actualReasoningLevel, err
		}
		if receiveErr == nil {
			if retried {
				l.observer.RetryRecovered(run)
			}
			return result, actualReasoningLevel, nil
		}
		l.recordRecoveryFailure(run, "stream", streamRetries+1, started, ended, result, receiveErr)
		// A disconnected response has not produced an accepted unit. Its entire
		// receive time is lost work, even if the final delta arrived just before
		// failure. Normal productive streams have no total deadline; the single
		// reserved recovery does have a hard deadline, even with fresh deltas.
		allowance.spent += max(0, ended.Sub(started))
		allowance.failedReceiving += max(0, ended.Sub(started))
		allowance.failedIdleTail += failedIdleTail(started, ended, lastProgress)
		allowance.lastError = receiveErr
		if allowance.reserved() {
			return result, actualReasoningLevel, recoveryReserveFailed(receiveErr, allowance)
		}
		if modelutil.IsRetryable(receiveErr) && allowance.remaining() <= 0 {
			if !allowance.reserveFirstLongResponse(started, ended, lastProgress) {
				return result, actualReasoningLevel, recoveryTimeExhausted(receiveErr, allowance)
			}
		}
		if !modelutil.IsRetryable(receiveErr) || streamRetries >= maxStreamRetries {
			if modelutil.IsRetryable(receiveErr) && streamRetries >= maxStreamRetries {
				receiveErr = retryExhaustedError(receiveErr, "stream", maxStreamRetries)
			}
			return result, actualReasoningLevel, receiveErr
		}
		streamRetries++
		retried = true
		if err := l.boundedRetryWait(ctx, run, "stream", streamRetries, maxStreamRetries, receiveErr, allowance); err != nil {
			return result, actualReasoningLevel, err
		}
		reconnecting = true
	}
}

func failedIdleTail(started, ended, lastProgress time.Time) time.Duration {
	if lastProgress.Before(started) || lastProgress.After(ended) {
		return max(0, ended.Sub(started))
	}
	return max(0, ended.Sub(lastProgress))
}

func retryExhaustedError(err error, phase string, retries int) error {
	var original *apperr.Error
	if !errors.As(err, &original) {
		return err
	}
	copy := *original
	copy.Cause = err
	copy.UserMessage = fmt.Sprintf("模型服务恢复重试已达上限（已重试 %d 次），当前请求已停止。请稍后重试。", retries)
	if copy.Details != "" {
		copy.Details += "\n"
	}
	copy.Details += fmt.Sprintf("Recovery stopped after %d %s retries; no incomplete result was accepted.", retries, phase)
	return &copy
}

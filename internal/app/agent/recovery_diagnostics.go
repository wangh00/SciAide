package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/modelutil"
)

func (l *Loop) recordRecoveryFailure(run chat.Run, phase string, attempt int, started, ended time.Time, result streamAttempt, err error) {
	repository, ok := l.runs.(chat.ModelRecoveryFailureRepository)
	if !ok {
		return
	}
	public := apperr.Public(err)
	var classified *apperr.Error
	hasClassified := errors.As(err, &classified)
	controlReason := ""
	// A primary typed failure owns its identity, even when a cancellation or
	// deadline also appears in its cause. Do not consult the current Run context:
	// a real failure may have happened before the caller cancelled the request.
	if !hasClassified {
		switch {
		case errors.Is(err, context.Canceled):
			public.Code, public.Message = "REQUEST_INTERRUPTED", "本次模型请求已取消或随宿主关闭而中断。"
			controlReason = "context_canceled"
		case errors.Is(err, context.DeadlineExceeded):
			public.Code, public.Message = "MODEL_TIMEOUT", "本次模型请求的宿主截止时间已到。"
			controlReason = "context_deadline_exceeded"
		}
	}
	fields := map[string]any{"code": public.Code, "message": public.Message}
	if controlReason != "" {
		fields["source"], fields["reason"] = "host_control", controlReason
	} else if trace, ok := modelutil.HTTPFailureTrace(err); ok {
		// A transport failure has no HTTP error body to parse. Do not hash the
		// local explanation as if it were malformed provider JSON.
		fields["source"], fields["network"] = "host_network", trace
	} else if public.Details != "" {
		// Untrusted provider/legacy details remain projected as safe metadata.
		fields["details"] = modelutil.SanitizeProviderDiagnostic(public.Details)
	} else {
		// No diagnostic body was supplied. Error() is not a provider payload.
		fields["source"] = "error_identity"
	}
	if hasClassified && classified.HTTPStatus >= 100 && classified.HTTPStatus <= 599 {
		fields["httpStatus"] = classified.HTTPStatus // typed status, never parsed from source text
	}
	payload, _ := json.Marshal(fields)
	partialChars := utf8.RuneCountInString(result.turn.text) + utf8.RuneCountInString(result.turn.commentary) + result.turn.pendingToolArgumentChars
	for _, call := range result.turn.toolCalls {
		partialChars += utf8.RuneCount(call.Arguments)
	}
	value := chat.ModelRecoveryFailure{
		RunID: run.ID, TurnIndex: run.ModelTurns, Phase: phase, Attempt: attempt,
		Code: public.Code, Details: modelutil.ProviderErrorDetails(phase, 0, payload),
		PartialChars:  partialChars,
		ElapsedMillis: max(0, ended.Sub(started).Milliseconds()), StartedAt: started, CompletedAt: ended,
	}
	// Cancellation must not discard the error which preceded it. Diagnostics
	// are best-effort, unlike the source checkpoint which is fail-closed.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = repository.RecordModelRecoveryFailure(ctx, value)
}

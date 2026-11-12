package modelutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// This is an inactivity limit, not a deadline on a healthy long response.
// Opaque reasoning gets a conservative grace period; a larger user-configured
// connection timeout also extends that grace. The agent may lower the ceiling
// when the same unfinished turn has already spent its recovery allowance.
const DefaultStreamInactivityTimeout = 5 * time.Minute

var errStreamStalled = errors.New("model stream made no effective progress")

func StreamInactivityTimeout(responseTimeoutSeconds int, recoveryCeiling time.Duration) time.Duration {
	timeout := max(DefaultStreamInactivityTimeout, time.Duration(responseTimeoutSeconds)*time.Second)
	if recoveryCeiling > 0 {
		timeout = min(timeout, recoveryCeiling)
	}
	return timeout
}

// progressBody closes the HTTP body to unblock a pending Read. Merely receiving
// bytes does not renew its lease: SSE comments, ping and repeated lifecycle
// metadata must not keep a stalled model request alive forever.
type progressBody struct {
	body         io.ReadCloser
	ctx          context.Context
	timeout      time.Duration
	mu           sync.Mutex
	timer        *time.Timer
	deadline     time.Time
	lastProgress time.Time
	closed       bool
	expired      bool
	once         sync.Once
	closeErr     error
}

func newProgressBody(ctx context.Context, body io.ReadCloser, timeout time.Duration, firstProgressDeadline ...time.Time) *progressBody {
	deadline := time.Now().Add(timeout)
	if len(firstProgressDeadline) > 0 && !firstProgressDeadline[0].IsZero() && firstProgressDeadline[0].Before(deadline) {
		deadline = firstProgressDeadline[0]
	}
	p := &progressBody{body: body, ctx: ctx, timeout: timeout, deadline: deadline}
	p.timer = time.AfterFunc(max(0, time.Until(deadline)), p.expire)
	return p
}

func (p *progressBody) expire() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	// A stopped timer callback may already be queued while progress resets it.
	if remaining := time.Until(p.deadline); remaining > 0 {
		p.timer.Reset(remaining)
		p.mu.Unlock()
		return
	}
	p.expired, p.closed = true, true
	p.mu.Unlock()
	p.closeBody()
}

func (p *progressBody) progress() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.lastProgress = time.Now()
		p.deadline = p.lastProgress.Add(p.timeout)
		p.timer.Reset(p.timeout)
	}
}

func (p *progressBody) LastProgressAt() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastProgress
}

func (p *progressBody) Read(data []byte) (int, error) {
	n, err := p.body.Read(data)
	if p.ctx.Err() != nil {
		return 0, p.ctx.Err()
	}
	p.mu.Lock()
	expired := p.expired
	p.mu.Unlock()
	if expired {
		return 0, fmt.Errorf("%w for %s", errStreamStalled, p.timeout)
	}
	return n, err
}

func (p *progressBody) closeBody() {
	p.once.Do(func() { p.closeErr = p.body.Close() })
}

func (p *progressBody) Close() error {
	p.mu.Lock()
	p.closed = true
	p.timer.Stop()
	p.mu.Unlock()
	p.closeBody()
	return p.closeErr
}

// NewProgressSSEDecoder keeps progress classification in the actual protocol
// parser. It neither stores nor exposes hidden reasoning/partial tool arguments.
func NewProgressSSEDecoder(ctx context.Context, body io.ReadCloser, limit int, timeout time.Duration, firstProgressDeadline ...time.Time) (*SSEDecoder, io.ReadCloser) {
	if timeout <= 0 {
		timeout = DefaultStreamInactivityTimeout
	}
	guarded := newProgressBody(ctx, body, timeout, firstProgressDeadline...)
	decoder := NewSSEDecoder(guarded, limit)
	decoder.progress = guarded.progress
	decoder.lastProgress = guarded.LastProgressAt
	return decoder, guarded
}

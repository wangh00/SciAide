package debugapi

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/apperr"
)

type LimitsCommand struct {
	SessionID        string    `json:"sessionId"`
	ExpectedRevision uint64    `json:"expectedRevision"`
	MaxRequests      int       `json:"maxRequests"`
	ExpiresAt        time.Time `json:"expiresAt"`
	Confirmed        bool      `json:"confirmed"`
}

type SessionState struct {
	ID                string    `json:"id"`
	Revision          uint64    `json:"revision"`
	ExpiresAt         time.Time `json:"expiresAt"`
	HardStopAt        time.Time `json:"hardStopAt"`
	SecondsRemaining  int64     `json:"secondsRemaining"`
	RequestsRemaining int       `json:"requestsRemaining"`
}

type LimitsState struct {
	Session           SessionState `json:"session"`
	ModelCalls        BudgetState  `json:"modelCalls"`
	DescriptorUpdated bool         `json:"descriptorUpdated"`
}

// A grant is bounded by BOTH civil time and elapsed time. Serialization or
// conversion to UTC must not discard its monotonic bound. Rolling the wall
// clock back cannot create time; rolling it forward cannot ignore the cutoff.
type sessionInstant struct{ wall, mono time.Time }
type sessionDeadline struct{ wall, mono time.Time }

// The grant lock NEVER spans disk IO or handler/worker draining. Cancellation
// remains prompt even if a budget/descriptor fsync is blocked indefinitely.
type sessionGrant struct {
	mu                    sync.Mutex
	expiresAt, hardStopAt sessionDeadline
	stopped               bool
	ctx                   context.Context
	cancel, onStop        context.CancelFunc
	clock                 func() sessionInstant
	notify                sync.Once
}

func (g *sessionGrant) signalStop() {
	g.notify.Do(func() {
		g.cancel()
		if g.onStop != nil {
			g.onStop()
		}
	})
}

func (g *sessionGrant) inspect() (sessionDeadline, sessionDeadline, bool) {
	g.mu.Lock()
	if g.ctx.Err() != nil || g.expiresAt.remaining(g.clock()) <= 0 {
		g.stopped = true
	}
	expires, hard, stopped := g.expiresAt, g.hardStopAt, g.stopped
	g.mu.Unlock()
	if stopped {
		g.signalStop()
	}
	return expires, hard, stopped
}

func (g *sessionGrant) expired() bool { _, _, stopped := g.inspect(); return stopped }

func (g *sessionGrant) stop() {
	g.mu.Lock()
	g.stopped = true
	g.mu.Unlock()
	g.signalStop()
}

func (g *sessionGrant) renew(expires sessionDeadline) bool {
	g.mu.Lock()
	now := g.clock()
	if g.ctx.Err() != nil || g.expiresAt.remaining(now) <= 0 || expires.remaining(now) <= 0 {
		g.stopped = true
	}
	if !g.stopped {
		g.expiresAt = expires
	}
	ok := !g.stopped
	g.mu.Unlock()
	if !ok {
		g.signalStop()
	}
	return ok
}

func sessionNow() sessionInstant {
	now := time.Now()
	return sessionInstant{wall: now.Round(0), mono: now}
}

func (d sessionDeadline) remaining(now sessionInstant) time.Duration {
	return min(d.wall.Sub(now.wall), d.mono.Sub(now.mono))
}

func after(now sessionInstant, duration time.Duration) sessionDeadline {
	return sessionDeadline{wall: now.wall.Add(duration), mono: now.mono.Add(duration)}
}

func earlier(a, b sessionDeadline) sessionDeadline {
	if b.wall.Before(a.wall) {
		a.wall = b.wall
	}
	if b.mono.Before(a.mono) {
		a.mono = b.mono
	}
	return a
}

func sessionWindow(parent context.Context, now sessionInstant, duration time.Duration, deadline time.Time) (sessionDeadline, sessionDeadline, error) {
	if err := parent.Err(); err != nil {
		return sessionDeadline{}, sessionDeadline{}, err
	}
	if duration <= 0 || duration > time.Hour {
		return sessionDeadline{}, sessionDeadline{}, fmt.Errorf("max-duration must be >0 and <=1h")
	}
	hardStop := after(now, time.Hour)
	if d, ok := parent.Deadline(); ok {
		hardStop = earlier(hardStop, sessionDeadline{wall: d.Round(0), mono: d})
	}
	expires := after(now, duration)
	if !deadline.IsZero() {
		if !deadline.After(now.wall) || deadline.After(expires.wall) {
			return sessionDeadline{}, sessionDeadline{}, fmt.Errorf("deadline must be in the future and within max-duration")
		}
		expires = after(now, deadline.Sub(now.wall))
	}
	expires = earlier(expires, hardStop)
	if expires.remaining(now) <= 0 {
		return sessionDeadline{}, sessionDeadline{}, fmt.Errorf("debug session already expired")
	}
	return expires, hardStop, nil
}

// Run BEFORE opening a workspace/Application: construction can legitimately
// recover persisted interrupted runs. Start validates again so initialization
// time cannot revive an expired grant.
func ValidateSessionWindow(parent context.Context, duration time.Duration, deadline time.Time) error {
	_, _, err := sessionWindow(parent, sessionNow(), duration, deadline)
	return err
}

func (s *Server) sessionExpired() bool {
	return s.grant.expired()
}

func (s *Server) expire() {
	expires, _, expired := s.grant.inspect()
	remaining := expires.remaining(s.workspace.budget.now())
	if !expired {
		// An old callback may already have started while an explicit renewal
		// reset its timer. The current grant, not the callback, owns the cutoff.
		s.timerMu.Lock()
		if s.ctx.Err() == nil && s.deadlineTimer != nil {
			// A renewal may have committed since inspect returned this copy.
			// A stale nonpositive duration only schedules a fresh observation;
			// only the grant's atomic expiry observation may stop the session.
			s.deadlineTimer.Reset(max(time.Duration(0), deadlineDelay(remaining)))
		}
		s.timerMu.Unlock()
		return
	}
	_ = s.Close()
}

// Civil-clock changes do not wake a Go duration timer. Recheck at most once a
// second as well as at every admission/Gate; shutdown still drains normally.
func deadlineDelay(remaining time.Duration) time.Duration { return min(remaining, time.Second) }

// Caller holds limitsMu then budget.mu; all combined snapshots use this order.
func (s *Server) limitsStateLocked() LimitsState {
	expires, hard, expired := s.grant.inspect()
	if expired {
		s.workspace.budget.state.Enabled = false
	}
	seconds := int64(math.Ceil(expires.remaining(s.workspace.budget.now()).Seconds()))
	if seconds < 0 || expired {
		seconds = 0
	}
	remaining := s.workspace.budget.state.Limit - s.workspace.budget.state.Used
	if remaining < 0 || !s.workspace.budget.state.Enabled {
		remaining = 0
	}
	return LimitsState{Session: SessionState{ID: s.sessionID, Revision: s.limitsRevision, ExpiresAt: expires.wall.UTC(), HardStopAt: hard.wall.UTC(), SecondsRemaining: seconds, RequestsRemaining: remaining}, ModelCalls: s.workspace.budget.state}
}

func (s *Server) limitsSnapshot() LimitsState {
	s.limitsMu.Lock()
	defer s.limitsMu.Unlock()
	s.workspace.budget.mu.Lock()
	defer s.workspace.budget.mu.Unlock()
	return s.limitsStateLocked()
}

func (s *Server) writeConnectionDescriptor(expires time.Time, budget BudgetState) error {
	data, _ := json.Marshal(map[string]any{"url": s.url, "token": s.token, "pid": os.Getpid(), "sessionId": s.sessionID, "expiresAt": expires.UTC(), "modelCalls": budget})
	return s.workspace.budget.saveAs(filepath.Join(s.workspace.Root, "debug-connection.json"), data)
}

func (s *Server) updateLimits(cmd LimitsCommand) (LimitsState, error) {
	// This is NOT permission to call a model, resume a task or accept a
	// scientific result. The grant, not an IO lock, arbitrates commit vs stop.
	s.limitsMu.Lock()
	defer s.limitsMu.Unlock()
	b := s.workspace.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if b.expired() {
		return LimitsState{}, &apperr.Error{Code: "DEBUG_SESSION_EXPIRED", UserMessage: "调试实例已停止或到期，不能以调整限额复活；请使用明确的新授权窗口启动。"}
	}
	if !cmd.Confirmed || cmd.ExpectedRevision == 0 || cmd.SessionID == "" {
		return LimitsState{}, invalidRequest("limits update requires confirmed=true, sessionId and the current expectedRevision")
	}
	if cmd.SessionID != s.sessionID || cmd.ExpectedRevision != s.limitsRevision {
		return LimitsState{}, &apperr.Error{Code: "DEBUG_LIMITS_CONFLICT", UserMessage: "调试实例或授权已变化，本次旧限额请求未生效；请先读取当前 status，不自动重发。"}
	}
	expires := after(now, cmd.ExpiresAt.Sub(now.wall))
	_, hard, _ := s.grant.inspect()
	if cmd.MaxRequests < 1 || cmd.MaxRequests > 1000 || cmd.MaxRequests < b.state.Used || expires.remaining(now) <= 0 || expires.wall.After(hard.wall) || expires.mono.After(hard.mono) {
		return LimitsState{}, invalidRequest("limits must preserve Used, use 1..1000 total requests and a future cutoff within this session's wall/elapsed hard stop")
	}
	next := b.state
	next.Limit = cmd.MaxRequests // Never enable, refund, restart or bypass Gate.
	data, _ := json.Marshal(next)
	if err := b.save(data); err != nil {
		return LimitsState{}, &apperr.Error{Code: "DEBUG_BUDGET_SAVE_FAILED", UserMessage: "无法保存新调用上限；本次时间和限额均未修改。", Cause: err}
	}
	b.state = next
	s.limitsRevision++ // The durable cap was committed, including on late expiry.
	// fsync/rename may cross the OLD cutoff while holding these locks. Never
	// revive it or pretend the committed cap was not saved. Disable IO and close.
	if !s.grant.renew(expires) {
		b.state.Enabled = false
		return s.limitsStateLocked(), &apperr.Error{Code: "DEBUG_LIMITS_EXPIRED_DURING_SAVE", UserMessage: "总调用上限已保存，但保存期间授权时间窗结束；未延长时间、未恢复任务，模型外发已禁止。"}
	}
	s.timerMu.Lock()
	if s.deadlineTimer != nil {
		s.deadlineTimer.Reset(deadlineDelay(expires.remaining(b.now())))
	}
	s.timerMu.Unlock()
	// The live status + durable budget are authoritative. Descriptor contents
	// are derived connection metadata: a refresh failure must not pretend that
	// an already committed grant failed, invite a blind POST retry, or roll back
	// Used. The address/token do not change; status remains readable.
	updated := s.writeConnectionDescriptor(expires.wall, b.state) == nil
	result := s.limitsStateLocked() // Metadata IO can also cross expiry/cancellation.
	result.DescriptorUpdated = updated
	return result, nil
}

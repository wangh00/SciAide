package debugapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/apperr"
)

type grantClock struct {
	mu                  sync.Mutex
	base                time.Time
	elapsed, wallOffset time.Duration
}

func (c *grantClock) now() sessionInstant {
	c.mu.Lock()
	defer c.mu.Unlock()
	return sessionInstant{wall: c.base.Add(c.elapsed + c.wallOffset).Round(0), mono: c.base.Add(c.elapsed)}
}

func (c *grantClock) advance(elapsed, wallOffset time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.elapsed += elapsed
	c.wallOffset += wallOffset
}

func limitsFixture(t *testing.T, duration time.Duration) (*Server, *grantClock, LimitsCommand) {
	t.Helper()
	root := t.TempDir()
	b, err := openBudget(root, true, 10)
	if err != nil {
		t.Fatal(err)
	}
	b.state.Used = 3
	data, _ := json.Marshal(b.state)
	if err := b.save(data); err != nil {
		t.Fatal(err)
	}
	c := &grantClock{base: time.Now()}
	b.clock = c.now
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	g := &sessionGrant{ctx: ctx, cancel: cancel, clock: c.now, expiresAt: after(c.now(), duration), hardStopAt: after(c.now(), time.Hour)}
	b.grant = g
	s := &Server{workspace: &Workspace{Root: root, budget: b}, ctx: ctx, cancel: cancel, sessionID: "current-session", limitsRevision: 1, grant: g}
	return s, c, LimitsCommand{SessionID: s.sessionID, ExpectedRevision: 1, MaxRequests: 20, ExpiresAt: c.now().wall.Add(10 * time.Minute), Confirmed: true}
}

func requireDebugCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *apperr.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("want %s; got %v", code, err)
	}
}

func TestLimitsCommitKeepsUseAndRejectsStaleOrOtherSession(t *testing.T) {
	s, _, cmd := limitsFixture(t, time.Minute)
	v, err := s.updateLimits(cmd)
	if err != nil || !v.DescriptorUpdated || v.Session.ID != s.sessionID || v.Session.Revision != 2 || v.ModelCalls.Used != 3 || v.ModelCalls.Limit != 20 || !v.ModelCalls.Enabled {
		t.Fatal("grant reset use or failed to commit", v, err)
	}
	before, _ := os.ReadFile(s.workspace.budget.path)
	for _, other := range []LimitsCommand{cmd, {SessionID: "prior-process-session", ExpectedRevision: 2, MaxRequests: 30, ExpiresAt: cmd.ExpiresAt, Confirmed: true}} {
		_, err = s.updateLimits(other)
		requireDebugCode(t, err, "DEBUG_LIMITS_CONFLICT")
	}
	after, _ := os.ReadFile(s.workspace.budget.path)
	if string(before) != string(after) || s.limitsRevision != 2 {
		t.Fatal("stale authorization mutated grant")
	}
}

func TestLimitsInvalidGrantCannotMutateBudget(t *testing.T) {
	for _, kind := range []string{"unconfirmed", "missing-session", "zero-revision", "refund", "zero-cap", "over-cap", "past", "beyond-hard-stop"} {
		t.Run(kind, func(t *testing.T) {
			s, c, cmd := limitsFixture(t, time.Minute)
			switch kind {
			case "unconfirmed":
				cmd.Confirmed = false
			case "missing-session":
				cmd.SessionID = ""
			case "zero-revision":
				cmd.ExpectedRevision = 0
			case "refund":
				cmd.MaxRequests = 2
			case "zero-cap":
				cmd.MaxRequests = 0
			case "over-cap":
				cmd.MaxRequests = 1001
			case "past":
				cmd.ExpiresAt = c.now().wall
			case "beyond-hard-stop":
				cmd.ExpiresAt = s.grant.hardStopAt.wall.Add(time.Nanosecond)
			}
			before, _ := os.ReadFile(s.workspace.budget.path)
			if _, err := s.updateLimits(cmd); err == nil {
				t.Fatal("invalid authorization accepted")
			}
			after, _ := os.ReadFile(s.workspace.budget.path)
			if string(before) != string(after) || s.limitsRevision != 1 || s.workspace.budget.state.Limit != 10 {
				t.Fatal("invalid grant wrote state")
			}
		})
	}
}

func TestLimitsSaveFailureIsAtomicAndDescriptorFailureIsNotRollback(t *testing.T) {
	s, _, cmd := limitsFixture(t, time.Minute)
	b := s.workspace.budget
	b.writeJSON = func(string, []byte) error { return errors.New("fixture save failed") }
	_, err := s.updateLimits(cmd)
	requireDebugCode(t, err, "DEBUG_BUDGET_SAVE_FAILED")
	if s.limitsRevision != 1 || b.state.Limit != 10 || s.grant.expiresAt.remaining(s.workspace.budget.now()) != time.Minute {
		t.Fatal("failed write changed grant")
	}
	b.writeJSON = nil
	if err := os.Mkdir(filepath.Join(s.workspace.Root, "debug-connection.json"), 0700); err != nil {
		t.Fatal(err)
	}
	v, err := s.updateLimits(cmd)
	if err != nil || v.DescriptorUpdated || v.ModelCalls.Limit != 20 || v.ModelCalls.Used != 3 || v.Session.Revision != 2 {
		t.Fatal("derived metadata failure hid a committed grant", v, err)
	}
}

func TestLimitsSaveCrossingOldDeadlineOrCancellationCannotRevive(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "expired", true: "cancelled"}[cancel], func(t *testing.T) {
			s, c, cmd := limitsFixture(t, time.Minute)
			old := s.grant.expiresAt
			b := s.workspace.budget
			b.writeJSON = func(path string, data []byte) error {
				if err := atomicJSON(path, data); err != nil {
					return err
				}
				if cancel {
					s.cancel()
				} else {
					c.advance(2*time.Minute, 0)
				}
				return nil
			}
			v, err := s.updateLimits(cmd)
			requireDebugCode(t, err, "DEBUG_LIMITS_EXPIRED_DURING_SAVE")
			if v.ModelCalls.Enabled || v.ModelCalls.Limit != 20 || v.ModelCalls.Used != 3 || v.Session.Revision != 2 || v.Session.SecondsRemaining != 0 || v.Session.RequestsRemaining != 0 || s.grant.expiresAt != old || s.ctx.Err() == nil {
				t.Fatal("late write revived grant or concealed commit", v)
			}
			raw, _ := os.ReadFile(b.path)
			var durable BudgetState
			if json.Unmarshal(raw, &durable) != nil || durable.Limit != 20 || durable.Used != 3 {
				t.Fatal("late commit was refunded or hidden")
			}
			if err := b.take(context.Background()); err == nil || b.state.Used != 3 {
				t.Fatal("late grant admitted model IO")
			}
		})
	}
}

func TestBudgetSaveCrossingDeadlineOrCancellationKeepsCountButRejectsIO(t *testing.T) {
	for _, kind := range []string{"deadline", "request-cancel", "session-cancel"} {
		t.Run(kind, func(t *testing.T) {
			s, c, _ := limitsFixture(t, time.Minute)
			b := s.workspace.budget
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			b.writeJSON = func(path string, data []byte) error {
				if err := atomicJSON(path, data); err != nil {
					return err
				}
				switch kind {
				case "deadline":
					c.advance(time.Minute, 0)
				case "request-cancel":
					cancel()
				case "session-cancel":
					s.cancel()
				}
				return nil
			}
			if err := b.take(ctx); err == nil {
				t.Fatal("Gate admitted IO after blocked persistence crossed authorization")
			}
			var saved BudgetState
			data, _ := os.ReadFile(b.path)
			if json.Unmarshal(data, &saved) != nil || saved.Used != 4 || b.state.Used != 4 {
				t.Fatal("conservative durable count was refunded")
			}
		})
	}
}

func TestClockRollbackCannotExtendElapsedHardStop(t *testing.T) {
	s, c, cmd := limitsFixture(t, time.Hour)
	c.advance(30*time.Minute, -30*time.Minute)
	cmd.ExpiresAt = c.now().wall.Add(time.Hour)
	if _, err := s.updateLimits(cmd); err == nil {
		t.Fatal("rollback made another elapsed hour available")
	}
	cmd.ExpiresAt = c.now().wall.Add(29 * time.Minute)
	if _, err := s.updateLimits(cmd); err != nil {
		t.Fatal("bounded grant should remain possible", err)
	}
	c.advance(30*time.Minute, 0)
	if !s.sessionExpired() || s.limitsSnapshot().Session.SecondsRemaining != 0 {
		t.Fatal("elapsed deadline was lost during wall clock rollback")
	}
	if err := s.workspace.budget.take(context.Background()); err == nil {
		t.Fatal("rollback bypassed Gate")
	}
}

func TestOldTimerCallbackDoesNotCancelConfirmedRenewal(t *testing.T) {
	s, c, cmd := limitsFixture(t, time.Minute)
	if _, err := s.updateLimits(cmd); err != nil {
		t.Fatal(err)
	}
	c.advance(2*time.Minute, 0)
	s.expire() // A callback queued for the previous one-minute cutoff.
	if s.ctx.Err() != nil || s.sessionExpired() {
		t.Fatal("stale callback cancelled the new confirmed window")
	}
}

func TestTimerOldSnapshotAcrossRenewalCannotStopCurrentGrant(t *testing.T) {
	s, c, cmd := limitsFixture(t, time.Minute)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })
	s.workspace.budget.clock = func() sessionInstant {
		if once.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
		return c.now()
	}
	go func() { s.expire(); close(done) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("callback did not pause after reading old grant")
	}
	if _, err := s.updateLimits(cmd); err != nil {
		t.Fatal(err)
	}
	c.advance(2*time.Minute, 0)
	unblock.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("old callback did not return")
	}
	if s.ctx.Err() != nil || s.sessionExpired() {
		t.Fatal("callback's stale deadline stopped confirmed renewed grant")
	}
}

func TestClockForwardExpiresWithoutWaitingForTimer(t *testing.T) {
	s, c, cmd := limitsFixture(t, time.Minute)
	c.advance(0, 2*time.Minute)
	if !s.sessionExpired() {
		t.Fatal("wall deadline ignored")
	}
	_, err := s.updateLimits(cmd)
	requireDebugCode(t, err, "DEBUG_SESSION_EXPIRED")
	requireDebugCode(t, s.workspace.budget.take(context.Background()), "DEBUG_SESSION_EXPIRED")
}

func TestDescriptorWriteCannotReturnStaleLiveStateAfterCancellation(t *testing.T) {
	s, _, cmd := limitsFixture(t, time.Minute)
	s.workspace.budget.writeJSON = func(path string, data []byte) error {
		if err := atomicJSON(path, data); err != nil {
			return err
		}
		if filepath.Base(path) == "debug-connection.json" {
			s.cancel()
		}
		return nil
	}
	v, err := s.updateLimits(cmd)
	if err != nil || v.ModelCalls.Limit != 20 || v.ModelCalls.Enabled || v.Session.SecondsRemaining != 0 || v.Session.RequestsRemaining != 0 || !v.DescriptorUpdated {
		t.Fatal("committed grant returned stale active status", v, err)
	}
}

func TestDeadlineObserverCancelsActiveWorkAndSurvivesRollback(t *testing.T) {
	s, c, _ := limitsFixture(t, time.Minute)
	s.http = &http.Server{}
	s.done = make(chan struct{})
	s.options.OnStop = s.cancel
	stopped := make(chan struct{})
	go func() { <-s.ctx.Done(); close(stopped) }()
	c.advance(0, 2*time.Minute)
	s.expire()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("observed wall expiry did not cancel active work")
	}
	c.advance(0, -2*time.Minute)
	if !s.sessionExpired() || s.limitsSnapshot().Session.SecondsRemaining != 0 {
		t.Fatal("closed expired session revived after rollback")
	}
	if deadlineDelay(time.Hour) > time.Second {
		t.Fatal("wall clock check could wait an entire old duration")
	}
}

func TestStopCancelsActiveWorkBeforeBlockedPersistenceReturns(t *testing.T) {
	for _, trigger := range []string{"expiry", "close"} {
		for _, phase := range []string{"gate-budget", "limits-budget", "limits-descriptor"} {
			t.Run(trigger+"/"+phase, func(t *testing.T) {
				s, c, cmd := limitsFixture(t, time.Minute)
				s.http, s.done = &http.Server{}, make(chan struct{})
				appCtx, stopApp := context.WithCancel(context.Background())
				defer stopApp()
				s.grant.onStop = stopApp // Independent sibling, like CLI appCtx.
				entered, release := make(chan struct{}), make(chan struct{})
				var unblock sync.Once
				defer unblock.Do(func() { close(release) })
				s.workspace.budget.writeJSON = func(path string, data []byte) error {
					isDescriptor := filepath.Base(path) == "debug-connection.json"
					if isDescriptor == (phase == "limits-descriptor") {
						close(entered)
						<-release
					}
					return atomicJSON(path, data)
				}
				written := make(chan error, 1)
				go func() {
					if phase == "gate-budget" {
						written <- s.workspace.budget.take(context.Background())
					} else {
						_, err := s.updateLimits(cmd)
						written <- err
					}
				}()
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("writer did not block")
				}
				closed := make(chan struct{})
				go func() {
					if trigger == "expiry" {
						c.advance(11*time.Minute, 0)
						s.expire()
					} else {
						_ = s.Close()
					}
					close(closed)
				}()
				select {
				case <-appCtx.Done():
				case <-time.After(time.Second):
					t.Fatal("active model sibling context was not cancelled while fsync was blocked")
				}
				if s.ctx.Err() == nil {
					t.Fatal("HTTP context was not cancelled")
				}
				select {
				case <-s.done:
					t.Fatal("root was released before persistence drained")
				default:
				}
				unblock.Do(func() { close(release) })
				select {
				case err := <-written:
					if phase != "limits-descriptor" && err == nil {
						t.Fatal("late persistence admitted IO or revived grant")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("writer did not drain")
				}
				select {
				case <-closed:
				case <-time.After(2 * time.Second):
					t.Fatal("Close did not drain")
				}
			})
		}
	}
}

func TestObservedExpiryCannotBeReversedByWallClockRollback(t *testing.T) {
	for _, observer := range []string{"gate", "status", "http-admission", "update"} {
		t.Run(observer, func(t *testing.T) {
			s, c, cmd := limitsFixture(t, time.Minute)
			b := s.workspace.budget
			c.advance(0, 2*time.Minute)
			switch observer {
			case "gate":
				requireDebugCode(t, b.take(context.Background()), "DEBUG_SESSION_EXPIRED")
			case "status":
				if s.limitsSnapshot().ModelCalls.Enabled {
					t.Fatal("status missed expiry")
				}
			case "http-admission":
				if !s.sessionExpired() {
					t.Fatal("HTTP admission missed expiry")
				}
			case "update":
				_, err := s.updateLimits(cmd)
				requireDebugCode(t, err, "DEBUG_SESSION_EXPIRED")
			}
			c.advance(0, -2*time.Minute)
			requireDebugCode(t, b.take(context.Background()), "DEBUG_SESSION_EXPIRED")
			_, err := s.updateLimits(cmd)
			requireDebugCode(t, err, "DEBUG_SESSION_EXPIRED")
			if b.state.Used != 3 || !s.sessionExpired() || s.ctx.Err() == nil {
				t.Fatal("rollback revived observed expired grant")
			}
		})
	}
}

func TestSessionWindowValidationAndParentBound(t *testing.T) {
	now := sessionNow()
	for _, duration := range []time.Duration{0, -1, time.Hour + 1} {
		if _, _, err := sessionWindow(context.Background(), now, duration, time.Time{}); err == nil {
			t.Fatal("invalid duration accepted")
		}
	}
	for _, deadline := range []time.Time{now.wall, now.wall.Add(-1), now.wall.Add(time.Hour + 1)} {
		if _, _, err := sessionWindow(context.Background(), now, time.Hour, deadline); err == nil {
			t.Fatal("invalid absolute deadline accepted")
		}
	}
	ctx, cancel := context.WithDeadline(context.Background(), now.mono.Add(time.Minute))
	defer cancel()
	expires, hard, err := sessionWindow(ctx, now, time.Hour, time.Time{})
	if err != nil || expires.remaining(now) != time.Minute || hard.remaining(now) != time.Minute {
		t.Fatal("parent bound was expanded", expires, hard, err)
	}
	cancel()
	if ValidateSessionWindow(ctx, time.Hour, time.Time{}) == nil {
		t.Fatal("cancelled parent accepted")
	}
}

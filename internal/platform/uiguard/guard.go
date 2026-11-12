// Package uiguard decides only whether an observed native renderer failure may
// trigger a bounded UI reload. A missing heartbeat alone is never sufficient.
package uiguard

import (
	"sync"
	"time"
)

const (
	HeartbeatGrace   = 25 * time.Second
	RecoveryCooldown = 2 * time.Minute
	FailureLifetime  = 5 * time.Minute
)

type Guard struct {
	mu           sync.Mutex
	lastPulse    time.Time
	hidden       bool
	failure      string
	failedAt     time.Time
	lastRecovery time.Time
	recoveries   int
	recovering   string
}

// Returns an identity only for the first healthy page pulse after a recovery
// request. This acknowledges page recovery, not scientific task completion.
func (g *Guard) Heartbeat(now time.Time, hidden bool) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	recovered := g.recovering
	g.recovering = ""
	g.lastPulse, g.hidden = now, hidden
	// A healthy page after an exit means WebView recovered itself.
	g.failure, g.failedAt = "", time.Time{}
	return recovered
}

func (g *Guard) RendererExited(identity string, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if identity != "" && !g.lastPulse.IsZero() && now.Sub(g.lastPulse) < FailureLifetime {
		g.failure, g.failedAt = identity, now
	}
}

func (g *Guard) Suspend() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failure, g.failedAt = "", time.Time{}
	g.lastPulse = time.Time{}
}

func (g *Guard) ShouldRecover(now time.Time, foreground bool) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failure == "" || g.lastPulse.IsZero() || g.hidden || !foreground || now.Sub(g.lastPulse) < HeartbeatGrace || now.Sub(g.failedAt) < HeartbeatGrace {
		return "", false
	}
	if now.Sub(g.failedAt) > FailureLifetime || g.recoveries >= 2 || !g.lastRecovery.IsZero() && now.Sub(g.lastRecovery) < RecoveryCooldown {
		g.failure = ""
		return "", false
	}
	identity := g.failure
	g.failure = ""
	g.recoveries++
	g.lastRecovery = now
	g.recovering = identity
	return identity, true
}

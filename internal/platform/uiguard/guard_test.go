package uiguard

import (
	"testing"
	"time"
)

func TestRecoveryRequiresNativeFailureAndMissingHealthyPage(t *testing.T) {
	now := time.Unix(1000, 0)
	var g Guard
	g.Heartbeat(now, false)
	if _, ok := g.ShouldRecover(now.Add(time.Hour), true); ok {
		t.Fatal("model wait/JS delay alone caused reload")
	}
	g.RendererExited("renderer-1", now.Add(time.Second))
	if _, ok := g.ShouldRecover(now.Add(20*time.Second), true); ok {
		t.Fatal("no recovery grace")
	}
	if _, ok := g.ShouldRecover(now.Add(30*time.Second), false); ok {
		t.Fatal("minimised window reloaded")
	}
	if id, ok := g.ShouldRecover(now.Add(30*time.Second), true); !ok || id != "renderer-1" {
		t.Fatal("native failure not recovered")
	}
	if _, ok := g.ShouldRecover(now.Add(time.Minute), true); ok {
		t.Fatal("same failure replayed")
	}
	if id := g.Heartbeat(now.Add(time.Minute), false); id != "renderer-1" {
		t.Fatal("recovered page not acknowledged", id)
	}
	if id := g.Heartbeat(now.Add(time.Minute), false); id != "" {
		t.Fatal("duplicate recovery acknowledgement", id)
	}
	g.RendererExited("self-recovered", now.Add(time.Minute+time.Second))
	g.Heartbeat(now.Add(time.Minute+2*time.Second), false)
	if _, ok := g.ShouldRecover(now.Add(2*time.Minute), true); ok {
		t.Fatal("healthy page reloaded")
	}
}

func TestRecoveryIsBoundedAndIgnoresHiddenSuspendAndOldFailures(t *testing.T) {
	now := time.Unix(1000, 0)
	var hidden Guard
	hidden.Heartbeat(now, true)
	hidden.RendererExited("hidden", now)
	if _, ok := hidden.ShouldRecover(now.Add(time.Minute), true); ok {
		t.Fatal("hidden page reloaded")
	}
	hidden.Suspend()
	if _, ok := hidden.ShouldRecover(now.Add(time.Minute), true); ok {
		t.Fatal("sleep caused recovery")
	}
	var g Guard
	for i := 0; i < 3; i++ {
		at := now.Add(time.Duration(i) * 3 * time.Minute)
		g.Heartbeat(at, false)
		g.RendererExited("failure", at)
		_, ok := g.ShouldRecover(at.Add(30*time.Second), true)
		if ok != (i < 2) {
			t.Fatal("unbounded recovery", i, ok)
		}
	}
	var stale Guard
	stale.Heartbeat(now, false)
	stale.RendererExited("stale", now)
	if _, ok := stale.ShouldRecover(now.Add(6*time.Minute), true); ok {
		t.Fatal("stale failure used")
	}
}

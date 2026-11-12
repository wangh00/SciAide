package wails

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/platform/uiguard"
)

type SystemFacade struct {
	version   string
	guard     uiguard.Guard
	mu        sync.Mutex
	logger    *slog.Logger
	lastError time.Time
}

type SystemInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Phase   string `json:"phase"`
}

func NewSystemFacade(version string) *SystemFacade {
	return &SystemFacade{version: version}
}

func NewSystemFacadeWithLogger(version string, logger *slog.Logger) *SystemFacade {
	return &SystemFacade{version: version, logger: logger}
}

func (f *SystemFacade) GetSystemInfo() SystemInfo {
	return SystemInfo{Name: "SciAide", Version: f.version, Phase: "P6.0"}
}

func (f *SystemFacade) UIHeartbeat(hidden bool) {
	if identity := f.guard.Heartbeat(time.Now(), hidden); identity != "" && f.logger != nil {
		f.logger.Info("UI page responded after native recovery", "renderer", identity)
	}
}

// Native host hooks are package functions, not bound facade methods. The page
// can report health but cannot manufacture native failures or request recovery.
func ObserveRendererExit(f *SystemFacade, identity string) {
	f.guard.RendererExited(identity, time.Now())
}
func SuspendUIRecovery(f *SystemFacade) { f.guard.Suspend() }
func RecoverUI(f *SystemFacade, foreground bool) bool {
	identity, recover := f.guard.ShouldRecover(time.Now(), foreground)
	if recover {
		f.mu.Lock()
		logger := f.logger
		f.mu.Unlock()
		if logger != nil {
			logger.Warn("native renderer exited; restoring UI only", "renderer", identity)
		}
	}
	return recover
}

func (f *SystemFacade) ReportUIError(scope, kind, componentStack string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.logger == nil || time.Since(f.lastError) < time.Second {
		return
	}
	f.lastError = time.Now()
	if scope != "application" && scope != "conversation" && scope != "research-workspace" {
		scope = "unknown"
	}
	if kind != "TypeError" && kind != "RangeError" && kind != "ReferenceError" {
		kind = "Error"
	}
	if len(componentStack) > 6000 {
		componentStack = componentStack[:6000]
	}
	f.logger.Error("UI render failed", "scope", scope, "kind", kind, "components", strings.ToValidUTF8(componentStack, ""))
}

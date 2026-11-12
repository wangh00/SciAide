// Package desktopdiag records desktop lifecycle independently of the page,
// application database and application logger. It never resumes research work.
package desktopdiag

import (
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/observability"
)

type sessionState struct {
	PID       int       `json:"pid"`
	Version   string    `json:"version"`
	StartedAt time.Time `json:"startedAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Phase     string    `json:"phase"`
}

type Session struct {
	mu        sync.Mutex
	logger    *slog.Logger
	writer    *observability.RotatingWriter
	state     sessionState
	statePath string
	logDir    string
	faulted   bool
	closed    bool
	restoreGo func()
}

func Start(logDir, version string) (*Session, error) {
	writer, err := observability.NewRotatingWriter(filepath.Join(logDir, "desktop.jsonl"), 2*1024*1024, 2)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	s := &Session{writer: writer, logDir: logDir, statePath: filepath.Join(logDir, "desktop-session.json"),
		logger: slog.New(slog.NewJSONHandler(writer, nil)),
		state:  sessionState{PID: os.Getpid(), Version: version, StartedAt: now, UpdatedAt: now, Phase: "starting"}}
	// A marker is evidence of an incomplete lifecycle, not proof of a crash,
	// OOM, external termination, or the exact time/cause of process death.
	if data, err := os.ReadFile(s.statePath); err == nil {
		var previous sessionState
		if json.Unmarshal(data, &previous) == nil && previous.Phase != "clean_exit" {
			s.logger.Warn("previous desktop session did not record a clean exit", "previousPid", previous.PID,
				"lastPhase", previous.Phase, "lastObservedAt", previous.UpdatedAt)
		}
	}
	s.recordLocked("desktop session started", "starting")
	exe, _ := os.Executable()
	s.logger.Info("desktop identity", "pid", s.state.PID, "version", version, "executable", exe, "arch", runtime.GOARCH)
	return s, nil
}

func (s *Session) Phase(phase string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.recordLocked("desktop lifecycle", phase)
	}
}

func (s *Session) CloseRequested() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.recordLocked("desktop close requested", "close_requested")
		// Distinguish an IPC Quit from a native window close without recording
		// the IPC payload or adding a user-facing confirmation dialog.
		s.logger.Info("desktop close call site", "stack", string(debug.Stack()))
	}
}

func (s *Session) recordLocked(message, phase string) {
	s.state.Phase, s.state.UpdatedAt = phase, time.Now()
	s.logger.Info(message, "pid", s.state.PID, "phase", phase)
	data, err := json.Marshal(s.state)
	if err == nil {
		// Same-directory publication; a torn write must not replace the last
		// complete marker with truncated JSON. No research data is written here.
		tmp := s.statePath + ".tmp"
		if err = os.WriteFile(tmp, data, 0o600); err == nil {
			err = os.Rename(tmp, s.statePath)
		}
	}
	if err != nil {
		s.logger.Error("desktop lifecycle marker could not be saved", "operation", "write_marker")
	}
}

func (s *Session) Error(scope string, err error) {
	if s == nil || err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.faulted = true
		s.logger.Error("desktop failure", "scope", scope, "detail", safeMessage(err.Error()))
	}
}

// AttachGoDiagnostics is called only by the primary desktop process, not by
// bootstrap, binding generation or headless library tests. Unhandled panics are
// still fatal; no blanket recover converts them into fake successful work.
func (s *Session) AttachGoDiagnostics() error {
	if s == nil {
		return nil
	}
	path := filepath.Join(s.logDir, "go-crash.log")
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		previous := path + ".previous"
		if err := os.Remove(previous); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(path, previous); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	err = debug.SetCrashOutput(file, debug.CrashOptions{})
	_ = file.Close() // Go owns a duplicated handle until SetCrashOutput(nil).
	if err != nil {
		return err
	}
	previousOutput := log.Writer()
	log.SetOutput(observability.FileFirstWriter(goLogWriter{s}, previousOutput))
	s.restoreGo = func() { log.SetOutput(previousOutput); _ = debug.SetCrashOutput(nil, debug.CrashOptions{}) }
	return nil
}

type goLogWriter struct{ session *Session }

func (w goLogWriter) Write(data []byte) (int, error) {
	s := w.session
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		// The standard logger also carries nonfatal framework warnings. A log
		// line alone is not proof of an abnormal exit; log.Fatal leaves the
		// lifecycle unfinished because it never returns to Finish.
		s.logger.Warn("Go runtime log", "detail", safeMessage(string(data)), "stack", string(debug.Stack()))
	}
	return len(data), nil
}

func (s *Session) Finish(clean bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	phase := "exit_with_error"
	if clean && !s.faulted {
		phase = "clean_exit"
	}
	s.recordLocked("desktop session ended", phase)
	s.closed = true
	restoreGo := s.restoreGo
	s.mu.Unlock()
	// log.Writer can concurrently be in goLogWriter.Write. Do not invert the
	// global log mutex and session mutex while releasing the diagnostics.
	if restoreGo != nil {
		restoreGo()
	}
	_ = s.writer.Close()
}

// Runtime logs can contain IPC bodies, model settings and user material. Keep
// known native failure messages, but do not persist arbitrary dispatcher or
// page payloads. Raw Go panic output is separate local diagnostic data.
func safeMessage(message string) string {
	message = strings.TrimSpace(message)
	if strings.Contains(message, "process message error:") {
		return "Wails IPC dispatch failed; request payload omitted"
	}
	for _, prefix := range []string{"WebVie2wProcess failed with kind ", "GetProcessFailedKind:", "Call to GetKeyboardState failed", "Invoke: PostMessage failed"} {
		if strings.HasPrefix(message, prefix) {
			return message[:min(len(message), 512)]
		}
	}
	// Error types remain recognizable (e.g. unsupported value: NaN), while
	// long JSON, URLs/headers and user content are excluded from runtime logs.
	for _, prefix := range []string{"json: unsupported value: NaN", "json: unsupported value: +Inf", "json: unsupported value: -Inf"} {
		if strings.HasPrefix(message, prefix) {
			return prefix
		}
	}
	return "desktop runtime error (payload omitted)"
}

type RuntimeLogger struct{ Session *Session }

func (l RuntimeLogger) Print(message string)   { l.write(slog.LevelInfo, message, false) }
func (l RuntimeLogger) Trace(message string)   {} // No trace-level IPC/model bodies.
func (l RuntimeLogger) Debug(message string)   {}
func (l RuntimeLogger) Info(message string)    { l.write(slog.LevelInfo, message, false) }
func (l RuntimeLogger) Warning(message string) { l.write(slog.LevelWarn, message, false) }
func (l RuntimeLogger) Error(message string)   { l.write(slog.LevelError, message, false) }
func (l RuntimeLogger) Fatal(message string)   { l.write(slog.LevelError, message, true) }

func (l RuntimeLogger) write(level slog.Level, message string, fatal bool) {
	s := l.Session
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.logger.Log(nil, level, "desktop runtime", "pid", s.state.PID, "fatal", fatal, "detail", safeMessage(message))
	if fatal {
		s.faulted = true
		s.recordLocked("desktop fatal exit requested", "runtime_fatal")
		// The framework calls os.Exit immediately after Fatal returns. Keep its
		// call site here because runtime panic hooks do not cover os.Exit.
		s.logger.Error("desktop fatal call site", "stack", string(debug.Stack()))
	}
}

var _ io.Writer = goLogWriter{}

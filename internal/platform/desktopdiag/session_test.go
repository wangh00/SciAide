package desktopdiag

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func readSession(t *testing.T, root string) sessionState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "desktop-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s sessionState
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSessionRecordsCleanAndUnfinishedExits(t *testing.T) {
	root := t.TempDir()
	s, err := Start(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	s.Phase("running")
	s.Phase("close_requested")
	s.Phase("shutdown_completed")
	s.Finish(true)
	s.Finish(false)
	if got := readSession(t, root); got.Phase != "clean_exit" || got.PID != os.Getpid() {
		t.Fatal(got)
	}
	// Use a separate directory to model an externally stopped session, rather
	// than changing a running session or a real application profile.
	crashedRoot := t.TempDir()
	data, _ := json.Marshal(sessionState{PID: 42, Phase: "running"})
	if err := os.WriteFile(filepath.Join(crashedRoot, "desktop-session.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	next, err := Start(crashedRoot, "test")
	if err != nil {
		t.Fatal(err)
	}
	next.Finish(true)
	log, _ := os.ReadFile(filepath.Join(crashedRoot, "desktop.jsonl"))
	if !strings.Contains(string(log), "previous desktop session did not record a clean exit") || strings.Contains(string(log), "out of memory") {
		t.Fatal("unfinished session guessed a cause or was lost", string(log))
	}
}

func TestRuntimeErrorsExcludeIPCAndSecretsButKeepNativeFailureKind(t *testing.T) {
	root := t.TempDir()
	s, err := Start(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	l := RuntimeLogger{s}
	l.Error(`process message error: C{"apiKey":"private-secret","text":"private-paper"} -> failure`)
	l.Error("WebVie2wProcess failed with kind 0")
	l.Fatal("json: unsupported value: NaN")
	l.Trace("private-secret")
	s.Finish(true)
	data, _ := os.ReadFile(filepath.Join(root, "desktop.jsonl"))
	for _, forbidden := range []string{"private-secret", "private-paper", "apiKey"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatal("runtime payload leaked", forbidden)
		}
	}
	for _, required := range []string{"WebVie2wProcess failed with kind 0", "IPC dispatch failed", "desktop fatal call site", "unsupported value: NaN"} {
		if !strings.Contains(string(data), required) {
			t.Fatal("diagnostic lost", required)
		}
	}
	if readSession(t, root).Phase != "exit_with_error" {
		t.Fatal("fatal error was labeled a clean exit")
	}
}

func TestNonfatalStandardLogDoesNotBecomeUncleanExit(t *testing.T) {
	root := t.TempDir()
	s, err := Start(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AttachGoDiagnostics(); err != nil {
		t.Fatal(err)
	}
	defer s.Finish(true)
	log.Print("controlled nonfatal warning: private-paper")
	s.Finish(true)
	if readSession(t, root).Phase != "clean_exit" {
		t.Fatal("a nonfatal log line was mislabeled as an abnormal exit")
	}
	data, err := os.ReadFile(filepath.Join(root, "desktop.jsonl"))
	if err != nil || !strings.Contains(string(data), "Go runtime log") || strings.Contains(string(data), "private-paper") {
		t.Fatal("standard log was lost or leaked its payload", err)
	}
}

// Run in a separate Go process because panic and os.Exit must stay fatal and
// SetCrashOutput is process-global. No application/task/model is started.
func TestUnhandledPanicAndRuntimeExitArePersisted(t *testing.T) {
	if mode := os.Getenv("SCIAIDE_DIAG_CHILD"); mode != "" {
		s, err := Start(os.Getenv("SCIAIDE_DIAG_ROOT"), "test-child")
		if err != nil {
			panic(err)
		}
		if err := s.AttachGoDiagnostics(); err != nil {
			panic(err)
		}
		s.Phase("running")
		if mode == "panic" {
			panic("controlled diagnostic panic")
		}
		if mode == "log_fatal" {
			log.Fatal("controlled diagnostic fatal")
		}
		RuntimeLogger{s}.Fatal("json: unsupported value: NaN")
		os.Exit(37)
	}
	for _, mode := range []string{"panic", "exit", "log_fatal"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestUnhandledPanicAndRuntimeExitArePersisted$")
			cmd.Env = append(os.Environ(), "SCIAIDE_DIAG_CHILD="+mode, "SCIAIDE_DIAG_ROOT="+root)
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatal("fatal failure silently recovered", string(output))
			}
			state := readSession(t, root)
			if mode == "panic" {
				crash, err := os.ReadFile(filepath.Join(root, "go-crash.log"))
				if err != nil || !strings.Contains(string(crash), "controlled diagnostic panic") || !strings.Contains(string(crash), "session_test.go:") || state.Phase != "running" {
					t.Fatal("panic stack lost", state, err, string(crash))
				}
			} else if mode == "exit" {
				log, _ := os.ReadFile(filepath.Join(root, "desktop.jsonl"))
				if !strings.Contains(string(log), "desktop fatal call site") || state.Phase != "runtime_fatal" {
					t.Fatal("framework os.Exit lost", state, string(log))
				}
			} else {
				data, _ := os.ReadFile(filepath.Join(root, "desktop.jsonl"))
				if !strings.Contains(string(data), "Go runtime log") || !strings.Contains(string(data), "log.Fatal") || state.Phase != "running" {
					t.Fatal("standard log.Fatal lost its call site or lifecycle marker", state, string(data))
				}
			}
			next, err := Start(root, "restart")
			if err != nil {
				t.Fatal(err)
			}
			next.Finish(true)
			data, _ := os.ReadFile(filepath.Join(root, "desktop.jsonl"))
			if !strings.Contains(string(data), fmt.Sprintf(`"previousPid":%d`, state.PID)) {
				t.Fatal("restart lost preceding failed session")
			}
		})
	}
}

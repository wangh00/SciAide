//go:build windows

package localexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunnerCapturesOutputAndNonZeroExit(t *testing.T) {
	runner := NewRunner(Options{MaxOutputBytes: 4 * 1024})
	defer runner.Close()
	cmd, err := exec.LookPath("cmd.exe")
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Execute(context.Background(), Request{
		Program: cmd, Args: []string{"/D", "/S", "/C", "echo hello & echo failure 1>&2 & exit /b 7"},
		Dir: t.TempDir(), Env: os.Environ(), Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reason != ReasonExitNonZero || result.ExitCode != 7 || !strings.Contains(result.Stdout.Text, "hello") || !strings.Contains(result.Stderr.Text, "failure") {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunnerContinuesDrainingAfterCaptureLimit(t *testing.T) {
	runner := NewRunner(Options{MaxOutputBytes: 1024})
	defer runner.Close()
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Execute(context.Background(), Request{
		Program: powershell, Args: []string{"-NoProfile", "-NonInteractive", "-Command", "[Console]::Out.Write(('x' * 1048576))"},
		Dir: t.TempDir(), Env: os.Environ(), Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reason != ReasonCompleted || result.Stdout.Bytes != 1<<20 || !result.Stdout.Truncated || len(result.Stdout.Text) != 1024 {
		t.Fatalf("bounded output = %#v", result.Stdout)
	}
}

func TestRunnerDistinguishesTimeoutAndCancellation(t *testing.T) {
	cmd, err := exec.LookPath("cmd.exe")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		context func() (context.Context, context.CancelFunc)
		timeout time.Duration
		reason  TerminationReason
	}{
		{"timeout", func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }, 100 * time.Millisecond, ReasonTimedOut},
		{"cancel", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 100*time.Millisecond)
		}, 5 * time.Second, ReasonTimedOut},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := NewRunner(Options{})
			defer runner.Close()
			ctx, cancel := test.context()
			defer cancel()
			if test.name == "cancel" {
				manual, manualCancel := context.WithCancel(context.Background())
				ctx, cancel = manual, manualCancel
				time.AfterFunc(100*time.Millisecond, manualCancel)
			}
			result, err := runner.Execute(ctx, Request{Program: cmd, Args: []string{"/D", "/C", "ping -n 10 127.0.0.1 >nul"}, Dir: t.TempDir(), Env: os.Environ(), Timeout: test.timeout})
			if err != nil {
				t.Fatal(err)
			}
			expected := test.reason
			if test.name == "cancel" {
				expected = ReasonCancelled
			}
			if result.Reason != expected {
				t.Fatalf("reason = %s, want %s", result.Reason, expected)
			}
		})
	}
}

func TestRunnerKillsDescendantsAfterRootExits(t *testing.T) {
	runner := NewRunner(Options{})
	defer runner.Close()
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "descendant.txt")
	child := "Start-Sleep -Milliseconds 800; Set-Content -LiteralPath '" + strings.ReplaceAll(marker, "'", "''") + "' -Value survived"
	command := "Start-Process -FilePath powershell.exe -ArgumentList @('-NoProfile','-NonInteractive','-Command'," + quotePowerShell(child) + "); exit 0"
	result, err := runner.Execute(context.Background(), Request{Program: powershell, Args: []string{"-NoProfile", "-NonInteractive", "-Command", command}, Dir: filepath.Dir(marker), Env: os.Environ(), Timeout: 5 * time.Second})
	if err != nil || result.Reason != ReasonCompleted {
		t.Fatalf("execute = %#v, %v", result, err)
	}
	time.Sleep(time.Second)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("descendant survived tool completion: %v", err)
	}
}

func TestRunnerRefusesExecutionAfterShutdownBegins(t *testing.T) {
	runner := NewRunner(Options{})
	runner.BeginShutdown()
	cmd, err := exec.LookPath("cmd.exe")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "started.txt")
	_, err = runner.Execute(context.Background(), Request{
		Program: cmd,
		Args:    []string{"/D", "/S", "/C", "echo started>" + marker},
		Dir:     filepath.Dir(marker),
		Env:     os.Environ(),
		Timeout: 5 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Execute() error = %v", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("process started after shutdown: %v", statErr)
	}
}

func TestRunnerStopsActiveExecutionOnAppShutdown(t *testing.T) {
	runner := NewRunner(Options{})
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "started.txt")
	done := make(chan struct {
		result Result
		err    error
	}, 1)
	go func() {
		result, runErr := runner.Execute(context.Background(), Request{
			Program: powershell,
			Args:    []string{"-NoProfile", "-NonInteractive", "-Command", "Set-Content -LiteralPath " + quotePowerShell(marker) + " -Value started; Start-Sleep -Seconds 30"},
			Dir:     filepath.Dir(marker),
			Env:     os.Environ(),
			Timeout: time.Minute,
		})
		done <- struct {
			result Result
			err    error
		}{result, runErr}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("process did not start before shutdown")
		}
		time.Sleep(20 * time.Millisecond)
	}
	runner.BeginShutdown()
	select {
	case outcome := <-done:
		if outcome.err != nil || outcome.result.Reason != ReasonAppShutdown {
			t.Fatalf("shutdown result = %#v, %v", outcome.result, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("active execution did not stop during application shutdown")
	}
	if err := runner.Close(); err != nil {
		t.Fatal(err)
	}
}

func quotePowerShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

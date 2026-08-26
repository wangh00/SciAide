//go:build windows

package localexec

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const localExecHelperEnvironment = "SCIAIDE_LOCAL_EXEC_TEST_HELPER"

func TestLocalExecHelperProcess(t *testing.T) {
	if os.Getenv(localExecHelperEnvironment) != "1" {
		return
	}
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		os.Exit(2)
	}
	arguments := os.Args[separator+1:]
	switch arguments[0] {
	case "result":
		_, _ = os.Stdout.WriteString("hello\n")
		_, _ = os.Stderr.WriteString("failure\n")
		os.Exit(7)
	case "output":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte{'x'}, 1<<20))
	case "sleep":
		time.Sleep(30 * time.Second)
	case "spawn-descendant":
		if len(arguments) != 2 {
			os.Exit(2)
		}
		child := exec.Command(os.Args[0], "-test.run=^TestLocalExecHelperProcess$", "--", "write-marker", arguments[1])
		child.Env = os.Environ()
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		_ = child.Process.Release()
	case "write-marker":
		if len(arguments) != 2 {
			os.Exit(2)
		}
		time.Sleep(800 * time.Millisecond)
		if err := os.WriteFile(arguments[1], []byte("survived"), 0o600); err != nil {
			os.Exit(4)
		}
	case "mark-and-sleep":
		if len(arguments) != 2 {
			os.Exit(2)
		}
		if err := os.WriteFile(arguments[1], []byte("started"), 0o600); err != nil {
			os.Exit(4)
		}
		time.Sleep(30 * time.Second)
	default:
		os.Exit(2)
	}
	os.Exit(0)
}

func localExecHelperCommand(t *testing.T, arguments ...string) (string, []string, []string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return executable, append([]string{"-test.run=^TestLocalExecHelperProcess$", "--"}, arguments...), append(os.Environ(), localExecHelperEnvironment+"=1")
}

func TestRunnerCapturesOutputAndNonZeroExit(t *testing.T) {
	runner := NewRunner(Options{MaxOutputBytes: 4 * 1024})
	defer runner.Close()
	program, arguments, environment := localExecHelperCommand(t, "result")
	result, err := runner.Execute(context.Background(), Request{
		Program: program, Args: arguments,
		Dir: t.TempDir(), Env: environment, Timeout: 5 * time.Second,
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
	program, arguments, environment := localExecHelperCommand(t, "output")
	result, err := runner.Execute(context.Background(), Request{
		Program: program, Args: arguments,
		Dir: t.TempDir(), Env: environment, Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reason != ReasonCompleted || result.Stdout.Bytes != 1<<20 || !result.Stdout.Truncated || len(result.Stdout.Text) != 1024 {
		t.Fatalf("bounded output = %#v", result.Stdout)
	}
}

func TestRunnerDistinguishesTimeoutAndCancellation(t *testing.T) {
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
			program, arguments, environment := localExecHelperCommand(t, "sleep")
			ctx, cancel := test.context()
			defer cancel()
			if test.name == "cancel" {
				manual, manualCancel := context.WithCancel(context.Background())
				ctx, cancel = manual, manualCancel
				time.AfterFunc(100*time.Millisecond, manualCancel)
			}
			result, err := runner.Execute(ctx, Request{Program: program, Args: arguments, Dir: t.TempDir(), Env: environment, Timeout: test.timeout})
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
	marker := filepath.Join(t.TempDir(), "descendant.txt")
	program, arguments, environment := localExecHelperCommand(t, "spawn-descendant", marker)
	result, err := runner.Execute(context.Background(), Request{Program: program, Args: arguments, Dir: filepath.Dir(marker), Env: environment, Timeout: 5 * time.Second})
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
	marker := filepath.Join(t.TempDir(), "started.txt")
	program, arguments, environment := localExecHelperCommand(t, "mark-and-sleep", marker)
	_, err := runner.Execute(context.Background(), Request{
		Program: program,
		Args:    arguments,
		Dir:     filepath.Dir(marker),
		Env:     environment,
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
	marker := filepath.Join(t.TempDir(), "started.txt")
	program, arguments, environment := localExecHelperCommand(t, "mark-and-sleep", marker)
	done := make(chan struct {
		result Result
		err    error
	}, 1)
	go func() {
		result, runErr := runner.Execute(context.Background(), Request{
			Program: program,
			Args:    arguments,
			Dir:     filepath.Dir(marker),
			Env:     environment,
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

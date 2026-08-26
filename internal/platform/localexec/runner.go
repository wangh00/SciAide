// Package localexec owns the lifecycle of model-reachable local processes.
// It is an authorized host executor, not an operating-system security sandbox.
package localexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DefaultOutputBytes  = 64 * 1024
	DefaultDrainTimeout = 2 * time.Second
	DefaultMemoryBytes  = int64(1024 * 1024 * 1024)
	cancellationGrace   = 50 * time.Millisecond
)

type TerminationReason string

const (
	ReasonCompleted   TerminationReason = "completed"
	ReasonExitNonZero TerminationReason = "exit_nonzero"
	ReasonTimedOut    TerminationReason = "timed_out"
	ReasonCancelled   TerminationReason = "cancelled"
	ReasonAppShutdown TerminationReason = "app_shutdown"
	ReasonStartFailed TerminationReason = "start_failed"
)

type Audit struct {
	CallID            string
	RunID             string
	ProjectID         string
	ToolName          string
	ExecutablePath    string
	ExecutableVersion string
	ExecutableSHA256  string
	ScriptPath        string
	ScriptSHA256      string
	CommandSHA256     string
	Workdir           string
	TimeoutMillis     int64
	EnvironmentNames  []string
}

type AuditOutcome struct {
	PID             int
	ExitCode        *int
	Reason          TerminationReason
	StdoutBytes     int64
	StdoutSHA256    string
	StdoutTruncated bool
	StderrBytes     int64
	StderrSHA256    string
	StderrTruncated bool
	ErrorMessage    string
	CompletedAt     time.Time
}

type AuditRecorder interface {
	PrepareProcessExecution(ctx context.Context, audit Audit, at time.Time) error
	MarkProcessExecutionStarted(ctx context.Context, callID string, pid int, at time.Time) error
	FinishProcessExecution(ctx context.Context, callID string, outcome AuditOutcome) error
}

type Request struct {
	Program string
	Args    []string
	Dir     string
	Env     []string
	Timeout time.Duration
	// MemoryLimitBytes is a best-effort platform containment limit. The
	// supported Windows target enforces it for the complete Job Object.
	MemoryLimitBytes int64
	Audit            Audit
}

type Stream struct {
	Text      string `json:"text"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256"`
	Truncated bool   `json:"truncated"`
}

type Result struct {
	PID               int               `json:"pid"`
	ExitCode          int               `json:"exitCode"`
	Reason            TerminationReason `json:"terminationReason"`
	Stdout            Stream            `json:"stdout"`
	Stderr            Stream            `json:"stderr"`
	ExecutableVersion string            `json:"executableVersion"`
	StartedAt         time.Time         `json:"startedAt"`
	FinishedAt        time.Time         `json:"finishedAt"`
}

type Options struct {
	MaxOutputBytes   int
	DrainTimeout     time.Duration
	MemoryLimitBytes int64
	Recorder         AuditRecorder
}

type Runner struct {
	maxOutput int
	drain     time.Duration
	memory    int64
	recorder  AuditRecorder

	mu     sync.Mutex
	active map[*executionState]struct{}
	closed bool
	wg     sync.WaitGroup
}

type executionState struct {
	controller *processController
	mu         sync.Mutex
	reason     TerminationReason
}

func NewRunner(options Options) *Runner {
	maxOutput := options.MaxOutputBytes
	if maxOutput <= 0 {
		maxOutput = DefaultOutputBytes
	}
	drain := options.DrainTimeout
	if drain <= 0 {
		drain = DefaultDrainTimeout
	}
	memory := options.MemoryLimitBytes
	if memory == 0 {
		memory = DefaultMemoryBytes
	}
	return &Runner{maxOutput: maxOutput, drain: drain, memory: memory, recorder: options.Recorder, active: make(map[*executionState]struct{})}
}

func (r *Runner) Execute(ctx context.Context, request Request) (Result, error) {
	if r == nil {
		return Result{}, fmt.Errorf("local process runner is not configured")
	}
	request.Program, request.Dir = strings.TrimSpace(request.Program), strings.TrimSpace(request.Dir)
	if request.Program == "" || request.Dir == "" || request.Timeout <= 0 {
		return Result{}, fmt.Errorf("local process executable, workdir and timeout are required")
	}
	request.Audit.EnvironmentNames = append([]string(nil), request.Audit.EnvironmentNames...)
	sort.Strings(request.Audit.EnvironmentNames)
	preparedAt := time.Now().UTC()
	if r.recorder != nil {
		if err := r.recorder.PrepareProcessExecution(ctx, request.Audit, preparedAt); err != nil {
			return Result{}, fmt.Errorf("prepare process execution audit: %w", err)
		}
	}
	prepared := r.recorder != nil
	finishPreparationFailure := func(err error) {
		if prepared {
			_ = r.recorder.FinishProcessExecution(context.Background(), request.Audit.CallID, AuditOutcome{Reason: ReasonStartFailed, ErrorMessage: err.Error(), CompletedAt: time.Now().UTC()})
		}
	}

	memory := request.MemoryLimitBytes
	if memory == 0 {
		memory = r.memory
	}
	controller, err := newProcessController(memory)
	if err != nil {
		finishPreparationFailure(err)
		return Result{}, fmt.Errorf("create process containment: %w", err)
	}
	state := &executionState{controller: controller}
	if !r.register(state) {
		_ = controller.close()
		err := fmt.Errorf("local process runner is closed")
		finishPreparationFailure(err)
		return Result{}, err
	}
	defer func() {
		r.unregister(state)
		_ = controller.close()
	}()

	cmd := exec.Command(request.Program, request.Args...)
	cmd.Dir = request.Dir
	cmd.Env = append([]string(nil), request.Env...)
	controller.prepare(cmd)
	stdoutPipe, stdoutWriter, err := os.Pipe()
	if err != nil {
		finishPreparationFailure(err)
		return Result{}, err
	}
	defer stdoutPipe.Close()
	stderrPipe, stderrWriter, err := os.Pipe()
	if err != nil {
		stdoutWriter.Close()
		finishPreparationFailure(err)
		return Result{}, err
	}
	defer stderrPipe.Close()
	cmd.Stdout, cmd.Stderr = stdoutWriter, stderrWriter
	startedAt := time.Now().UTC()
	if err := r.start(state, cmd); err != nil {
		stdoutWriter.Close()
		stderrWriter.Close()
		finishPreparationFailure(err)
		return Result{}, fmt.Errorf("start local process: %w", err)
	}
	// Close the parent's writer handles immediately. Descendants retain their
	// inherited copies until the Job Object/process group is terminated.
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	pid := cmd.Process.Pid
	if r.recorder != nil {
		if err := r.recorder.MarkProcessExecutionStarted(context.Background(), request.Audit.CallID, pid, startedAt); err != nil {
			_ = controller.terminate()
			_, _ = waitCommand(cmd)
			finishPreparationFailure(err)
			return Result{}, fmt.Errorf("record local process start: %w", err)
		}
	}

	stdout := newCapture(r.maxOutput)
	stderr := newCapture(r.maxOutput)
	stdoutDone := drainPipe(stdoutPipe, stdout)
	stderrDone := drainPipe(stderrPipe, stderr)
	waitDone := make(chan waitResult, 1)
	go func() {
		state, waitErr := waitCommand(cmd)
		waitDone <- waitResult{state: state, err: waitErr}
	}()

	timer := time.NewTimer(request.Timeout)
	defer timer.Stop()
	var waited waitResult
	select {
	case waited = <-waitDone:
	case <-timer.C:
		state.setReason(ReasonTimedOut)
		_ = controller.terminate()
		waited = <-waitDone
	case <-ctx.Done():
		reason := ReasonCancelled
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			reason = ReasonTimedOut
		}
		state.setReason(reason)
		_ = controller.interrupt(cmd.Process)
		grace := time.NewTimer(cancellationGrace)
		select {
		case waited = <-waitDone:
			if !grace.Stop() {
				<-grace.C
			}
		case <-grace.C:
			_ = controller.terminate()
			waited = <-waitDone
		}
	}
	// Background descendants are not allowed to outlive a tool call, including
	// successful calls whose root process exited before its children.
	_ = controller.terminate()

	drainErr := errors.Join(awaitDrain(stdoutPipe, stdoutDone, r.drain), awaitDrain(stderrPipe, stderrDone, r.drain))
	stdoutResult, stderrResult := stdout.result(), stderr.result()
	finishedAt := time.Now().UTC()
	exitCode := -1
	if waited.state != nil {
		exitCode = waited.state.ExitCode()
	}
	reason := state.getReason()
	if reason == "" {
		if waited.err == nil && exitCode == 0 {
			reason = ReasonCompleted
		} else {
			reason = ReasonExitNonZero
		}
	}
	result := Result{PID: pid, ExitCode: exitCode, Reason: reason, Stdout: stdoutResult, Stderr: stderrResult, ExecutableVersion: request.Audit.ExecutableVersion, StartedAt: startedAt, FinishedAt: finishedAt}
	operationalErr := errors.Join(waited.err, drainErr)
	if r.recorder != nil {
		code := exitCode
		outcome := AuditOutcome{PID: pid, ExitCode: &code, Reason: reason,
			StdoutBytes: stdoutResult.Bytes, StdoutSHA256: stdoutResult.SHA256, StdoutTruncated: stdoutResult.Truncated,
			StderrBytes: stderrResult.Bytes, StderrSHA256: stderrResult.SHA256, StderrTruncated: stderrResult.Truncated,
			CompletedAt: finishedAt}
		if operationalErr != nil {
			outcome.ErrorMessage = operationalErr.Error()
		}
		if err := r.recorder.FinishProcessExecution(context.Background(), request.Audit.CallID, outcome); err != nil {
			return result, fmt.Errorf("finish process execution audit: %w", err)
		}
	}
	if operationalErr != nil {
		return result, operationalErr
	}
	return result, nil
}

func (r *Runner) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		r.wg.Wait()
		return nil
	}
	r.closed = true
	states := make([]*executionState, 0, len(r.active))
	for state := range r.active {
		states = append(states, state)
		state.setReason(ReasonAppShutdown)
	}
	r.mu.Unlock()
	var terminateErrors []error
	for _, state := range states {
		if err := state.controller.terminate(); err != nil {
			terminateErrors = append(terminateErrors, err)
		}
	}
	r.wg.Wait()
	return errors.Join(terminateErrors...)
}

// BeginShutdown prevents new executions and terminates active process trees
// without waiting for their tool goroutines to unwind.
func (r *Runner) BeginShutdown() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closed = true
	states := make([]*executionState, 0, len(r.active))
	for state := range r.active {
		states = append(states, state)
		state.setReason(ReasonAppShutdown)
	}
	r.mu.Unlock()
	for _, state := range states {
		_ = state.controller.terminate()
	}
}

func (r *Runner) register(state *executionState) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	r.active[state] = struct{}{}
	r.wg.Add(1)
	return true
}

// start serializes process creation and containment with shutdown. Once this
// method returns, shutdown either cannot have started yet or will observe and
// terminate the contained process through the active execution state.
func (r *Runner) start(state *executionState, cmd *exec.Cmd) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fmt.Errorf("local process runner is closed")
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := state.controller.containAndResume(cmd.Process); err != nil {
		_ = cmd.Process.Kill()
		_, _ = waitCommand(cmd)
		return fmt.Errorf("contain local process: %w", err)
	}
	return nil
}

func (r *Runner) unregister(state *executionState) {
	r.mu.Lock()
	delete(r.active, state)
	r.mu.Unlock()
	r.wg.Done()
}

func (s *executionState) setReason(reason TerminationReason) {
	s.mu.Lock()
	if s.reason == "" || reason == ReasonAppShutdown {
		s.reason = reason
	}
	s.mu.Unlock()
}

func (s *executionState) getReason() TerminationReason {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reason
}

type waitResult struct {
	state *os.ProcessState
	err   error
}

func waitCommand(cmd *exec.Cmd) (*os.ProcessState, error) {
	err := cmd.Wait()
	if _, ok := err.(*exec.ExitError); ok {
		return cmd.ProcessState, nil
	}
	return cmd.ProcessState, err
}

type capture struct {
	mu        sync.Mutex
	retained  []byte
	total     int64
	digest    hash.Hash
	truncated bool
	limit     int
}

func newCapture(limit int) *capture {
	return &capture{retained: make([]byte, 0, min(limit, 8*1024)), digest: sha256.New(), limit: limit}
}

func (c *capture) Write(value []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total += int64(len(value))
	_, _ = c.digest.Write(value)
	if len(c.retained) < c.limit {
		remaining := c.limit - len(c.retained)
		c.retained = append(c.retained, value[:min(remaining, len(value))]...)
	}
	if c.total > int64(c.limit) {
		c.truncated = true
	}
	return len(value), nil
}

func (c *capture) result() Stream {
	c.mu.Lock()
	defer c.mu.Unlock()
	text := strings.ToValidUTF8(string(c.retained), "\uFFFD")
	if len(text) > c.limit {
		text = truncateUTF8Bytes(text, c.limit)
	}
	return Stream{Text: text, Bytes: c.total, SHA256: hex.EncodeToString(c.digest.Sum(nil)), Truncated: c.truncated}
}

func truncateUTF8Bytes(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	for limit > 0 && (value[limit]&0xc0) == 0x80 {
		limit--
	}
	return value[:limit]
}

func drainPipe(reader io.ReadCloser, target io.Writer) <-chan error {
	done := make(chan error, 1)
	go func() {
		buffer := make([]byte, 8*1024)
		_, err := io.CopyBuffer(target, reader, buffer)
		if errors.Is(err, os.ErrClosed) {
			err = nil
		}
		done <- err
	}()
	return done
}

func awaitDrain(reader io.ReadCloser, done <-chan error, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		_ = reader.Close()
		return err
	case <-timer.C:
		_ = reader.Close()
		return fmt.Errorf("process output pipe did not close within %s", timeout)
	}
}

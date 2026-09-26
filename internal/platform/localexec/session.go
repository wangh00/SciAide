package localexec

import (
	"errors"
	"fmt"
	"github.com/wangh00/SciAide/internal/network"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type SessionRequest struct {
	NetworkPinned    bool
	CallID           string
	Program          string
	Args             []string
	Dir              string
	Env              []string
	MemoryLimitBytes int64
}

type SessionOutcome struct {
	ExitCode  int
	Reason    TerminationReason
	StartedAt time.Time
	ExitedAt  time.Time
	Err       error
}

// Session is a long-lived, process-tree-contained child. Callers must
// continuously drain Stdout and Stderr and eventually call Close.
type Session struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
	state  *executionState
	cmd    *exec.Cmd
	runner *Runner
	live   *liveProcess

	startedAt time.Time
	done      chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	outcome   SessionOutcome
}

func (r *Runner) StartSession(request SessionRequest) (*Session, error) {
	if r == nil {
		return nil, fmt.Errorf("local process runner is not configured")
	}
	request.Program, request.Dir = strings.TrimSpace(request.Program), strings.TrimSpace(request.Dir)
	if request.Program == "" || request.Dir == "" {
		return nil, fmt.Errorf("session executable and workdir are required")
	}
	memory := request.MemoryLimitBytes
	if memory == 0 {
		memory = r.memory
	}
	controller, err := newProcessController(memory)
	if err != nil {
		return nil, fmt.Errorf("create process containment: %w", err)
	}
	state := &executionState{controller: controller}
	if !r.register(state) {
		_ = controller.close()
		return nil, fmt.Errorf("local process runner is closed")
	}
	fail := func(err error) (*Session, error) {
		r.unregister(state)
		_ = controller.close()
		return nil, err
	}
	cmd := exec.Command(request.Program, request.Args...)
	cmd.Dir = request.Dir
	cmd.Env = append([]string(nil), request.Env...)
	if !request.NetworkPinned {
		cmd.Env = network.Environment(cmd.Env, "dependencies")
	}
	controller.prepare(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fail(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fail(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return fail(err)
	}
	startedAt := time.Now().UTC()
	if err := r.start(state, cmd); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return fail(fmt.Errorf("start contained session: %w", err))
	}
	live := newLiveProcess(request.CallID, cmd.Process.Pid)
	if strings.TrimSpace(request.CallID) != "" {
		r.addLive(live)
	}
	session := &Session{stdin: stdin, stdout: &trackedReader{reader: stdout, process: live, stdout: true}, stderr: &trackedReader{reader: stderr, process: live, stdout: false}, state: state, cmd: cmd, runner: r, live: live, startedAt: startedAt, done: make(chan struct{})}
	go session.wait()
	return session, nil
}

func (s *Session) Stdin() io.Writer  { return s.stdin }
func (s *Session) Stdout() io.Reader { return s.stdout }
func (s *Session) Stderr() io.Reader { return s.stderr }

// SetRuntimeCallID associates a persistent Kernel session with its current
// tool call. A session is reused across executions, so this moves the live
// observer from the previous call to the new one.
func (s *Session) SetRuntimeCallID(callID string) {
	if s == nil || s.runner == nil || s.live == nil {
		return
	}
	callID = strings.TrimSpace(callID)
	s.runner.mu.Lock()
	old := strings.TrimSpace(s.live.value.CallID)
	if old != "" && s.runner.live[old] == s.live {
		delete(s.runner.live, old)
	}
	s.live.mu.Lock()
	// The live buffers are replaced for every Kernel execution. Keep the
	// replacement under the same lock used by trackedReader and snapshot so a
	// long-lived session cannot mix two calls' output tails.
	s.live.value.CallID = callID
	s.live.value.StdoutTail, s.live.value.StderrTail = "", ""
	s.live.value.StdoutBytes, s.live.value.StderrBytes = 0, 0
	s.live.stdout = &tailBuffer{limit: LiveOutputBytes}
	s.live.stderr = &tailBuffer{limit: LiveOutputBytes}
	s.live.value.UpdatedAt = time.Now().UTC()
	s.live.mu.Unlock()
	if callID != "" {
		s.runner.live[callID] = s.live
	}
	s.runner.mu.Unlock()
}
func (s *Session) PID() int {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

func (s *Session) wait() {
	processState, waitErr := waitCommand(s.cmd)
	_ = s.state.controller.terminate()
	_ = s.stdin.Close()
	exitCode := -1
	if processState != nil {
		exitCode = processState.ExitCode()
	}
	reason := s.state.getReason()
	if reason == "" {
		if waitErr == nil && exitCode == 0 {
			reason = ReasonCompleted
		} else {
			reason = ReasonExitNonZero
		}
	}
	s.mu.Lock()
	s.outcome = SessionOutcome{ExitCode: exitCode, Reason: reason, StartedAt: s.startedAt, ExitedAt: time.Now().UTC(), Err: waitErr}
	s.mu.Unlock()
	if s.live != nil {
		// Keep the lock order (runner -> live) consistent with SetRuntimeCallID
		// to avoid a shutdown/retagging deadlock.
		s.runner.mu.Lock()
		s.live.mu.Lock()
		callID := s.live.value.CallID
		if callID != "" && s.runner.live[callID] == s.live {
			delete(s.runner.live, callID)
		}
		s.live.mu.Unlock()
		s.runner.mu.Unlock()
	}
	_ = s.state.controller.close()
	s.runner.unregister(s.state)
	close(s.done)
}

type trackedReader struct {
	reader  io.ReadCloser
	process *liveProcess
	stdout  bool
}

func (r *trackedReader) Read(value []byte) (int, error) {
	n, err := r.reader.Read(value)
	if n > 0 && r.process != nil {
		_, _ = r.process.writer(r.stdout).Write(value[:n])
	}
	return n, err
}

func (r *trackedReader) Close() error { return r.reader.Close() }

func (s *Session) Wait() SessionOutcome {
	if s == nil {
		return SessionOutcome{ExitCode: -1, Reason: ReasonStartFailed, Err: fmt.Errorf("session is nil")}
	}
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.outcome
}

func (s *Session) Done() <-chan struct{} {
	if s == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return s.done
}

func (s *Session) Terminate(reason TerminationReason) error {
	if s == nil {
		return nil
	}
	if reason == "" {
		reason = ReasonCancelled
	}
	s.state.setReason(reason)
	return s.state.controller.terminate()
}

func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	var closeErr error
	s.closeOnce.Do(func() {
		closeErr = errors.Join(s.stdin.Close(), s.Terminate(ReasonCancelled))
	})
	<-s.done
	return closeErr
}

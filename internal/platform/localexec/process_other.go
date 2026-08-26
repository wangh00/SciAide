//go:build !windows

package localexec

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

type processController struct {
	mu  sync.Mutex
	pid int
}

func newProcessController(_ int64) (*processController, error) { return &processController{}, nil }

func (*processController) prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func (c *processController) containAndResume(process *os.Process) error {
	if process != nil {
		c.mu.Lock()
		c.pid = process.Pid
		c.mu.Unlock()
	}
	return nil
}

func (*processController) interrupt(process *os.Process) error {
	if process == nil {
		return nil
	}
	err := syscall.Kill(-process.Pid, syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (c *processController) terminate() error {
	c.mu.Lock()
	pid := c.pid
	c.mu.Unlock()
	if pid == 0 {
		return nil
	}
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (*processController) close() error { return nil }

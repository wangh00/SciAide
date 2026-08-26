//go:build windows

package localexec

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var ntResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

type processController struct {
	job windows.Handle
}

func newProcessController(memoryLimitBytes int64) (*processController, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if memoryLimitBytes > 0 {
		limits.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_JOB_MEMORY
		limits.JobMemoryLimit = uintptr(memoryLimitBytes)
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	return &processController{job: job}, nil
}

func (c *processController) prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW}
}

func (c *processController) containAndResume(process *os.Process) error {
	if process == nil {
		return fmt.Errorf("started process handle is unavailable")
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME, false, uint32(process.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	if err := windows.AssignProcessToJobObject(c.job, handle); err != nil {
		return err
	}
	status, _, callErr := ntResumeProcess.Call(uintptr(handle))
	if int32(status) < 0 {
		return fmt.Errorf("NtResumeProcess failed with NTSTATUS %#x: %v", uint32(status), callErr)
	}
	return nil
}

func (c *processController) interrupt(process *os.Process) error {
	if process == nil {
		return nil
	}
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(process.Pid))
}

func (c *processController) terminate() error {
	if c == nil || c.job == 0 {
		return nil
	}
	err := windows.TerminateJobObject(c.job, 1)
	if err == windows.ERROR_ACCESS_DENIED {
		return nil
	}
	return err
}

func (c *processController) close() error {
	if c == nil || c.job == 0 {
		return nil
	}
	err := windows.CloseHandle(c.job)
	c.job = 0
	return err
}

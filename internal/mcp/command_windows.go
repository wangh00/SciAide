//go:build windows

package mcp

import (
	"os/exec"
	"syscall"
)

// createNoWindow prevents console applications and command wrappers such as
// npx.cmd from allocating a visible console when SciAide launches them from
// its GUI process. HideWindow is kept as a second line of defence for Windows
// command launch paths that still honour STARTUPINFO.ShowWindow.
const createNoWindow = 0x08000000

func configureBackgroundCommand(command *exec.Cmd) {
	if command == nil {
		return
	}
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.HideWindow = true
	command.SysProcAttr.CreationFlags |= createNoWindow
}

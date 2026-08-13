//go:build !windows

package mcp

import "os/exec"

func configureBackgroundCommand(_ *exec.Cmd) {}

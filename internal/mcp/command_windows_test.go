//go:build windows

package mcp

import (
	"os/exec"
	"syscall"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wangh00/SciAide/internal/app/mcpserver"
)

func TestConfigureBackgroundCommandHidesWindowsConsoleAndPreservesFlags(t *testing.T) {
	const existingFlag = 0x00000200
	command := exec.Command("cmd.exe", "/c", "exit", "0")
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: existingFlag}

	configureBackgroundCommand(command)

	if command.SysProcAttr == nil || !command.SysProcAttr.HideWindow {
		t.Fatal("Windows MCP command was not configured to hide its window")
	}
	if command.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("CREATE_NO_WINDOW missing from %#x", command.SysProcAttr.CreationFlags)
	}
	if command.SysProcAttr.CreationFlags&existingFlag == 0 {
		t.Fatalf("existing creation flags were lost: %#x", command.SysProcAttr.CreationFlags)
	}
}

func TestConfigureBackgroundCommandAllowsStdioPipes(t *testing.T) {
	command := exec.Command("cmd.exe", "/c", "echo", "ready")
	configureBackgroundCommand(command)
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	if len(output) == 0 {
		t.Fatal("hidden command did not preserve stdout piping")
	}
}

func TestBuildTransportConfiguresWindowsStdioProcessAsBackground(t *testing.T) {
	transport, err := buildTransport(mcpserver.Server{
		Transport:      mcpserver.TransportStdio,
		Command:        "npx.cmd",
		Args:           []string{"-y", "fixture"},
		TimeoutSeconds: 30,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	commandTransport, ok := transport.(*mcpsdk.CommandTransport)
	if !ok || commandTransport.Command == nil {
		t.Fatalf("transport = %#v", transport)
	}
	attributes := commandTransport.Command.SysProcAttr
	if attributes == nil || !attributes.HideWindow || attributes.CreationFlags&createNoWindow == 0 {
		t.Fatalf("stdio process attributes = %#v", attributes)
	}
	if commandTransport.Command.Path == "" || len(commandTransport.Command.Args) != 3 {
		t.Fatalf("command boundary changed: path=%q args=%#v", commandTransport.Command.Path, commandTransport.Command.Args)
	}
}

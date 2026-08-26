package pythonruntime

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/wangh00/SciAide/internal/platform/localexec"
)

func TestAdapterCreatesAndFreezesRealVirtualEnvironment(t *testing.T) {
	name := "python"
	if runtime.GOOS != "windows" {
		name = "python3"
	}
	python, err := exec.LookPath(name)
	if err != nil {
		t.Skip("Python 3 is not installed")
	}
	runner := localexec.NewRunner(localexec.Options{MaxOutputBytes: 512 * 1024})
	defer runner.Close()
	adapter := New(runner)
	discovery, err := adapter.Discover(context.Background(), python)
	if err != nil || len(discovery.Interpreters) == 0 {
		t.Fatalf("Discover() = %#v, %v", discovery, err)
	}
	destination := filepath.Join(t.TempDir(), "venv")
	if err := adapter.CreateEnvironment(context.Background(), discovery.Interpreters[0].ExecutablePath, destination); err != nil {
		t.Fatalf("CreateEnvironment() error = %v", err)
	}
	environmentPython := filepath.Join(destination, "bin", "python")
	if runtime.GOOS == "windows" {
		environmentPython = filepath.Join(destination, "Scripts", "python.exe")
	}
	probe, err := adapter.Probe(context.Background(), environmentPython)
	if err != nil {
		t.Fatalf("Probe(environment) error = %v", err)
	}
	if probe.Prefix == probe.BasePrefix || !probe.HasPip {
		t.Fatalf("environment probe = %#v", probe)
	}
	lock, err := adapter.Freeze(context.Background(), environmentPython)
	if err != nil {
		t.Fatalf("Freeze() error = %v", err)
	}
	if len(lock) == 0 {
		t.Fatal("Freeze() returned no base packages")
	}
}

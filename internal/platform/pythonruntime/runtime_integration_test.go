package pythonruntime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
	root := t.TempDir()
	destination := filepath.Join(root, ".staging-test")
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
	if err := adapter.ValidateEnvironmentScripts(context.Background(), environmentPython); err == nil {
		t.Fatal("staging path must not pass published script validation")
	}
	// A local synthetic wheel's metadata exercises non-pip console entry points
	// without network access, package installation or touching user environments.
	output, err := exec.Command(environmentPython, "-I", "-c", "import sysconfig; print(sysconfig.get_path('purelib'))").Output()
	if err != nil {
		t.Fatal(err)
	}
	dist := filepath.Join(strings.TrimSpace(string(output)), "sciaide_cli_test-1.0.dist-info")
	if err := os.Mkdir(dist, 0700); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{"METADATA": "Metadata-Version: 2.1\nName: sciaide-cli-test\nVersion: 1.0\n", "entry_points.txt": "[console_scripts]\nsciaide-json-test = json.tool:main\n", "RECORD": ""} {
		if err := os.WriteFile(filepath.Join(dist, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	published := filepath.Join(root, "venv")
	if err := os.Rename(destination, published); err != nil {
		t.Fatal(err)
	}
	environmentPython = filepath.Join(published, "bin", "python")
	if runtime.GOOS == "windows" {
		environmentPython = filepath.Join(published, "Scripts", "python.exe")
	}
	if err := adapter.ValidateEnvironmentScripts(context.Background(), environmentPython); err == nil {
		t.Fatal("renamed venv unexpectedly passes script validation")
	}
	if err := adapter.FinalizeEnvironment(context.Background(), environmentPython, destination); err != nil {
		t.Fatalf("FinalizeEnvironment: %v", err)
	}
	if err := adapter.ValidateEnvironmentScripts(context.Background(), environmentPython); err != nil {
		t.Fatalf("published script validation: %v", err)
	}
	for _, name := range []string{"pip", "pip3", "pip" + strings.Join(strings.Split(probe.Version, ".")[:2], ".")} {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		if out, err := exec.Command(filepath.Join(filepath.Dir(environmentPython), name), "--version").CombinedOutput(); err != nil {
			t.Fatalf("pip alias %s: %v: %s", name, err, out)
		}
	}
	cli := filepath.Join(filepath.Dir(environmentPython), "sciaide-json-test")
	if runtime.GOOS == "windows" {
		cli += ".exe"
	}
	if out, err := exec.Command(cli, "--help").CombinedOutput(); err != nil {
		t.Fatalf("generated package CLI: %v: %s", err, out)
	}
	// Execute activation as a user would, not just a textual match.
	var activate *exec.Cmd
	if runtime.GOOS == "windows" {
		batch := "@echo off\r\ncall \"" + filepath.Join(published, "Scripts", "activate.bat") + "\"\r\npython -c \"import sys; print(sys.prefix)\"\r\n"
		if err := os.WriteFile(filepath.Join(root, "test-activate.cmd"), []byte(batch), 0600); err != nil {
			t.Fatal(err)
		}
		activate = exec.Command("cmd.exe", "/d", "/c", "test-activate.cmd")
		activate.Dir = root
	} else {
		activate = exec.Command("sh", "-c", `. "$1"; python -c 'import sys; print(sys.prefix)'`, "sh", filepath.Join(published, "bin", "activate"))
	}
	if out, err := activate.CombinedOutput(); err != nil || !strings.Contains(string(out), published) {
		t.Fatalf("activation: %v: %s", err, out)
	}
	// Reject path traversal metadata before invoking distlib or writing scripts.
	newDist := filepath.Join(published, strings.TrimPrefix(dist, destination+string(filepath.Separator)))
	if err := os.WriteFile(filepath.Join(newDist, "entry_points.txt"), []byte("[console_scripts]\n../escape = json.tool:main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := adapter.FinalizeEnvironment(context.Background(), environmentPython, destination); err == nil {
		t.Fatal("accepted unsafe entry point name")
	}
}

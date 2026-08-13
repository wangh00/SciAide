//go:build windows

package pathguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGuardRejectsWindowsJunction(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	outside := filepath.Join(parent, "outside")
	junction := filepath.Join(workspace, "junction")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", junction, outside).CombinedOutput(); err != nil {
		t.Skipf("junction creation unavailable: %v (%s)", err, output)
	}
	defer os.Remove(junction)
	guard, err := Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	if file, _, err := guard.OpenFile(filepath.Join("junction", "secret.txt")); err == nil {
		file.Close()
		t.Fatal("junction escaping Workspace was opened")
	}
}

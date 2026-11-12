// Produces a build-only derived module; never edits the shared Go module cache.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const oldReload = `func (f *Frontend) WindowReloadApp() {
	f.ExecJS(fmt.Sprintf("window.location.href = '%s';", f.startURL))
}`

const nativeReload = `func (f *Frontend) WindowReloadApp() {
	// A crashed renderer cannot execute window.location.href reliably. Queue
	// native WebView navigation on its owning UI thread, without page JS.
	f.mainWindow.Invoke(func() {
		f.chromium.Navigate(f.startURL.String())
	})
}`

func patch(source string) (string, error) {
	if strings.Count(source, oldReload) != 1 {
		return "", fmt.Errorf("Wails WindowReloadApp source changed; review the native-navigation patch before building")
	}
	return strings.Replace(source, oldReload, nativeReload, 1), nil
}

func run(output string) (string, error) {
	data, err := exec.Command("go", "list", "-m", "-json", "github.com/wailsapp/wails/v2").Output()
	if err != nil {
		return "", fmt.Errorf("locate pinned Wails module: %w", err)
	}
	var module struct{ Dir string }
	if err := json.Unmarshal(data, &module); err != nil || module.Dir == "" {
		return "", fmt.Errorf("invalid Wails module location")
	}
	original := filepath.Join(module.Dir, "internal", "frontend", "desktop", "windows", "frontend.go")
	source, err := os.ReadFile(original)
	if err != nil {
		return "", err
	}
	derived, err := patch(strings.ReplaceAll(string(source), "\r\n", "\n"))
	if err != nil {
		return "", err
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(output, 0o700); err != nil {
		return "", err
	}
	// Go 1.26 disallows overlays underneath GOMODCACHE. Use a local derived
	// module and build-only workspace; original go.mod/go.sum stay intact.
	moduleCopy := filepath.Join(output, "wails")
	if err := copyModule(module.Dir, moduleCopy); err != nil {
		return "", err
	}
	target := filepath.Join(moduleCopy, "internal", "frontend", "desktop", "windows", "frontend.go")
	if err := os.WriteFile(target, []byte(derived), 0o600); err != nil {
		return "", err
	}
	project, err := os.Getwd()
	if err != nil {
		return "", err
	}
	workfile := filepath.Join(output, "go.work")
	workspace := "go 1.25.0\n\nuse " + strconv.Quote(project) + "\n\nreplace github.com/wailsapp/wails/v2 => " + strconv.Quote(moduleCopy) + "\n"
	return workfile, os.WriteFile(workfile, []byte(workspace), 0o600)
}

func copyModule(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("non-regular file in pinned Wails module: %s", relative)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, data, 0o600)
	})
}

func main() {
	output := flag.String("output", ".tmp/wails-ui-runtime", "derived module and build-only workspace directory")
	flag.Parse()
	path, err := run(*output)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(path)
}

package pythonruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/pythonenv"
	"github.com/wangh00/SciAide/internal/platform/localexec"
)

const probeScript = `import json, platform, sys
try:
    import venv
    has_venv = True
except Exception:
    has_venv = False
try:
    import pip
    has_pip = True
except Exception:
    has_pip = False
print(json.dumps({
    "executable": sys.executable,
    "version": platform.python_version(),
    "major": sys.version_info.major,
    "architecture": platform.architecture()[0],
    "implementation": platform.python_implementation(),
    "prefix": sys.prefix,
    "base_prefix": sys.base_prefix,
    "has_venv": has_venv,
    "has_pip": has_pip,
}, ensure_ascii=True))`

type Adapter struct {
	runner *localexec.Runner
}

func New(runner *localexec.Runner) *Adapter { return &Adapter{runner: runner} }

func (a *Adapter) Discover(ctx context.Context, preferredPath string) (pythonenv.Discovery, error) {
	if a == nil || a.runner == nil {
		return pythonenv.Discovery{}, fmt.Errorf("Python runtime adapter is not configured")
	}
	candidates := make([]string, 0, 3)
	if preferredPath = strings.TrimSpace(preferredPath); preferredPath != "" {
		if !filepath.IsAbs(preferredPath) {
			return pythonenv.Discovery{}, fmt.Errorf("selected Python interpreter path must be absolute")
		}
		candidates = append(candidates, preferredPath)
	}
	for _, name := range []string{"python", "python3"} {
		if path, err := exec.LookPath(name); err == nil {
			candidates = append(candidates, path)
		}
	}
	seen := map[string]bool{}
	interpreters := make([]pythonenv.Interpreter, 0, len(candidates))
	var preferredErr error
	for index, candidate := range candidates {
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		key := filepath.Clean(absolute)
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		value, err := a.Probe(ctx, absolute)
		if err != nil {
			if preferredPath != "" && index == 0 {
				preferredErr = err
			}
			continue
		}
		interpreters = append(interpreters, value)
	}
	if preferredPath != "" && preferredErr != nil {
		return pythonenv.Discovery{}, fmt.Errorf("selected Python interpreter is unavailable: %w", preferredErr)
	}
	status, message := "available", fmt.Sprintf("检测到 %d 个可用的 64 位 Python 3 解释器", len(interpreters))
	if len(interpreters) == 0 {
		status, message = "unavailable", "未检测到 Python 3；请安装 64 位 Python 3，或手动选择 python.exe"
	}
	return pythonenv.Discovery{Status: status, Message: message, Interpreters: interpreters}, nil
}

func (a *Adapter) Probe(ctx context.Context, executablePath string) (pythonenv.Interpreter, error) {
	executablePath, err := secureExecutablePath(executablePath)
	if err != nil {
		return pythonenv.Interpreter{}, err
	}
	environment, _ := localexec.CoreEnvironment(map[string]string{"PYTHONIOENCODING": "utf-8", "PYTHONUTF8": "1", "NO_COLOR": "1"})
	result, err := a.runner.Execute(ctx, localexec.Request{
		Program: executablePath,
		Args:    []string{"-I", "-u", "-X", "utf8", "-c", probeScript},
		Dir:     filepath.Dir(executablePath),
		Env:     environment,
		Timeout: 10 * time.Second,
	})
	if err != nil || result.ExitCode != 0 {
		return pythonenv.Interpreter{}, executionError("probe Python interpreter", result, err)
	}
	var payload struct {
		Executable     string `json:"executable"`
		Version        string `json:"version"`
		Major          int    `json:"major"`
		Architecture   string `json:"architecture"`
		Implementation string `json:"implementation"`
		Prefix         string `json:"prefix"`
		BasePrefix     string `json:"base_prefix"`
		HasVenv        bool   `json:"has_venv"`
		HasPip         bool   `json:"has_pip"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout.Text)), &payload); err != nil {
		return pythonenv.Interpreter{}, fmt.Errorf("parse Python probe output: %w", err)
	}
	if payload.Major != 3 {
		return pythonenv.Interpreter{}, fmt.Errorf("Python 3 is required")
	}
	if payload.Architecture != "64bit" {
		return pythonenv.Interpreter{}, fmt.Errorf("64-bit Python is required")
	}
	hash, err := hashFile(executablePath)
	if err != nil {
		return pythonenv.Interpreter{}, err
	}
	return pythonenv.Interpreter{
		ExecutablePath: executablePath, Version: payload.Version, Architecture: payload.Architecture,
		Implementation: payload.Implementation, Prefix: filepath.Clean(payload.Prefix), BasePrefix: filepath.Clean(payload.BasePrefix),
		ExecutableSHA256: hash, HasVenv: payload.HasVenv, HasPip: payload.HasPip,
	}, nil
}

func (a *Adapter) CreateEnvironment(ctx context.Context, baseExecutablePath, destination string) error {
	baseExecutablePath, err := secureExecutablePath(baseExecutablePath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(destination) == "" || !filepath.IsAbs(destination) {
		return fmt.Errorf("Python environment staging path must be absolute")
	}
	environment, _ := localexec.CoreEnvironment(map[string]string{"PYTHONIOENCODING": "utf-8", "PYTHONUTF8": "1", "PIP_DISABLE_PIP_VERSION_CHECK": "1", "NO_COLOR": "1"})
	result, runErr := a.runner.Execute(ctx, localexec.Request{
		Program: baseExecutablePath,
		Args:    []string{"-I", "-u", "-X", "utf8", "-m", "venv", destination},
		Dir:     filepath.Dir(destination),
		Env:     environment,
		Timeout: 5 * time.Minute,
	})
	if runErr != nil || result.ExitCode != 0 {
		return executionError("create Python virtual environment", result, runErr)
	}
	return nil
}

func (a *Adapter) Freeze(ctx context.Context, environmentPythonPath string) ([]string, error) {
	executablePath, err := secureExecutablePath(environmentPythonPath)
	if err != nil {
		return nil, err
	}
	environment, _ := localexec.CoreEnvironment(map[string]string{"PYTHONIOENCODING": "utf-8", "PYTHONUTF8": "1", "PIP_DISABLE_PIP_VERSION_CHECK": "1", "NO_COLOR": "1"})
	result, runErr := a.runner.Execute(ctx, localexec.Request{
		Program: executablePath,
		Args:    []string{"-I", "-u", "-X", "utf8", "-m", "pip", "freeze", "--all", "--disable-pip-version-check"},
		Dir:     filepath.Dir(executablePath),
		Env:     environment,
		Timeout: time.Minute,
	})
	if runErr != nil || result.ExitCode != 0 {
		return nil, executionError("freeze Python environment", result, runErr)
	}
	if result.Stdout.Truncated {
		return nil, fmt.Errorf("pip freeze output exceeded the configured limit")
	}
	lines := strings.Split(strings.ReplaceAll(result.Stdout.Text, "\r\n", "\n"), "\n")
	values := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			values = append(values, line)
		}
	}
	sort.Strings(values)
	return values, nil
}

func (a *Adapter) InstallPackages(ctx context.Context, environmentPythonPath string, packages []string) error {
	if len(packages) == 0 {
		return nil
	}
	executablePath, err := secureExecutablePath(environmentPythonPath)
	if err != nil {
		return err
	}
	if len(packages) > 512 {
		return fmt.Errorf("too many Python package specifications")
	}
	args := []string{"-I", "-u", "-X", "utf8", "-m", "pip", "install", "--disable-pip-version-check", "--no-input"}
	args = append(args, packages...)
	environment, _ := localexec.CoreEnvironment(map[string]string{"PYTHONIOENCODING": "utf-8", "PYTHONUTF8": "1", "PIP_DISABLE_PIP_VERSION_CHECK": "1", "PIP_NO_INPUT": "1", "NO_COLOR": "1"})
	result, runErr := a.runner.Execute(ctx, localexec.Request{Program: executablePath, Args: args, Dir: filepath.Dir(executablePath), Env: environment, Timeout: 10 * time.Minute})
	if runErr != nil || result.ExitCode != 0 {
		return executionError("install Python packages", result, runErr)
	}
	if result.Stdout.Truncated || result.Stderr.Truncated {
		return fmt.Errorf("pip install output exceeded the configured limit")
	}
	return nil
}

func secureExecutablePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("Python executable path must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve Python executable: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect Python executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("Python executable is not a regular file")
	}
	// Validate the resolved file, but execute through the selected venv path.
	// On POSIX, venv/bin/python commonly points at the base interpreter; using
	// the resolved path would silently discard the virtual environment.
	return filepath.Clean(path), nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func executionError(action string, result localexec.Result, err error) error {
	detail := strings.TrimSpace(result.Stderr.Text)
	if detail == "" {
		detail = strings.TrimSpace(result.Stdout.Text)
	}
	if len(detail) > 2000 {
		detail = detail[:2000]
	}
	if err != nil && detail != "" {
		return fmt.Errorf("%s: %w: %s", action, err, detail)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s exited with code %d: %s", action, result.ExitCode, detail)
}

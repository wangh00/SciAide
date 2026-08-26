package builtin

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
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/platform/localexec"
	"github.com/wangh00/SciAide/internal/tools/pathguard"
)

const (
	ShellExecuteName  = "builtin.shell.execute"
	PythonExecuteName = "builtin.python.execute"

	defaultProcessTimeout = 30
	maxProcessTimeout     = 300
	maxArtifactPaths      = 32
)

type ShellExecute struct {
	projects ProjectLoader
	runner   *localexec.Runner
}

type PythonExecute struct {
	projects ProjectLoader
	runner   *localexec.Runner
}

func NewShellExecute(projects ProjectLoader, runner *localexec.Runner) *ShellExecute {
	return &ShellExecute{projects: projects, runner: runner}
}

func NewPythonExecute(projects ProjectLoader, runner *localexec.Runner) *PythonExecute {
	return &PythonExecute{projects: projects, runner: runner}
}

func (*ShellExecute) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{
		QualifiedName: ShellExecuteName,
		Description:   "Execute an approved PowerShell or CMD command in the current project Workspace. Network access is allowed by default. The process tree has a 1 GiB memory budget; this is host execution, not a security sandbox. Output is bounded, descendants are terminated when the call ends, and only explicitly declared output files become Artifacts.",
		InputSchema:   json.RawMessage(`{"type":"object","additionalProperties":false,"required":["command"],"properties":{"shell":{"type":"string","enum":["powershell","cmd"]},"command":{"type":"string","minLength":1,"maxLength":131072},"workdir":{"type":"string","maxLength":4096},"timeoutSeconds":{"type":"integer","minimum":1,"maximum":300},"artifactPaths":{"type":"array","maxItems":32,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":4096}}}}`),
		OutputSchema:  processOutputSchema(),
		Risk:          tool.RiskHigh,
		Permissions: []tool.PermissionRequirement{
			{Kind: tool.PermissionWorkspaceRead, Resource: "."},
			{Kind: tool.PermissionWorkspaceWrite, Resource: "."},
			{Kind: tool.PermissionProcessExecute, Resource: "shell"},
		},
		Idempotent: false,
		Version:    "1",
	}, nil
}

func (t *ShellExecute) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if t == nil || t.projects == nil || t.runner == nil {
		return tool.Result{}, fmt.Errorf("Shell execution is not configured")
	}
	var args struct {
		Shell          string   `json:"shell"`
		Command        string   `json:"command"`
		Workdir        string   `json:"workdir"`
		TimeoutSeconds int      `json:"timeoutSeconds"`
		ArtifactPaths  []string `json:"artifactPaths"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	args.Shell = strings.ToLower(strings.TrimSpace(args.Shell))
	if args.Shell == "" {
		args.Shell = "powershell"
	}
	if strings.TrimSpace(args.Command) == "" {
		return tool.Result{}, fmt.Errorf("shell command is required")
	}
	timeout, err := processTimeout(args.TimeoutSeconds)
	if err != nil {
		return tool.Result{}, err
	}
	prepared, err := prepareExecution(ctx, t.projects, invocation.ProjectID, args.Workdir, args.ArtifactPaths)
	if err != nil {
		return tool.Result{}, err
	}
	defer prepared.guard.Close()

	executable, commandArgs, err := resolveShell(args.Shell, args.Command)
	if err != nil {
		return tool.Result{}, err
	}
	executableHash, err := hashRegularFile(executable)
	if err != nil {
		return tool.Result{}, fmt.Errorf("hash shell executable: %w", err)
	}
	executableVersion, err := localexec.ExecutableVersion(executable)
	if err != nil {
		return tool.Result{}, fmt.Errorf("read shell executable version: %w", err)
	}
	environment, environmentNames := localexec.CoreEnvironment(map[string]string{"NO_COLOR": "1"})
	result, runErr := t.runner.Execute(ctx, localexec.Request{
		Program: executable, Args: commandArgs, Dir: prepared.workdir, Env: environment, Timeout: timeout,
		Audit: localexec.Audit{
			CallID: invocation.CallID, RunID: invocation.RunID, ProjectID: invocation.ProjectID, ToolName: ShellExecuteName,
			ExecutablePath: executable, ExecutableVersion: executableVersion, ExecutableSHA256: executableHash, CommandSHA256: hashString(args.Command),
			Workdir: prepared.workdirRelative, TimeoutMillis: timeout.Milliseconds(), EnvironmentNames: environmentNames,
		},
	})
	return buildProcessToolResult(result, runErr, prepared, executable, "")
}

func (*PythonExecute) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{
		QualifiedName: PythonExecuteName,
		Description:   "Execute approved Python code or one Workspace script with an isolated, unbuffered interpreter in the current project Workspace. Network access is allowed by default. The process tree has a 1 GiB memory budget; this is host execution, not a security sandbox. Output is bounded, descendants are terminated when the call ends, and only explicitly declared output files become Artifacts.",
		InputSchema:   json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"code":{"type":"string","minLength":1,"maxLength":131072},"scriptPath":{"type":"string","minLength":1,"maxLength":4096},"args":{"type":"array","maxItems":64,"items":{"type":"string","maxLength":8192}},"workdir":{"type":"string","maxLength":4096},"timeoutSeconds":{"type":"integer","minimum":1,"maximum":300},"artifactPaths":{"type":"array","maxItems":32,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":4096}}}}`),
		OutputSchema:  processOutputSchema(),
		Risk:          tool.RiskHigh,
		Permissions: []tool.PermissionRequirement{
			{Kind: tool.PermissionWorkspaceRead, Resource: "."},
			{Kind: tool.PermissionWorkspaceWrite, Resource: "."},
			{Kind: tool.PermissionProcessExecute, Resource: "python"},
		},
		Idempotent: false,
		Version:    "1",
	}, nil
}

func (t *PythonExecute) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if t == nil || t.projects == nil || t.runner == nil {
		return tool.Result{}, fmt.Errorf("Python execution is not configured")
	}
	var args struct {
		Code           string   `json:"code"`
		ScriptPath     string   `json:"scriptPath"`
		Args           []string `json:"args"`
		Workdir        string   `json:"workdir"`
		TimeoutSeconds int      `json:"timeoutSeconds"`
		ArtifactPaths  []string `json:"artifactPaths"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	hasCode, hasScript := strings.TrimSpace(args.Code) != "", strings.TrimSpace(args.ScriptPath) != ""
	if hasCode == hasScript {
		return tool.Result{}, fmt.Errorf("provide exactly one of code or scriptPath")
	}
	timeout, err := processTimeout(args.TimeoutSeconds)
	if err != nil {
		return tool.Result{}, err
	}
	prepared, err := prepareExecution(ctx, t.projects, invocation.ProjectID, args.Workdir, args.ArtifactPaths)
	if err != nil {
		return tool.Result{}, err
	}
	defer prepared.guard.Close()

	executable, prefixArgs, err := resolvePython()
	if err != nil {
		return tool.Result{}, err
	}
	executableHash, err := hashRegularFile(executable)
	if err != nil {
		return tool.Result{}, fmt.Errorf("hash Python executable: %w", err)
	}
	executableVersion, err := localexec.ExecutableVersion(executable)
	if err != nil {
		return tool.Result{}, fmt.Errorf("read Python executable version: %w", err)
	}
	commandArgs := append(prefixArgs, "-I", "-u", "-X", "utf8")
	scriptAbsolute, scriptRelative, scriptHash := "", "", ""
	commandHash := ""
	if hasCode {
		commandArgs = append(commandArgs, "-c", args.Code)
		commandHash = hashString(args.Code)
	} else {
		if err := rejectPrivateProjectPath(prepared.guard, args.ScriptPath); err != nil {
			return tool.Result{}, err
		}
		file, clean, openErr := prepared.guard.OpenFile(args.ScriptPath)
		if openErr != nil {
			return tool.Result{}, openErr
		}
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			file.Close()
			return tool.Result{}, fmt.Errorf("Python script is not a regular Workspace file")
		}
		digest := sha256.New()
		if _, err := io.Copy(digest, file); err != nil {
			file.Close()
			return tool.Result{}, err
		}
		file.Close()
		scriptHash = hex.EncodeToString(digest.Sum(nil))
		scriptRelative = filepath.ToSlash(clean)
		scriptAbsolute, err = prepared.guard.Absolute(clean)
		if err != nil {
			return tool.Result{}, err
		}
		commandArgs = append(commandArgs, scriptAbsolute)
	}
	commandArgs = append(commandArgs, args.Args...)
	environment, environmentNames := localexec.CoreEnvironment(map[string]string{"PYTHONIOENCODING": "utf-8", "PYTHONUTF8": "1", "NO_COLOR": "1"})
	result, runErr := t.runner.Execute(ctx, localexec.Request{
		Program: executable, Args: commandArgs, Dir: prepared.workdir, Env: environment, Timeout: timeout,
		Audit: localexec.Audit{
			CallID: invocation.CallID, RunID: invocation.RunID, ProjectID: invocation.ProjectID, ToolName: PythonExecuteName,
			ExecutablePath: executable, ExecutableVersion: executableVersion, ExecutableSHA256: executableHash, ScriptPath: scriptRelative, ScriptSHA256: scriptHash,
			CommandSHA256: commandHash, Workdir: prepared.workdirRelative, TimeoutMillis: timeout.Milliseconds(), EnvironmentNames: environmentNames,
		},
	})
	return buildProcessToolResult(result, runErr, prepared, executable, scriptRelative)
}

type preparedExecution struct {
	guard           *pathguard.Guard
	workdir         string
	workdirRelative string
	artifacts       []string
	artifactBefore  map[string]fileSnapshot
}

type fileSnapshot struct {
	exists  bool
	size    int64
	modTime time.Time
	sha256  string
}

func prepareExecution(ctx context.Context, projects ProjectLoader, projectID, workdir string, artifacts []string) (preparedExecution, error) {
	if len(artifacts) > maxArtifactPaths {
		return preparedExecution{}, fmt.Errorf("too many declared Artifact paths")
	}
	selected, err := projects.Get(ctx, strings.TrimSpace(projectID))
	if err != nil {
		return preparedExecution{}, err
	}
	if err := project.VerifyPrivateDataLayout(selected); err != nil {
		return preparedExecution{}, err
	}
	guard, err := pathguard.Open(selected.WorkspacePath)
	if err != nil {
		return preparedExecution{}, err
	}
	fail := func(err error) (preparedExecution, error) {
		guard.Close()
		return preparedExecution{}, err
	}
	if strings.TrimSpace(workdir) == "" {
		workdir = "."
	}
	if err := rejectPrivateProjectPath(guard, workdir); err != nil {
		return fail(err)
	}
	directory, clean, err := guard.OpenFile(workdir)
	if err != nil {
		return fail(err)
	}
	info, err := directory.Stat()
	directory.Close()
	if err != nil || !info.IsDir() {
		return fail(fmt.Errorf("process workdir is not a Workspace directory"))
	}
	abs, err := guard.Absolute(clean)
	if err != nil {
		return fail(err)
	}
	cleanArtifacts := make([]string, 0, len(artifacts))
	artifactBefore := make(map[string]fileSnapshot, len(artifacts))
	seen := map[string]struct{}{}
	for _, value := range artifacts {
		if err := rejectPrivateProjectPath(guard, value); err != nil {
			return fail(err)
		}
		relative, err := guard.Relative(value)
		if err != nil || relative == "." {
			if err == nil {
				err = fmt.Errorf("declared Artifact path must be a file")
			}
			return fail(err)
		}
		key := strings.ToLower(relative)
		if _, exists := seen[key]; exists {
			return fail(fmt.Errorf("declared Artifact paths must be unique"))
		}
		seen[key] = struct{}{}
		slash := filepath.ToSlash(relative)
		before, err := snapshotWorkspaceFile(guard, relative)
		if err != nil {
			return fail(fmt.Errorf("inspect declared Artifact %q: %w", slash, err))
		}
		cleanArtifacts = append(cleanArtifacts, slash)
		artifactBefore[slash] = before
	}
	return preparedExecution{guard: guard, workdir: abs, workdirRelative: filepath.ToSlash(clean), artifacts: cleanArtifacts, artifactBefore: artifactBefore}, nil
}

func processTimeout(seconds int) (time.Duration, error) {
	if seconds == 0 {
		seconds = defaultProcessTimeout
	}
	if seconds < 1 || seconds > maxProcessTimeout {
		return 0, fmt.Errorf("process timeout must be between 1 and %d seconds", maxProcessTimeout)
	}
	return time.Duration(seconds) * time.Second, nil
}

func resolveShell(shell, command string) (string, []string, error) {
	switch shell {
	case "powershell":
		executable, err := resolveExecutable([]string{"pwsh.exe", "powershell.exe"})
		if err != nil {
			return "", nil, fmt.Errorf("PowerShell is unavailable: %w", err)
		}
		utf8Command := "$OutputEncoding = [Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false); " + command
		return executable, []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", utf8Command}, nil
	case "cmd":
		executable, err := resolveExecutable([]string{"cmd.exe"})
		if err != nil {
			return "", nil, fmt.Errorf("CMD is unavailable: %w", err)
		}
		return executable, []string{"/D", "/S", "/C", "chcp 65001>nul & " + command}, nil
	default:
		return "", nil, fmt.Errorf("unsupported shell %q", shell)
	}
}

func resolvePython() (string, []string, error) {
	values := []struct {
		name   string
		prefix []string
	}{{"python.exe", nil}, {"python3.exe", nil}}
	for _, value := range values {
		path, err := exec.LookPath(value.name)
		if err != nil {
			continue
		}
		abs, err := filepath.Abs(path)
		if err == nil {
			return filepath.Clean(abs), append([]string(nil), value.prefix...), nil
		}
	}
	return "", nil, fmt.Errorf("no supported Python 3 interpreter was found on PATH")
}

func resolveExecutable(candidates []string) (string, error) {
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		abs, err := filepath.Abs(path)
		if err == nil {
			return filepath.Clean(abs), nil
		}
	}
	return "", fmt.Errorf("none of %s were found on PATH", strings.Join(candidates, ", "))
}

func hashRegularFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("executable is not a regular file")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func hashString(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func buildProcessToolResult(result localexec.Result, runErr error, prepared preparedExecution, executable, scriptPath string) (tool.Result, error) {
	artifacts := []tool.ArtifactRef{}
	if runErr == nil && result.Reason == localexec.ReasonCompleted && result.ExitCode == 0 {
		for _, relative := range prepared.artifacts {
			after, err := snapshotWorkspaceFile(prepared.guard, relative)
			if err != nil {
				return tool.Result{}, fmt.Errorf("validate declared Artifact %q: %w", relative, err)
			}
			if !after.exists {
				return tool.Result{}, fmt.Errorf("declared Artifact %q is not a regular file", relative)
			}
			before := prepared.artifactBefore[relative]
			if before.exists && before.sha256 == after.sha256 {
				return tool.Result{}, fmt.Errorf("declared Artifact %q was not produced or changed by this execution", relative)
			}
			artifacts = append(artifacts, tool.ArtifactRef{Name: filepath.Base(relative), WorkspacePath: relative})
		}
	}
	payload := struct {
		PID               int                         `json:"pid"`
		ExitCode          int                         `json:"exitCode"`
		TerminationReason localexec.TerminationReason `json:"terminationReason"`
		Stdout            localexec.Stream            `json:"stdout"`
		Stderr            localexec.Stream            `json:"stderr"`
		Executable        string                      `json:"executable"`
		ExecutableVersion string                      `json:"executableVersion"`
		ScriptPath        string                      `json:"scriptPath,omitempty"`
		Workdir           string                      `json:"workdir"`
		StartedAt         time.Time                   `json:"startedAt"`
		FinishedAt        time.Time                   `json:"finishedAt"`
	}{result.PID, result.ExitCode, result.Reason, result.Stdout, result.Stderr, executable, result.ExecutableVersion, scriptPath, prepared.workdirRelative, result.StartedAt, result.FinishedAt}
	structured, err := json.Marshal(payload)
	if err != nil {
		return tool.Result{}, err
	}
	text := renderProcessText(result, runErr)
	status := tool.ResultSuccess
	if result.Reason == localexec.ReasonCancelled || result.Reason == localexec.ReasonAppShutdown {
		status = tool.ResultCancelled
	} else if runErr != nil || result.Reason != localexec.ReasonCompleted || result.ExitCode != 0 {
		status = tool.ResultError
	}
	return tool.Result{Status: status, Text: text, Structured: structured, Artifacts: artifacts, Citations: []tool.CitationRef{}, Truncated: result.Stdout.Truncated || result.Stderr.Truncated}, nil
}

func snapshotWorkspaceFile(guard *pathguard.Guard, relative string) (fileSnapshot, error) {
	abs, err := guard.Absolute(relative)
	if err != nil {
		return fileSnapshot{}, err
	}
	info, err := os.Lstat(abs)
	if os.IsNotExist(err) {
		if err := validateExistingArtifactParent(guard, relative); err != nil {
			return fileSnapshot{}, err
		}
		return fileSnapshot{}, nil
	}
	if err != nil {
		return fileSnapshot{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fileSnapshot{}, fmt.Errorf("path exists but is not a regular file")
	}
	file, _, err := guard.OpenFile(relative)
	if err != nil {
		return fileSnapshot{}, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return fileSnapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return fileSnapshot{}, fmt.Errorf("path exists but is not a regular file")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return fileSnapshot{}, err
	}
	return fileSnapshot{exists: true, size: info.Size(), modTime: info.ModTime(), sha256: hex.EncodeToString(digest.Sum(nil))}, nil
}

func validateExistingArtifactParent(guard *pathguard.Guard, relative string) error {
	parent := filepath.Dir(relative)
	for {
		file, _, err := guard.OpenFile(parent)
		if err == nil {
			defer file.Close()
			info, statErr := file.Stat()
			if statErr != nil || !info.IsDir() {
				return fmt.Errorf("declared Artifact parent is not a Workspace directory")
			}
			return nil
		}
		if !os.IsNotExist(err) {
			return err
		}
		next := filepath.Dir(parent)
		if next == parent {
			return err
		}
		parent = next
	}
}

func renderProcessText(result localexec.Result, runErr error) string {
	var text strings.Builder
	fmt.Fprintf(&text, "Process finished: reason=%s exitCode=%d\n", result.Reason, result.ExitCode)
	if result.Stdout.Text != "" {
		text.WriteString("\nstdout:\n")
		text.WriteString(result.Stdout.Text)
	}
	if result.Stderr.Text != "" {
		text.WriteString("\nstderr:\n")
		text.WriteString(result.Stderr.Text)
	}
	if runErr != nil {
		text.WriteString("\nThe local executor encountered an operational error. Check the local process audit for details.")
	}
	return text.String()
}

func processOutputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"required":["pid","exitCode","terminationReason","stdout","stderr","executable","executableVersion","workdir","startedAt","finishedAt"],"properties":{"pid":{"type":"integer"},"exitCode":{"type":"integer"},"terminationReason":{"type":"string","enum":["completed","exit_nonzero","timed_out","cancelled","app_shutdown","start_failed"]},"stdout":{"type":"object","additionalProperties":false,"required":["text","bytes","sha256","truncated"],"properties":{"text":{"type":"string"},"bytes":{"type":"integer","minimum":0},"sha256":{"type":"string","pattern":"^[0-9a-f]{64}$"},"truncated":{"type":"boolean"}}},"stderr":{"type":"object","additionalProperties":false,"required":["text","bytes","sha256","truncated"],"properties":{"text":{"type":"string"},"bytes":{"type":"integer","minimum":0},"sha256":{"type":"string","pattern":"^[0-9a-f]{64}$"},"truncated":{"type":"boolean"}}},"executable":{"type":"string"},"executableVersion":{"type":"string"},"scriptPath":{"type":"string"},"workdir":{"type":"string"},"startedAt":{"type":"string"},"finishedAt":{"type":"string"}}}`)
}

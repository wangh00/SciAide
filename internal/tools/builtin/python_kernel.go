package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/pythonenv"
	"github.com/wangh00/SciAide/internal/app/tool"
)

const (
	PythonKernelExecuteName = "builtin.python.kernel.execute"
	PythonKernelManageName  = "builtin.python.kernel.manage"
)

type PythonKernelExecute struct{ kernels *pythonenv.KernelService }
type PythonKernelManage struct{ kernels *pythonenv.KernelService }

func NewPythonKernelExecute(kernels *pythonenv.KernelService) *PythonKernelExecute {
	return &PythonKernelExecute{kernels: kernels}
}

func NewPythonKernelManage(kernels *pythonenv.KernelService) *PythonKernelManage {
	return &PythonKernelManage{kernels: kernels}
}

func (*PythonKernelExecute) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{
		QualifiedName: PythonKernelExecuteName,
		Description:   "Execute Python code in the current project's persistent, serial Kernel. Variables and imports persist until restart, idle reclamation, cancellation, timeout or environment change. Declared input files and structured input data are hashed; declared new outputs and Matplotlib figures become research Artifacts. Output paths may use {{runId}} and {{attempt}} placeholders. Network access is allowed by default. The process tree has a 1 GiB memory budget, but this is host execution, not a security sandbox.",
		InputSchema:   json.RawMessage(`{"type":"object","additionalProperties":false,"required":["code"],"properties":{"code":{"type":"string","minLength":1,"maxLength":131072},"inputPaths":{"type":"array","maxItems":64,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":4096}},"inputData":{},"expectedEnvironmentFingerprint":{"type":"string","pattern":"^[0-9a-f]{64}$"},"outputPaths":{"type":"array","maxItems":32,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":4096}},"timeoutSeconds":{"type":"integer","minimum":1,"maximum":300}}}`),
		OutputSchema:  kernelOutputSchema(),
		Risk:          tool.RiskHigh,
		Permissions: []tool.PermissionRequirement{
			{Kind: tool.PermissionWorkspaceRead, Resource: "."},
			{Kind: tool.PermissionWorkspaceWrite, Resource: "."},
			{Kind: tool.PermissionProcessExecute, Resource: "project-python-kernel"},
		},
		Idempotent: false,
		Version:    "1",
	}, nil
}

func (t *PythonKernelExecute) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if t == nil || t.kernels == nil {
		return tool.Result{}, fmt.Errorf("Python Kernel is not configured")
	}
	var args struct {
		Code                           string          `json:"code"`
		InputPaths                     []string        `json:"inputPaths"`
		InputData                      json.RawMessage `json:"inputData"`
		ExpectedEnvironmentFingerprint string          `json:"expectedEnvironmentFingerprint"`
		OutputPaths                    []string        `json:"outputPaths"`
		TimeoutSeconds                 int             `json:"timeoutSeconds"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if args.TimeoutSeconds == 0 {
		args.TimeoutSeconds = 60
	}
	for index := range args.OutputPaths {
		args.OutputPaths[index] = expandWorkflowPath(args.OutputPaths[index], invocation)
	}
	if expected := strings.TrimSpace(args.ExpectedEnvironmentFingerprint); expected != "" {
		environment, err := t.kernels.Environment(ctx, invocation.ProjectID)
		if err != nil {
			return tool.Result{}, err
		}
		if environment.EnvironmentFingerprint != expected {
			return tool.Result{}, fmt.Errorf("project Python environment fingerprint changed after Workflow validation")
		}
	}
	workspaceRoot := invocation.WorkspaceRoot
	if invocation.ResearchTaskID != "" {
		selected, err := t.kernels.Project(ctx, invocation.ProjectID)
		if err != nil {
			return tool.Result{}, err
		}
		taskRoot, err := project.ResearchTaskWorkspacePath(selected, invocation.ResearchTaskID)
		if err != nil {
			return tool.Result{}, err
		}
		// Task-scoped execution must never honor a project-root fallback or a
		// caller-supplied sibling path. Resolve the canonical task root here,
		// immediately before starting the persistent Kernel.
		workspaceRoot = taskRoot
		if err := os.MkdirAll(workspaceRoot, 0o700); err != nil {
			return tool.Result{}, err
		}
		for _, inputPath := range args.InputPaths {
			absolute := filepath.Join(workspaceRoot, filepath.FromSlash(inputPath))
			info, statErr := os.Stat(absolute)
			if statErr != nil {
				if os.IsNotExist(statErr) {
					return tool.Result{}, tool.NewUserFacingError(fmt.Sprintf("当前任务输入文件不存在：%s。请重新选择该任务的冻结输入后再试。", filepath.ToSlash(inputPath)))
				}
				return tool.Result{}, fmt.Errorf("task workspace input %q is unavailable at %s: %w", inputPath, workspaceRoot, statErr)
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return tool.Result{}, tool.NewUserFacingError(fmt.Sprintf("当前任务输入路径不是普通文件：%s。请重新选择该任务的冻结输入后再试。", filepath.ToSlash(inputPath)))
			}
		}
	}
	result, runErr := t.kernels.ExecuteTool(ctx, pythonenv.KernelExecuteRequest{
		ProjectID: invocation.ProjectID, WorkspacePath: workspaceRoot, ToolCallID: invocation.CallID, Code: args.Code, InputPaths: args.InputPaths, InputData: args.InputData, OutputPaths: args.OutputPaths,
		Timeout: time.Duration(args.TimeoutSeconds) * time.Second,
	}, invocation.CallID, invocation.RunID)
	structured, err := json.Marshal(result)
	if err != nil {
		return tool.Result{}, err
	}
	artifacts := make([]tool.ArtifactRef, 0, len(result.OutputSHA256))
	paths := make([]string, 0, len(result.OutputSHA256))
	for path := range result.OutputSHA256 {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		artifacts = append(artifacts, tool.ArtifactRef{Name: filepath.Base(path), WorkspacePath: path, SHA256: result.OutputSHA256[path]})
	}
	status := tool.ResultError
	if result.Status == "success" {
		status = tool.ResultSuccess
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return tool.Result{}, ctx.Err()
		}
		if message, safe := pythonKernelUserFacingError(runErr); safe {
			return tool.Result{}, tool.NewUserFacingError(message)
		}
		return tool.Result{}, runErr
	}
	return tool.Result{Status: status, Text: renderKernelResult(result), Structured: structured, Artifacts: artifacts, Citations: []tool.CitationRef{}, Truncated: result.StdoutTruncated || result.StderrTruncated}, nil
}

func pythonKernelUserFacingError(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "openblas error: memory allocation"):
		return "Python 数值库初始化超出 1 GiB 内存预算；SciAide 已停止本次分析。", true
	case strings.Contains(message, "process-tree memory budget"):
		return "Python 分析超出 1 GiB 内存预算；请降低数据规模或方法的内存占用后重试。", true
	case strings.Contains(message, "output exceeded the 64 kib per-stream limit"):
		return "Python 分析输出超过 64 KiB 限制；请减少逐行打印并把完整结果写入声明的产物文件。", true
	default:
		return "", false
	}
}

func expandWorkflowPath(value string, invocation tool.Invocation) string {
	attempt := "1"
	if parts := strings.Split(strings.TrimSpace(invocation.ProviderCallID), ":"); len(parts) > 0 {
		if candidate := strings.TrimSpace(parts[len(parts)-1]); candidate != "" {
			attempt = candidate
		}
	}
	return strings.ReplaceAll(strings.ReplaceAll(value, "{{runId}}", invocation.RunID), "{{attempt}}", attempt)
}

func (*PythonKernelManage) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{
		QualifiedName: PythonKernelManageName,
		Description:   "Stop or restart the current project's persistent Python Kernel, clearing all in-memory variables and imports. The next execution starts a clean Kernel in the same project environment.",
		InputSchema:   json.RawMessage(`{"type":"object","additionalProperties":false,"required":["action"],"properties":{"action":{"type":"string","enum":["stop","restart"]}}}`),
		OutputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["action","state"],"properties":{"action":{"type":"string"},"state":{"type":"string","enum":["stopped"]}}}`),
		Risk:          tool.RiskModerate,
		Permissions:   []tool.PermissionRequirement{{Kind: tool.PermissionProcessExecute, Resource: "project-python-kernel"}},
		Idempotent:    true,
		Version:       "1",
	}, nil
}

func (t *PythonKernelManage) Invoke(_ context.Context, invocation tool.Invocation) (tool.Result, error) {
	if t == nil || t.kernels == nil {
		return tool.Result{}, fmt.Errorf("Python Kernel is not configured")
	}
	var args struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	action := strings.ToLower(strings.TrimSpace(args.Action))
	var err error
	if action == "restart" {
		err = t.kernels.Restart(invocation.ProjectID)
	} else if action == "stop" {
		err = t.kernels.Stop(invocation.ProjectID)
	} else {
		return tool.Result{}, fmt.Errorf("unsupported Kernel action")
	}
	if err != nil {
		return tool.Result{}, err
	}
	structured, _ := json.Marshal(map[string]string{"action": action, "state": "stopped"})
	return tool.Result{Status: tool.ResultSuccess, Text: "Project Python Kernel stopped; in-memory state was cleared.", Structured: structured, Artifacts: []tool.ArtifactRef{}, Citations: []tool.CitationRef{}}, nil
}

func renderKernelResult(result pythonenv.KernelResult) string {
	var text strings.Builder
	fmt.Fprintf(&text, "Python Kernel execution %d finished with status=%s", result.Sequence, result.Status)
	if result.Stdout != "" {
		text.WriteString("\n\nstdout:\n")
		text.WriteString(result.Stdout)
	}
	if result.Stderr != "" {
		text.WriteString("\n\nstderr:\n")
		text.WriteString(result.Stderr)
	}
	if result.Exception != nil {
		fmt.Fprintf(&text, "\n\n%s: %s\n%s", result.Exception.Type, result.Exception.Message, result.Exception.Traceback)
	} else if result.ValueType != "" {
		fmt.Fprintf(&text, "\n\nresult (%s): %v", result.ValueType, result.Value)
	}
	if result.Table != nil {
		fmt.Fprintf(&text, "\n\ntable: %d rows, %d columns (preview limited to %d rows)", result.Table.Total, len(result.Table.Columns), len(result.Table.Rows))
	}
	if len(result.Images) > 0 {
		fmt.Fprintf(&text, "\n\nfigures: %s", strings.Join(result.Images, ", "))
	}
	return text.String()
}

func kernelOutputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["kernelId","executionId","sequence","status","stdout","stderr","stdoutTruncated","stderrTruncated","images","codeSha256","inputSha256","outputSha256","environmentFingerprint","reproductionSha256","startedAt","finishedAt"],"properties":{"kernelId":{"type":"string"},"executionId":{"type":"string"},"sequence":{"type":"integer","minimum":1},"status":{"type":"string","enum":["success","error","failed"]},"stdout":{"type":"string"},"stderr":{"type":"string"},"stdoutTruncated":{"type":"boolean"},"stderrTruncated":{"type":"boolean"},"valueType":{"type":"string"},"images":{"type":"array","items":{"type":"string"}},"codeSha256":{"type":"string","pattern":"^[0-9a-f]{64}$"},"inputSha256":{"type":"object"},"outputSha256":{"type":"object"},"environmentFingerprint":{"type":"string"},"reproductionSha256":{"type":"string","pattern":"^[0-9a-f]{64}$"},"startedAt":{"type":"string"},"finishedAt":{"type":"string"}}}`)
}

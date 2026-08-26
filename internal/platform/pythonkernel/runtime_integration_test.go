package pythonkernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/pythonenv"
	"github.com/wangh00/SciAide/internal/platform/localexec"
)

func TestRuntimePersistsStateAndResetsAfterTimeout(t *testing.T) {
	name := "python"
	if runtime.GOOS != "windows" {
		name = "python3"
	}
	python, err := exec.LookPath(name)
	if err != nil {
		t.Skip("Python 3 is not installed")
	}
	python, err = filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	runner := localexec.NewRunner(localexec.Options{})
	kernels := New(runner)
	defer func() { _ = kernels.Close(); _ = runner.Close() }()
	workspace := t.TempDir()
	environment := pythonenv.Environment{EnvironmentPythonPath: python, EnvironmentFingerprint: "fixture-environment"}
	request := pythonenv.KernelExecuteRequest{ProjectID: "project", Environment: environment, Timeout: 10 * time.Second}

	request.Code = `value = 41
print("first")
[{"name": "sample", "value": value}]`
	first, err := kernels.Execute(context.Background(), request, workspace)
	if err != nil || first.Status != "success" || first.Sequence != 1 || !strings.Contains(first.Stdout, "first") || first.Table == nil || first.Table.Total != 1 {
		t.Fatalf("first execution = %#v, %v", first, err)
	}
	request.Code = "value + 1"
	second, err := kernels.Execute(context.Background(), request, workspace)
	if err != nil || second.KernelID != first.KernelID || second.Sequence != 2 || second.Value != float64(42) {
		t.Fatalf("persistent execution = %#v, %v", second, err)
	}
	request.Code = "raise ValueError('structured failure')"
	failed, err := kernels.Execute(context.Background(), request, workspace)
	if err != nil || failed.Status != "error" || failed.Exception == nil || failed.Exception.Type != "ValueError" || !strings.Contains(failed.Exception.Traceback, "structured failure") {
		t.Fatalf("exception execution = %#v, %v", failed, err)
	}
	request.Code, request.Timeout = "import time; time.sleep(30)", 100*time.Millisecond
	if _, err := kernels.Execute(context.Background(), request, workspace); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
	request.Code, request.Timeout = "'value' in globals()", 10*time.Second
	reset, err := kernels.Execute(context.Background(), request, workspace)
	if err != nil || reset.KernelID == first.KernelID || reset.Sequence != 1 || reset.Value != false {
		t.Fatalf("reset execution = %#v, %v", reset, err)
	}
}

func TestRuntimeResetsAfterCancellation(t *testing.T) {
	name := "python"
	if runtime.GOOS != "windows" {
		name = "python3"
	}
	python, err := exec.LookPath(name)
	if err != nil {
		t.Skip("Python 3 is not installed")
	}
	python, _ = filepath.Abs(python)
	runner := localexec.NewRunner(localexec.Options{})
	kernels := New(runner)
	defer func() { _ = kernels.Close(); _ = runner.Close() }()
	workspace := t.TempDir()
	request := pythonenv.KernelExecuteRequest{
		ProjectID: "cancel-project", Environment: pythonenv.Environment{EnvironmentPythonPath: python, EnvironmentFingerprint: "cancel-fixture"},
		Timeout: 10 * time.Second, Code: "cancel_fixture = 1\nimport time; time.sleep(30)",
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	cancelled, err := kernels.Execute(ctx, request, workspace)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %#v, %v", cancelled, err)
	}
	request.Code = "'cancel_fixture' in globals()"
	clean, err := kernels.Execute(context.Background(), request, workspace)
	if err != nil || clean.Status != "success" || clean.Value != false || clean.KernelID == cancelled.KernelID {
		t.Fatalf("post-cancellation Kernel = %#v, %v; cancelled=%#v", clean, err, cancelled)
	}
}

func TestRuntimeProducesDeclaredWorkspaceFile(t *testing.T) {
	name := "python"
	if runtime.GOOS != "windows" {
		name = "python3"
	}
	python, err := exec.LookPath(name)
	if err != nil {
		t.Skip("Python 3 is not installed")
	}
	python, _ = filepath.Abs(python)
	runner := localexec.NewRunner(localexec.Options{})
	kernels := New(runner)
	defer func() { _ = kernels.Close(); _ = runner.Close() }()
	workspace := t.TempDir()
	request := pythonenv.KernelExecuteRequest{
		ProjectID: "project", Environment: pythonenv.Environment{EnvironmentPythonPath: python, EnvironmentFingerprint: "fixture"},
		Timeout: 10 * time.Second, OutputPaths: []string{"analysis-output/result.csv"},
		Code: `import os
os.makedirs("analysis-output", exist_ok=True)
open("analysis-output/result.csv", "w", encoding="utf-8").write("x,y\n1,2\n")`,
	}
	result, err := kernels.Execute(context.Background(), request, workspace)
	if err != nil || result.Status != "success" {
		t.Fatalf("execute = %#v, %v", result, err)
	}
	contents, err := os.ReadFile(filepath.Join(workspace, "analysis-output", "result.csv"))
	if err != nil || strings.ReplaceAll(string(contents), "\r\n", "\n") != "x,y\n1,2\n" {
		t.Fatalf("output = %q, %v", contents, err)
	}
}

func TestRuntimeCanUseNetworkWithoutDomainConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte("kernel-network-ok"))
	}))
	defer server.Close()
	name := "python"
	if runtime.GOOS != "windows" {
		name = "python3"
	}
	python, err := exec.LookPath(name)
	if err != nil {
		t.Skip("Python 3 is not installed")
	}
	python, _ = filepath.Abs(python)
	runner := localexec.NewRunner(localexec.Options{})
	kernels := New(runner)
	defer func() { _ = kernels.Close(); _ = runner.Close() }()
	request := pythonenv.KernelExecuteRequest{
		ProjectID: "network-project", Environment: pythonenv.Environment{EnvironmentPythonPath: python, EnvironmentFingerprint: "network-fixture"},
		Timeout: 10 * time.Second,
		Code:    fmt.Sprintf("import urllib.request\nurllib.request.urlopen(%q, timeout=5).read().decode('utf-8')", server.URL),
	}
	result, err := kernels.Execute(context.Background(), request, t.TempDir())
	if err != nil || result.Status != "success" || result.Value != "kernel-network-ok" {
		t.Fatalf("networked Kernel result = %#v, %v", result, err)
	}
	server.Close()
	request.Code = fmt.Sprintf("import urllib.request\nurllib.request.urlopen(%q, timeout=1).read()", server.URL)
	failed, err := kernels.Execute(context.Background(), request, t.TempDir())
	if err != nil || failed.Status != "error" || failed.Exception == nil || failed.Exception.Type == "" || failed.Exception.Message == "" {
		t.Fatalf("diagnosable Kernel network failure = %#v, %v", failed, err)
	}
}

func TestRuntimeProvidesStructuredInputWithoutWritingPrivateFiles(t *testing.T) {
	name := "python"
	if runtime.GOOS != "windows" {
		name = "python3"
	}
	python, err := exec.LookPath(name)
	if err != nil {
		t.Skip("Python 3 is not installed")
	}
	python, _ = filepath.Abs(python)
	runner := localexec.NewRunner(localexec.Options{})
	kernels := New(runner)
	defer func() { _ = kernels.Close(); _ = runner.Close() }()
	request := pythonenv.KernelExecuteRequest{
		ProjectID: "project", Environment: pythonenv.Environment{EnvironmentPythonPath: python, EnvironmentFingerprint: "fixture"},
		Timeout: 10 * time.Second, InputData: json.RawMessage(`{"citations":[{"quote":"verified"}]}`),
		Code: `SCIAIDE_DATA["citations"][0]["quote"]`,
	}
	result, err := kernels.Execute(context.Background(), request, t.TempDir())
	if err != nil || result.Status != "success" || result.Value != "verified" {
		t.Fatalf("structured input = %#v, %v", result, err)
	}
}

func TestRuntimeSerializesConcurrentStartsAndStopsAfterOutputLimit(t *testing.T) {
	name := "python"
	if runtime.GOOS != "windows" {
		name = "python3"
	}
	python, err := exec.LookPath(name)
	if err != nil {
		t.Skip("Python 3 is not installed")
	}
	python, _ = filepath.Abs(python)
	runner := localexec.NewRunner(localexec.Options{})
	kernels := New(runner)
	defer func() { _ = kernels.Close(); _ = runner.Close() }()
	request := pythonenv.KernelExecuteRequest{ProjectID: "project", Environment: pythonenv.Environment{EnvironmentPythonPath: python, EnvironmentFingerprint: "fixture"}, Timeout: 10 * time.Second}
	workspace := t.TempDir()
	type response struct {
		result pythonenv.KernelResult
		err    error
	}
	start := make(chan struct{})
	responses := make(chan response, 2)
	for index := 0; index < 2; index++ {
		go func(value int) {
			<-start
			copy := request
			copy.Code = "value = " + string(rune('1'+value)) + "; value"
			result, err := kernels.Execute(context.Background(), copy, workspace)
			responses <- response{result, err}
		}(index)
	}
	close(start)
	first, second := <-responses, <-responses
	if first.err != nil || second.err != nil || first.result.KernelID != second.result.KernelID {
		t.Fatalf("concurrent executions = %#v / %#v", first, second)
	}
	if first.result.Sequence+second.result.Sequence != 3 {
		t.Fatalf("sequences = %d, %d", first.result.Sequence, second.result.Sequence)
	}

	request.Code = "print('x' * 70000)"
	overflow, err := kernels.Execute(context.Background(), request, workspace)
	if err == nil || !overflow.StdoutTruncated || !strings.Contains(err.Error(), "64 KiB") {
		t.Fatalf("overflow = %#v, %v", overflow, err)
	}
	request.Code = "'value' in globals()"
	reset, err := kernels.Execute(context.Background(), request, workspace)
	if err != nil || reset.KernelID == first.result.KernelID || reset.Value != false {
		t.Fatalf("post-overflow reset = %#v, %v", reset, err)
	}
}

func TestRuntimeStopsKernelAtProcessTreeMemoryBudget(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows Job Object memory budget test")
	}
	python, err := exec.LookPath("python")
	if err != nil {
		t.Skip("Python 3 is not installed")
	}
	python, _ = filepath.Abs(python)
	runner := localexec.NewRunner(localexec.Options{})
	kernels := NewWithOptions(runner, Options{MemoryLimitBytes: 64 * 1024 * 1024})
	defer func() { _ = kernels.Close(); _ = runner.Close() }()
	request := pythonenv.KernelExecuteRequest{
		ProjectID: "memory-project", Environment: pythonenv.Environment{EnvironmentPythonPath: python, EnvironmentFingerprint: "memory-fixture"},
		Timeout: 15 * time.Second, Code: "memory_fixture = bytearray(256 * 1024 * 1024)",
	}
	failed, runErr := kernels.Execute(context.Background(), request, t.TempDir())
	if runErr == nil && (failed.Status != "error" || failed.Exception == nil || failed.Exception.Type != "MemoryError") {
		t.Fatalf("memory-budget execution = %#v, %v", failed, runErr)
	}
	request.Code = "'memory_fixture' in globals()"
	clean, err := kernels.Execute(context.Background(), request, t.TempDir())
	if err != nil || clean.Status != "success" || clean.Value != false || clean.KernelID == failed.KernelID {
		t.Fatalf("post-memory-budget Kernel = %#v, %v; failed=%#v", clean, err, failed)
	}
}

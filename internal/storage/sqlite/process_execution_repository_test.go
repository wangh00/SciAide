package sqlite

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/platform/localexec"
)

func TestProcessExecutionRepositoryPersistsLifecycle(t *testing.T) {
	store, run := createToolFixture(t)
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	call := tool.Call{ID: "process-call", RunID: run.ID, ProviderCallID: "provider", ToolName: "builtin.python.execute", ToolVersion: "1", Arguments: json.RawMessage(`{"code":"print(1)"}`), Status: tool.CallRunning, Risk: tool.RiskHigh, Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionProcessExecute, Resource: "python"}}, CreatedAt: now, UpdatedAt: now}
	if err := NewToolRepository(store.DB()).Create(ctx, call); err != nil {
		t.Fatal(err)
	}
	repository := NewProcessExecutionRepository(store.DB())
	projectID, err := NewRunRepository(store.DB()).ProjectIDForRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	audit := localexec.Audit{CallID: call.ID, RunID: run.ID, ProjectID: projectID, ToolName: call.ToolName, ExecutablePath: "C:/Python/python.exe", ExecutableVersion: "3.13.1.0", ExecutableSHA256: hash64("a"), CommandSHA256: hash64("b"), Workdir: ".", TimeoutMillis: 30_000, EnvironmentNames: []string{"PATH", "TEMP"}}
	if err := repository.PrepareProcessExecution(ctx, audit, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkProcessExecutionStarted(ctx, call.ID, 1234, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	exit := 0
	if err := repository.FinishProcessExecution(ctx, call.ID, localexec.AuditOutcome{PID: 1234, ExitCode: &exit, Reason: localexec.ReasonCompleted, StdoutBytes: 2, StdoutSHA256: hash64("c"), StderrSHA256: hash64("d"), CompletedAt: now.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	var state, reason, environment string
	var pid, exitCode int
	if err := store.DB().QueryRowContext(ctx, `SELECT state,termination_reason,process_id,exit_code,environment_names_json FROM process_execution_audits WHERE tool_call_id=?`, call.ID).Scan(&state, &reason, &pid, &exitCode, &environment); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || reason != "completed" || pid != 1234 || exitCode != 0 || environment != `["PATH","TEMP"]` {
		t.Fatalf("audit state=%s reason=%s pid=%d exit=%d env=%s", state, reason, pid, exitCode, environment)
	}
}

func TestProcessExecutionRepositoryInterruptsStaleAudit(t *testing.T) {
	store, run := createToolFixture(t)
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	call := tool.Call{ID: "stale-process-call", RunID: run.ID, ProviderCallID: "provider-stale", ToolName: "builtin.shell.execute", ToolVersion: "1", Arguments: json.RawMessage(`{"command":"timeout"}`), Status: tool.CallRunning, Risk: tool.RiskHigh, Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionProcessExecute, Resource: "shell"}}, CreatedAt: now, UpdatedAt: now}
	if err := NewToolRepository(store.DB()).Create(ctx, call); err != nil {
		t.Fatal(err)
	}
	projectID, _ := NewRunRepository(store.DB()).ProjectIDForRun(ctx, run.ID)
	repository := NewProcessExecutionRepository(store.DB())
	if err := repository.PrepareProcessExecution(ctx, localexec.Audit{CallID: call.ID, RunID: run.ID, ProjectID: projectID, ToolName: call.ToolName, ExecutablePath: "C:/Windows/System32/cmd.exe", ExecutableVersion: "10.0.0.0", ExecutableSHA256: hash64("a"), CommandSHA256: hash64("b"), Workdir: ".", TimeoutMillis: 30_000, EnvironmentNames: []string{}}, now); err != nil {
		t.Fatal(err)
	}
	if count, err := repository.InterruptActive(ctx, now.Add(time.Second)); err != nil || count != 1 {
		t.Fatalf("InterruptActive() = %d, %v", count, err)
	}
	var state, reason string
	if err := store.DB().QueryRowContext(ctx, `SELECT state,termination_reason FROM process_execution_audits WHERE tool_call_id=?`, call.ID).Scan(&state, &reason); err != nil || state != "failed" || reason != "app_shutdown" {
		t.Fatalf("stale audit state=%q reason=%q err=%v", state, reason, err)
	}
}

func hash64(value string) string {
	result := ""
	for len(result) < 64 {
		result += value
	}
	return result[:64]
}

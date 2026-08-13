package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/events"
)

func TestPermissionRepositoryPersistsApprovalGrantAndAuditEvents(t *testing.T) {
	ctx := context.Background()
	store, run := createToolFixture(t)
	defer store.Close()
	toolRepository := NewToolRepository(store.DB())
	permissionRepository := NewPermissionRepository(store.DB())
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	call := permissionCall(run.ID, "call")
	if err := toolRepository.Create(ctx, call); err != nil {
		t.Fatal(err)
	}
	approval := permission.Approval{ID: "approval", RunID: run.ID, ToolCallID: call.ID, ProjectID: projectIDForRun(t, store, run.ID), ToolName: call.ToolName, ToolVersion: call.ToolVersion, PermissionKind: tool.PermissionWorkspaceRead, Resource: "paper.md", Risk: tool.RiskModerate, Status: permission.ApprovalPending, RequestedScope: permission.ScopeProject, Reason: "test", CreatedAt: now}
	if err := permissionRepository.CreateApprovalWithEvent(ctx, approval, permissionEvent(run.ID, "approval.requested")); err != nil {
		t.Fatal(err)
	}
	loaded, err := permissionRepository.GetApproval(ctx, approval.ID)
	if err != nil || loaded.Status != permission.ApprovalPending || loaded.ResolvedAt != nil {
		t.Fatalf("GetApproval() = %#v, %v", loaded, err)
	}
	grant := permission.Grant{ID: "grant", ProjectID: approval.ProjectID, ToolName: call.ToolName, PermissionKind: approval.PermissionKind, Resource: approval.Resource, Scope: permission.ScopeProject, GrantedBy: permission.SubjectUser, CreatedAt: now.Add(time.Second)}
	if err := permissionRepository.ResolveApprovalWithGrantAndEvent(ctx, approval.ID, permission.ApprovalPending, permission.ApprovalGranted, permission.ScopeProject, &grant, grant.CreatedAt, permissionEvent(run.ID, "approval.granted")); err != nil {
		t.Fatal(err)
	}
	if err := permissionRepository.ResolveApprovalWithGrantAndEvent(ctx, approval.ID, permission.ApprovalPending, permission.ApprovalDenied, permission.ScopeCall, nil, now.Add(2*time.Second), permissionEvent(run.ID, "approval.denied")); !errors.Is(err, permission.ErrApprovalConflict) {
		t.Fatalf("stale resolve error = %v", err)
	}
	active, err := permissionRepository.ListActiveGrants(ctx, approval.ProjectID, run.ID, call.ToolName, now.Add(2*time.Second))
	if err != nil || len(active) != 1 || active[0].ID != grant.ID {
		t.Fatalf("ListActiveGrants() = %#v, %v", active, err)
	}
	var eventCount int
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM run_events WHERE aggregate_id=? AND event_type LIKE 'approval.%'`, run.ID).Scan(&eventCount); err != nil || eventCount != 2 {
		t.Fatalf("approval event count = %d, %v", eventCount, err)
	}
	if err := permissionRepository.RevokeGrantWithEvent(ctx, grant.ID, now.Add(3*time.Second), permissionEvent(run.ID, "permission.grant_revoked")); err != nil {
		t.Fatal(err)
	}
	active, err = permissionRepository.ListActiveGrants(ctx, approval.ProjectID, run.ID, call.ToolName, now.Add(4*time.Second))
	if err != nil || len(active) != 0 {
		t.Fatalf("revoked active grants = %#v, %v", active, err)
	}
	all, err := permissionRepository.ListGrantsByProject(ctx, approval.ProjectID)
	if err != nil || len(all) != 1 || all[0].RevokedAt == nil {
		t.Fatalf("ListGrantsByProject() = %#v, %v", all, err)
	}
}

func TestPermissionRepositoryEnforcesPendingAndActiveGrantUniqueness(t *testing.T) {
	ctx := context.Background()
	store, run := createToolFixture(t)
	defer store.Close()
	toolRepository := NewToolRepository(store.DB())
	repository := NewPermissionRepository(store.DB())
	now := time.Now().UTC()
	call := permissionCall(run.ID, "unique-call")
	if err := toolRepository.Create(ctx, call); err != nil {
		t.Fatal(err)
	}
	projectID := projectIDForRun(t, store, run.ID)
	first := permission.Approval{ID: "approval-1", RunID: run.ID, ToolCallID: call.ID, ProjectID: projectID, ToolName: call.ToolName, ToolVersion: call.ToolVersion, PermissionKind: tool.PermissionToolInvoke, Resource: call.ToolName, Risk: call.Risk, Status: permission.ApprovalPending, RequestedScope: permission.ScopeProject, CreatedAt: now}
	if err := repository.CreateApprovalWithEvent(ctx, first, permissionEvent(run.ID, "approval.requested")); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID, second.PermissionKind, second.Resource = "approval-2", tool.PermissionWorkspaceRead, "paper.md"
	if err := repository.CreateApprovalWithEvent(ctx, second, permissionEvent(run.ID, "approval.requested")); err == nil {
		t.Fatal("second pending approval for the same call was accepted")
	}
	grant := permission.Grant{ID: "grant-1", ProjectID: projectID, ToolName: call.ToolName, PermissionKind: first.PermissionKind, Resource: first.Resource, Scope: permission.ScopeProject, GrantedBy: permission.SubjectUser, CreatedAt: now.Add(time.Second)}
	if err := repository.ResolveApprovalWithGrantAndEvent(ctx, first.ID, permission.ApprovalPending, permission.ApprovalGranted, permission.ScopeProject, &grant, grant.CreatedAt, permissionEvent(run.ID, "approval.granted")); err != nil {
		t.Fatal(err)
	}
	third := second
	third.CreatedAt = now.Add(2 * time.Second)
	if err := repository.CreateApprovalWithEvent(ctx, third, permissionEvent(run.ID, "approval.requested")); err != nil {
		t.Fatalf("new permission after prior resolution rejected: %v", err)
	}
	duplicate := permission.Grant{ID: "grant-2", ProjectID: projectID, ToolName: call.ToolName, PermissionKind: grant.PermissionKind, Resource: grant.Resource, Scope: permission.ScopeProject, GrantedBy: permission.SubjectUser, CreatedAt: now.Add(3 * time.Second)}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO permission_grants(id,project_id,run_id,tool_name,permission_kind,resource,scope,granted_by,created_at) VALUES (?,?,?,?,?,?,?,?,?)`, duplicate.ID, duplicate.ProjectID, nil, duplicate.ToolName, duplicate.PermissionKind, duplicate.Resource, duplicate.Scope, duplicate.GrantedBy, formatTime(duplicate.CreatedAt)); err == nil {
		t.Fatal("duplicate active permission grant was accepted")
	}
}

func TestPermissionScopesExpiryRecoveryAndCascade(t *testing.T) {
	ctx := context.Background()
	store, run := createToolFixture(t)
	defer store.Close()
	toolRepository := NewToolRepository(store.DB())
	repository := NewPermissionRepository(store.DB())
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	projectID := projectIDForRun(t, store, run.ID)
	call := permissionCall(run.ID, "scope-call")
	if err := toolRepository.Create(ctx, call); err != nil {
		t.Fatal(err)
	}
	approval := permission.Approval{ID: "pending", RunID: run.ID, ToolCallID: call.ID, ProjectID: projectID, ToolName: call.ToolName, ToolVersion: call.ToolVersion, PermissionKind: tool.PermissionWorkspaceRead, Resource: "paper.md", Risk: call.Risk, Status: permission.ApprovalPending, RequestedScope: permission.ScopeProject, CreatedAt: now}
	if err := repository.CreateApprovalWithEvent(ctx, approval, permissionEvent(run.ID, "approval.requested")); err != nil {
		t.Fatal(err)
	}
	if count, err := repository.ExpirePending(ctx, now.Add(time.Second)); err != nil || count != 1 {
		t.Fatalf("ExpirePending() = %d, %v", count, err)
	}
	expired, err := repository.GetApproval(ctx, approval.ID)
	if err != nil || expired.Status != permission.ApprovalExpired || expired.ResolvedAt == nil {
		t.Fatalf("expired approval = %#v, %v", expired, err)
	}
	var expiredEvents int
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM run_events WHERE aggregate_id=? AND event_type='approval.expired'`, run.ID).Scan(&expiredEvents); err != nil || expiredEvents != 1 {
		t.Fatalf("approval.expired event count = %d, %v", expiredEvents, err)
	}
	for _, grant := range []permission.Grant{
		{ID: "run-grant", ProjectID: projectID, RunID: run.ID, ToolName: call.ToolName, PermissionKind: tool.PermissionWorkspaceRead, Resource: "paper.md", Scope: permission.ScopeRun, GrantedBy: permission.SubjectUser, CreatedAt: now},
		{ID: "project-grant", ProjectID: projectID, ToolName: call.ToolName, PermissionKind: tool.PermissionWorkspaceRead, Resource: "notes.md", Scope: permission.ScopeProject, GrantedBy: permission.SubjectUser, CreatedAt: now},
	} {
		if _, err := store.DB().ExecContext(ctx, `INSERT INTO permission_grants(id,project_id,run_id,tool_name,permission_kind,resource,scope,granted_by,created_at) VALUES (?,?,?,?,?,?,?,?,?)`, grant.ID, grant.ProjectID, nullableString(grant.RunID), grant.ToolName, grant.PermissionKind, grant.Resource, grant.Scope, grant.GrantedBy, formatTime(grant.CreatedAt)); err != nil {
			t.Fatal(err)
		}
	}
	active, err := repository.ListActiveGrants(ctx, projectID, "another-run", call.ToolName, now.Add(time.Second))
	if err != nil || len(active) != 1 || active[0].ID != "project-grant" {
		t.Fatalf("cross-run grants = %#v, %v", active, err)
	}
	expiresAt := now.Add(2 * time.Second)
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO permission_grants(id,project_id,tool_name,permission_kind,resource,scope,granted_by,created_at,expires_at) VALUES ('expired-grant',?,?,?,?, 'project','user',?,?)`, projectID, call.ToolName, tool.PermissionNetworkDomain, "expired.example.test:443", formatTime(now), formatTime(expiresAt)); err != nil {
		t.Fatal(err)
	}
	active, err = repository.ListActiveGrants(ctx, projectID, run.ID, call.ToolName, expiresAt)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range active {
		if value.ID == "expired-grant" {
			t.Fatal("expired grant participated in policy lookup")
		}
	}
	if _, err := store.DB().ExecContext(ctx, `DELETE FROM runs WHERE id=?`, run.ID); err != nil {
		t.Fatal(err)
	}
	var approvals, runGrants, projectGrants int
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM approvals WHERE run_id=?`, run.ID).Scan(&approvals); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM permission_grants WHERE id='run-grant'`).Scan(&runGrants); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM permission_grants WHERE id='project-grant'`).Scan(&projectGrants); err != nil {
		t.Fatal(err)
	}
	if approvals != 0 || runGrants != 0 || projectGrants != 1 {
		t.Fatalf("cascade counts approvals=%d run=%d project=%d", approvals, runGrants, projectGrants)
	}
	if _, err := store.DB().ExecContext(ctx, `DELETE FROM projects WHERE id=?`, projectID); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM permission_grants WHERE project_id=?`, projectID).Scan(&projectGrants); err != nil || projectGrants != 0 {
		t.Fatalf("project grant cascade = %d, %v", projectGrants, err)
	}
}

func TestPermissionRepositoryRejectsPersistentDangerousGrantAtDatabaseBoundary(t *testing.T) {
	ctx := context.Background()
	store, run := createToolFixture(t)
	defer store.Close()
	projectID := projectIDForRun(t, store, run.ID)
	for _, kind := range []tool.PermissionKind{tool.PermissionFilesystemExternal, tool.PermissionProcessExecute, tool.PermissionDestructive, tool.PermissionSecretUse} {
		if _, err := store.DB().ExecContext(ctx, `INSERT INTO permission_grants(id,project_id,tool_name,permission_kind,resource,scope,granted_by,created_at) VALUES (?,?,?,?,?,'project','user',?)`, string(kind), projectID, "builtin.test", kind, "exact", formatTime(time.Now().UTC())); err == nil {
			t.Fatalf("dangerous persistent grant %s was accepted", kind)
		}
	}
}

func permissionCall(runID, id string) tool.Call {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	return tool.Call{ID: id, RunID: runID, ProviderCallID: "provider-" + id, ToolName: "builtin.workspace.read", ToolVersion: "1", Arguments: json.RawMessage(`{"path":"paper.md"}`), Status: tool.CallPending, Risk: tool.RiskModerate, Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead, Resource: "paper.md"}, {Kind: tool.PermissionNetworkDomain, Resource: "api.example.test:443"}}, Idempotent: true, CreatedAt: now, UpdatedAt: now}
}

func projectIDForRun(t *testing.T, store *Store, runID string) string {
	t.Helper()
	var projectID string
	if err := store.DB().QueryRow(`SELECT c.project_id FROM runs r JOIN conversations c ON c.id=r.conversation_id WHERE r.id=?`, runID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	return projectID
}

func permissionEvent(runID, eventType string) events.Envelope {
	return events.New(eventType+"-event-"+runID+time.Now().UTC().Format("150405.000000000"), runID, "run", eventType, 0, json.RawMessage(`{}`))
}

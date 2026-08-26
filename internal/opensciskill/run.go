package opensciskill

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrRunSkillNotFound = errors.New("dynamic Run Skill not found")

type RunSkill struct {
	RunID           string                  `json:"runId"`
	ProjectID       string                  `json:"projectId"`
	Ordinal         int                     `json:"ordinal"`
	ToolCallID      string                  `json:"toolCallId"`
	Name            string                  `json:"name"`
	Origin          Origin                  `json:"origin"`
	Category        string                  `json:"category,omitempty"`
	ContentHash     string                  `json:"contentHash"`
	PackageHash     string                  `json:"packageHash"`
	Instructions    string                  `json:"instructions"`
	CapabilityAudit CapabilityAuditSnapshot `json:"capabilityAudit"`
	LoadedAt        time.Time               `json:"loadedAt"`
}

type CapabilityAuditSnapshot struct {
	Version          string     `json:"version,omitempty"`
	Capability       Capability `json:"capability,omitempty"`
	Reason           string     `json:"reason,omitempty"`
	RequiredTools    []string   `json:"requiredTools"`
	PythonPackages   []string   `json:"pythonPackages"`
	CLIDependencies  []string   `json:"cliDependencies"`
	ExternalServices []string   `json:"externalServices"`
	Limitations      []string   `json:"limitations"`
}

func capabilityAuditSnapshot(info Info) CapabilityAuditSnapshot {
	return CapabilityAuditSnapshot{
		Version: info.CapabilityAuditVersion, Capability: info.Capability, Reason: info.CapabilityReason,
		RequiredTools: cloneStrings(info.RequiredTools), PythonPackages: cloneStrings(info.PythonPackages),
		CLIDependencies: cloneStrings(info.CLIDependencies), ExternalServices: cloneStrings(info.ExternalServices),
		Limitations: cloneStrings(info.CapabilityLimitations),
	}
}

func (value CapabilityAuditSnapshot) apply(info *Info) {
	info.CapabilityAuditVersion, info.Capability, info.CapabilityReason = value.Version, value.Capability, value.Reason
	info.RequiredTools, info.PythonPackages = cloneStrings(value.RequiredTools), cloneStrings(value.PythonPackages)
	info.CLIDependencies, info.ExternalServices = cloneStrings(value.CLIDependencies), cloneStrings(value.ExternalServices)
	info.CapabilityLimitations, info.MissingTools = cloneStrings(value.Limitations), []string{}
}

func (s *Service) RecordRunSkill(ctx context.Context, runID, projectID, toolCallID string, info Info, instructions string) (RunSkill, bool, error) {
	runID, projectID, toolCallID = strings.TrimSpace(runID), strings.TrimSpace(projectID), strings.TrimSpace(toolCallID)
	if runID == "" || projectID == "" || toolCallID == "" || !ValidName(info.Name) {
		return RunSkill{}, false, fmt.Errorf("dynamic Run Skill identity is incomplete")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunSkill{}, false, err
	}
	defer tx.Rollback()
	current, err := getRunSkill(ctx, tx, runID, info.Name)
	if err == nil {
		if current.ProjectID != projectID || current.ContentHash != info.ContentHash || current.PackageHash != info.PackageHash || current.Origin != info.Origin || current.Instructions != instructions {
			return RunSkill{}, false, fmt.Errorf("Skill %q changed after it was loaded in this Run", info.Name)
		}
		return current, false, tx.Commit()
	}
	if !errors.Is(err, ErrRunSkillNotFound) {
		return RunSkill{}, false, err
	}
	var ordinal int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal)+1,0) FROM run_dynamic_skills WHERE run_id=?`, runID).Scan(&ordinal); err != nil {
		return RunSkill{}, false, err
	}
	now := time.Now().UTC()
	audit := capabilityAuditSnapshot(info)
	auditJSON, err := json.Marshal(audit)
	if err != nil {
		return RunSkill{}, false, fmt.Errorf("encode Skill capability audit: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_dynamic_skills(run_id,project_id,ordinal,tool_call_id,skill_name,origin,category,content_hash,package_hash,instruction_snapshot,capability_audit_json,loaded_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, runID, projectID, ordinal, toolCallID, info.Name, info.Origin, info.Category, info.ContentHash, info.PackageHash, instructions, string(auditJSON), now.Format(time.RFC3339Nano)); err != nil {
		return RunSkill{}, false, err
	}
	value := RunSkill{RunID: runID, ProjectID: projectID, Ordinal: ordinal, ToolCallID: toolCallID, Name: info.Name, Origin: info.Origin, Category: info.Category, ContentHash: info.ContentHash, PackageHash: info.PackageHash, Instructions: instructions, CapabilityAudit: audit, LoadedAt: now}
	return value, true, tx.Commit()
}

func (s *Service) GetRunSkill(ctx context.Context, runID, name string) (RunSkill, error) {
	return getRunSkill(ctx, s.db, strings.TrimSpace(runID), strings.TrimSpace(name))
}

func (s *Service) ListRunSkills(ctx context.Context, runID string) ([]RunSkill, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT run_id,project_id,ordinal,tool_call_id,skill_name,origin,category,content_hash,package_hash,instruction_snapshot,capability_audit_json,loaded_at
		FROM run_dynamic_skills WHERE run_id=? ORDER BY ordinal`, strings.TrimSpace(runID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []RunSkill{}
	for rows.Next() {
		value, err := scanRunSkill(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

type rowScanner interface{ Scan(...any) error }

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getRunSkill(ctx context.Context, queryer rowQueryer, runID, name string) (RunSkill, error) {
	value, err := scanRunSkill(queryer.QueryRowContext(ctx, `SELECT run_id,project_id,ordinal,tool_call_id,skill_name,origin,category,content_hash,package_hash,instruction_snapshot,capability_audit_json,loaded_at
		FROM run_dynamic_skills WHERE run_id=? AND skill_name=?`, runID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return RunSkill{}, ErrRunSkillNotFound
	}
	return value, err
}

func scanRunSkill(row rowScanner) (RunSkill, error) {
	var value RunSkill
	var loaded, auditJSON string
	if err := row.Scan(&value.RunID, &value.ProjectID, &value.Ordinal, &value.ToolCallID, &value.Name, &value.Origin, &value.Category, &value.ContentHash, &value.PackageHash, &value.Instructions, &auditJSON, &loaded); err != nil {
		return RunSkill{}, err
	}
	if err := json.Unmarshal([]byte(auditJSON), &value.CapabilityAudit); err != nil {
		return RunSkill{}, fmt.Errorf("decode Skill capability audit: %w", err)
	}
	value.CapabilityAudit.RequiredTools = nonNilStrings(value.CapabilityAudit.RequiredTools)
	value.CapabilityAudit.PythonPackages = nonNilStrings(value.CapabilityAudit.PythonPackages)
	value.CapabilityAudit.CLIDependencies = nonNilStrings(value.CapabilityAudit.CLIDependencies)
	value.CapabilityAudit.ExternalServices = nonNilStrings(value.CapabilityAudit.ExternalServices)
	value.CapabilityAudit.Limitations = nonNilStrings(value.CapabilityAudit.Limitations)
	var err error
	value.LoadedAt, err = time.Parse(time.RFC3339Nano, loaded)
	return value, err
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func (s *Service) resolveExact(ctx context.Context, projectID string, loaded RunSkill) (Info, error) {
	info, err := s.resolve(ctx, projectID, loaded.Name)
	if err != nil {
		return Info{}, err
	}
	if info.Origin != loaded.Origin || info.ContentHash != loaded.ContentHash || info.PackageHash != loaded.PackageHash {
		return Info{}, fmt.Errorf("Skill %q package no longer matches the Run snapshot", loaded.Name)
	}
	if _, err := s.verifiedInstructionBody(info); err != nil {
		return Info{}, fmt.Errorf("Skill %q package no longer matches the Run snapshot: %w", loaded.Name, err)
	}
	return info, nil
}

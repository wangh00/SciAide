package opensciskill

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/wangh00/SciAide/internal/app/tool"
)

// Capability describes the strongest currently usable execution path. It is
// deliberately independent from whether a user allows a Skill to be loaded.
type Capability string

const (
	CapabilityNative                  Capability = "native"
	CapabilityRequiresDependency      Capability = "requires_dependency"
	CapabilityRequiresExternalService Capability = "requires_external_service"
	CapabilityUnavailable             Capability = "unavailable"
	CapabilityUnreviewed              Capability = "unreviewed"
)

const capabilityAuditFile = "capability-audit.v1.json"

type capabilityAuditManifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	AuditVersion  string            `json:"auditVersion"`
	SkillCount    int               `json:"skillCount"`
	Skills        []capabilityAudit `json:"skills"`
}

type capabilityAudit struct {
	Name             string     `json:"name"`
	PackageHash      string     `json:"packageHash"`
	Capability       Capability `json:"capability"`
	Reason           string     `json:"reason"`
	RequiredTools    []string   `json:"requiredTools"`
	PythonPackages   []string   `json:"pythonPackages"`
	CLIDependencies  []string   `json:"cliDependencies"`
	ExternalServices []string   `json:"externalServices"`
	Limitations      []string   `json:"limitations"`
}

type capabilityAuditor struct {
	version string
	byName  map[string]capabilityAudit
}

func loadCapabilityAuditor() (*capabilityAuditor, error) {
	encoded, err := bundled.ReadFile(capabilityAuditFile)
	if err != nil {
		return nil, fmt.Errorf("read embedded Skill capability audit: %w", err)
	}
	var manifest capabilityAuditManifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		return nil, fmt.Errorf("decode embedded Skill capability audit: %w", err)
	}
	if manifest.SchemaVersion != 1 || strings.TrimSpace(manifest.AuditVersion) == "" || manifest.SkillCount != len(manifest.Skills) || manifest.SkillCount != 311 {
		return nil, fmt.Errorf("embedded Skill capability audit metadata is invalid")
	}
	result := &capabilityAuditor{version: strings.TrimSpace(manifest.AuditVersion), byName: make(map[string]capabilityAudit, len(manifest.Skills))}
	for _, entry := range manifest.Skills {
		entry.Name = strings.TrimSpace(entry.Name)
		entry.PackageHash = strings.ToLower(strings.TrimSpace(entry.PackageHash))
		entry.Reason = strings.TrimSpace(entry.Reason)
		entry.RequiredTools = uniqueSortedStrings(entry.RequiredTools)
		entry.PythonPackages = uniqueSortedStrings(entry.PythonPackages)
		entry.CLIDependencies = uniqueSortedStrings(entry.CLIDependencies)
		entry.ExternalServices = uniqueSortedStrings(entry.ExternalServices)
		entry.Limitations = uniqueSortedStrings(entry.Limitations)
		if !ValidName(entry.Name) || len(entry.PackageHash) != 64 || entry.Reason == "" || !validCapability(entry.Capability) {
			return nil, fmt.Errorf("invalid capability audit entry for %q", entry.Name)
		}
		if _, exists := result.byName[entry.Name]; exists {
			return nil, fmt.Errorf("duplicate capability audit entry for %q", entry.Name)
		}
		result.byName[entry.Name] = entry
	}
	return result, nil
}

func (a *capabilityAuditor) applyBase(info *Info) {
	if info.Origin != OriginDefault {
		info.Capability = CapabilityUnreviewed
		info.CapabilityReason = "第三方或本地 Skill 尚未进入默认能力审计"
		return
	}
	entry, ok := a.byName[info.Name]
	if !ok {
		info.Capability = CapabilityUnreviewed
		info.CapabilityReason = "默认 Skill 缺少版本化能力审计记录"
		return
	}
	info.CapabilityAuditVersion = a.version
	info.RequiredTools = cloneStrings(entry.RequiredTools)
	info.PythonPackages = cloneStrings(entry.PythonPackages)
	info.CLIDependencies = cloneStrings(entry.CLIDependencies)
	info.ExternalServices = cloneStrings(entry.ExternalServices)
	info.CapabilityLimitations = cloneStrings(entry.Limitations)
	if entry.PackageHash != strings.ToLower(info.PackageHash) {
		info.Capability = CapabilityUnavailable
		info.CapabilityReason = "Skill 包内容已偏离能力审计锁定版本；需重新审计后才能加载"
		return
	}
	info.Capability = entry.Capability
	info.CapabilityReason = entry.Reason
}

func (a *capabilityAuditor) applyRuntime(ctx context.Context, registry tool.Registry, info *Info) {
	info.MissingTools = []string{}
	if registry == nil || info.Origin != OriginDefault || len(info.RequiredTools) == 0 || info.Capability == CapabilityUnavailable || info.Capability == CapabilityUnreviewed {
		return
	}
	for _, name := range info.RequiredTools {
		if _, err := registry.Definition(ctx, name); err != nil {
			info.MissingTools = append(info.MissingTools, name)
		}
	}
	if len(info.MissingTools) > 0 {
		info.Capability = CapabilityUnavailable
		info.CapabilityReason = "当前运行时缺少能力审计要求的工具：" + strings.Join(info.MissingTools, "、")
	}
}

func validCapability(value Capability) bool {
	switch value {
	case CapabilityNative, CapabilityRequiresDependency, CapabilityRequiresExternalService, CapabilityUnavailable:
		return true
	default:
		return false
	}
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]string, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			seen[strings.ToLower(value)] = value
		}
	}
	result := make([]string, 0, len(seen))
	for _, value := range seen {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i]) < strings.ToLower(result[j]) })
	return result
}

func cloneStrings(values []string) []string {
	return append(make([]string, 0, len(values)), values...)
}

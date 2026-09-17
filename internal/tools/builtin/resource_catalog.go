package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/opensciskill"
)

func (r *ResourceActions) initial(ctx context.Context, scope resource.Scope) ([]resource.Seed, json.RawMessage, error) {
	seeds := []resource.Seed{}
	files := []resource.Seed{}
	facts := map[string]any{"scope": "current task and explicitly shared project documents", "inputState": "not_exposed", "attachmentCount": 0, "workspaceEntryCount": 0, "unavailableDocuments": []any{}, "workspaceMetadataOnly": []string{}}
	inv := tool.Invocation{RunID: scope.RunID, SubjectKind: tool.SubjectChatRun, ProjectID: scope.ProjectID, ResearchTaskID: scope.TaskID, WorkspaceRoot: scope.WorkspaceRoot}
	inspected := false
	if scope.ToolContracts[ListWorkspaceName] != "" || scope.ToolContracts[ReadTextName] != "" {
		result, err := r.metadata(ctx, scope, inv, ListWorkspaceName, resourceArgs(map[string]any{"path": ".", "limit": 500}))
		if err != nil {
			return nil, nil, err
		}
		var data struct {
			Entries   []struct{ Name, Path, Kind string }
			Truncated bool
		}
		if err = json.Unmarshal(result.Structured, &data); err != nil {
			return nil, nil, err
		}
		facts["workspaceEntryCount"] = len(data.Entries)
		facts["workspaceInventoryTruncated"] = data.Truncated
		inspected = true
		found, err := workspaceResourceSeeds(scope, result)
		if err != nil {
			return nil, nil, err
		}
		files = append(files, found...)
		unsupported := []string{}
		for _, entry := range data.Entries {
			if entry.Kind == "file" && !workspaceTextResource(entry.Path) {
				unsupported = append(unsupported, entry.Name)
			}
		}
		facts["workspaceMetadataOnly"] = unsupported
	}
	if scope.ToolContracts[ListAttachmentsName] != "" || scope.ToolContracts[InspectDocumentName] != "" || scope.ToolContracts[ReadDocumentName] != "" || scope.ToolContracts[SearchDocumentName] != "" {
		result, err := r.metadata(ctx, scope, inv, ListAttachmentsName, json.RawMessage(`{}`))
		if err != nil {
			return nil, nil, err
		}
		var data struct {
			Attachments []struct {
				ID     string `json:"id"`
				Name   string `json:"originalName"`
				Status string `json:"status"`
				Format string `json:"format"`
			}
		}
		if err = json.Unmarshal(result.Structured, &data); err != nil {
			return nil, nil, err
		}
		facts["attachmentCount"] = len(data.Attachments)
		inspected = true
		unavailable := []any{}
		for _, a := range data.Attachments {
			if a.Status != "ready" || a.Format == "image" {
				unavailable = append(unavailable, map[string]string{"name": a.Name, "status": a.Status, "format": a.Format})
				continue
			}
			if scope.ToolContracts[InspectDocumentName] != "" {
				files = append(files, resource.Seed{Kind: "document", Label: "查看资料结构 · " + a.Name, ToolName: InspectDocumentName, Arguments: resourceArgs(map[string]any{"attachmentId": a.ID})})
			}
			if scope.ToolContracts[ReadDocumentName] != "" {
				files = append(files, resource.Seed{Kind: "document_read", Label: "读取资料 · " + a.Name, ToolName: ReadDocumentName, Arguments: resourceArgs(map[string]any{"attachmentId": a.ID, "maxChars": 6000})})
			}
			if scope.ToolContracts[SearchDocumentName] != "" {
				files = append(files, resource.Seed{Kind: "document_search", Label: "检索资料 · " + a.Name, ToolName: SearchDocumentName, Arguments: resourceArgs(map[string]any{"attachmentId": a.ID, "limit": 8}), Search: true})
			}
		}
		facts["unavailableDocuments"] = unavailable
	}
	if inspected {
		facts["inputState"] = "no_supplied_resources"
		if facts["attachmentCount"].(int) > 0 || facts["workspaceEntryCount"].(int) > 0 {
			facts["inputState"] = "resources_present"
		}
	}
	if len(files) > 0 {
		seeds = append(seeds, resource.Seed{Kind: "menu", Label: "当前任务资料", Children: files})
		seeds = append(seeds, menuPage(files, "当前任务资料")...)
	}
	if scope.ToolContracts[SkillLoadName] != "" {
		snapshot, err := r.skills.Catalog(ctx, scope.ProjectID)
		if err != nil {
			return nil, nil, err
		}
		byName := map[string]opensciskill.Info{}
		categories := map[string][]resource.Seed{}
		strict := map[string]bool{}
		for _, name := range scope.SkillNames {
			strict[name] = true
		}
		for _, s := range snapshot.Skills {
			if !s.Entry || !s.Enabled || s.Capability == opensciskill.CapabilityUnavailable || (!scope.SkillDiscovery && !strict[s.Name]) {
				continue
			}
			byName[s.Name] = s
			categories[s.Category] = append(categories[s.Category], skillResourceSeed(s))
		}
		names := append([]string(nil), scope.SkillNames...)
		if scope.SkillDiscovery {
			names = nil
			if audits, ok := r.skills.(interface {
				GetRoutingAudit(context.Context, string, string) (opensciskill.RoutingAudit, error)
			}); ok {
				audit, err := audits.GetRoutingAudit(ctx, scope.ProjectID, scope.RunID)
				if err != nil {
					return nil, nil, err
				}
				for _, candidate := range audit.Candidates {
					if candidate.Shortlisted {
						names = append(names, candidate.Name)
					}
				}
			} else {
				for name := range byName {
					names = append(names, name)
				}
				sort.Strings(names)
			}
			if len(names) > 8 {
				names = names[:8]
			}
		}
		for _, name := range names {
			if s, ok := byName[name]; ok {
				seeds = append(seeds, skillResourceSeed(s))
			}
		}
		groups := []resource.Seed{}
		keys := []string{}
		for key := range categories {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			items := categories[key]
			sort.Slice(items, func(i, j int) bool { return items[i].Label < items[j].Label })
			groups = append(groups, resource.Seed{Kind: "menu", Label: "Skill 分类 · " + key, Children: items})
		}
		if len(groups) > 0 {
			seeds = append(seeds, resource.Seed{Kind: "menu", Label: "浏览可用 Skill（仅目录，不算加载）", Children: groups})
			// Common categories are directly selectable; a catalog menu retains
			// bounded navigation when there are more categories than fit here.
			seeds = append(seeds, groups[:min(len(groups), 24)]...)
		}
		facts["availableSkillCount"] = len(byName)
	}
	facts["resourceSnapshotVersion"] = resource.Version
	return seeds, resourceArgs(facts), nil
}

// Metadata discovery is host-owned and contains no model locator. A stage
// authorized to read documents/files may enumerate their scoped identities;
// this does not grant the model a free-form list/read adapter.
func (r *ResourceActions) metadata(ctx context.Context, scope resource.Scope, inv tool.Invocation, name string, args json.RawMessage) (tool.Result, error) {
	if name != ListWorkspaceName && name != ListAttachmentsName {
		return tool.Result{}, fmt.Errorf("not a metadata adapter")
	}
	d, err := r.registry.Definition(ctx, name)
	if err != nil {
		return tool.Result{}, err
	}
	copyScope := scope
	copyScope.ToolContracts = map[string]string{}
	for k, v := range scope.ToolContracts {
		copyScope.ToolContracts[k] = v
	}
	copyScope.ToolContracts[name] = tool.DefinitionFingerprint(d)
	return r.invoke(ctx, copyScope, inv, name, args)
}
func skillResourceSeed(s opensciskill.Info) resource.Seed {
	desc := []rune(s.Description)
	if len(desc) > 120 {
		desc = desc[:120]
	}
	return resource.Seed{Kind: "skill", Label: "加载 Skill · " + s.Name + " — " + string(desc) + " [" + string(s.Capability) + "]", ToolName: SkillLoadName, Arguments: resourceArgs(map[string]any{"name": s.Name, "limit": 6000}), ContentHash: s.ContentHash, PackageHash: s.PackageHash}
}
func workspaceTextResource(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".txt", ".md", ".csv", ".tsv", ".json", ".py", ".r", ".jl", ".yaml", ".yml", ".toml", ".log", ".xml", ".html", ".css", ".js", ".sql", ".ini", ".cfg", ".tex":
		return true
	}
	return false
}
func workspaceResourceSeeds(scope resource.Scope, result tool.Result) ([]resource.Seed, error) {
	var data struct {
		Entries   []struct{ Name, Path, Kind string }
		Truncated bool
	}
	if err := json.Unmarshal(result.Structured, &data); err != nil {
		return nil, err
	}
	seeds := []resource.Seed{}
	for _, entry := range data.Entries {
		if entry.Kind == "directory" && scope.ToolContracts[ListWorkspaceName] != "" {
			seeds = append(seeds, resource.Seed{Kind: "directory", Label: "浏览目录 · " + entry.Name, ToolName: ListWorkspaceName, Arguments: resourceArgs(map[string]any{"path": entry.Path, "limit": 500})})
		}
		if entry.Kind == "file" && scope.ToolContracts[ReadTextName] != "" && workspaceTextResource(entry.Path) {
			seeds = append(seeds, resource.Seed{Kind: "workspace_read", Label: "读取文件 · " + entry.Name, ToolName: ReadTextName, Arguments: resourceArgs(map[string]any{"path": entry.Path, "maxBytes": 6000})})
		}
	}
	return seeds, nil
}

func (r *ResourceActions) followups(scope resource.Scope, parent resource.Seed, result tool.Result) ([]resource.Seed, error) {
	seeds := []resource.Seed{}
	var args map[string]any
	if err := json.Unmarshal(parent.Arguments, &args); err != nil {
		return nil, err
	}
	switch parent.ToolName {
	case ListWorkspaceName:
		values, err := workspaceResourceSeeds(scope, result)
		if err != nil {
			return nil, err
		}
		return menuPage(values, parent.Label), nil
	case InspectDocumentName:
		var data struct {
			Units []struct{ Locator, Title string }
		}
		if err := json.Unmarshal(result.Structured, &data); err != nil {
			return nil, err
		}
		if scope.ToolContracts[ReadDocumentName] != "" {
			for _, u := range data.Units {
				seeds = append(seeds, resource.Seed{Kind: "document_read", Label: "读取资料片段 · " + u.Locator + " " + u.Title, ToolName: ReadDocumentName, Arguments: resourceArgs(map[string]any{"attachmentId": args["attachmentId"], "locator": u.Locator, "maxChars": 6000})})
			}
		}
		return menuPage(seeds, parent.Label), nil
	case SkillLoadName:
		var data skillLoadResult
		if err := json.Unmarshal(result.Structured, &data); err != nil {
			return nil, err
		}
		if data.Mode == "index" {
			for _, section := range data.Sections {
				child := parent
				child.Kind = "skill_section"
				child.Label = "读取 Skill 章节 · " + data.Name + " / " + section.Heading
				child.Arguments = resourceArgs(map[string]any{"name": data.Name, "section": section.ID, "limit": 6000})
				seeds = append(seeds, child)
			}
			if scope.ToolContracts[ListSkillResourcesName] != "" {
				child := parent
				child.Kind = "skill_resources"
				child.Label = "查看 Skill 配套资源 · " + data.Name
				child.ToolName = ListSkillResourcesName
				child.Arguments = resourceArgs(map[string]any{"name": data.Name})
				seeds = append([]resource.Seed{child}, seeds...)
			}
			return menuPage(seeds, "Skill · "+data.Name), nil
		}
		if data.Truncated && data.NextOffset > data.Offset {
			args["offset"] = data.NextOffset
			child := parent
			child.Label = "继续读取 · " + data.Name
			child.Arguments = resourceArgs(args)
			seeds = append(seeds, child)
		}
	case ListSkillResourcesName:
		var data opensciskill.ResourceList
		if err := json.Unmarshal(result.Structured, &data); err != nil {
			return nil, err
		}
		if scope.ToolContracts[ReadSkillResourceName] != "" {
			for _, entry := range data.Resources {
				if !entry.Text {
					continue
				}
				child := parent
				child.Kind = "skill_resource_read"
				child.Label = "读取 Skill 资源 · " + data.Name + " / " + entry.Path
				child.ToolName = ReadSkillResourceName
				child.Arguments = resourceArgs(map[string]any{"name": data.Name, "path": entry.Path, "maxBytes": 6000})
				seeds = append(seeds, child)
			}
		}
		return menuPage(seeds, "Skill 配套资源 · "+data.Name), nil
	case ReadTextName, ReadSkillResourceName:
		var data struct {
			BytesRead     int  `json:"bytesRead"`
			OriginalBytes int  `json:"originalBytes"`
			Truncated     bool `json:"truncated"`
		}
		if err := json.Unmarshal(result.Structured, &data); err != nil {
			return nil, err
		}
		offset := 0
		if v, ok := args["offset"].(float64); ok {
			offset = int(v)
		}
		if data.Truncated && data.BytesRead > 0 && offset+data.BytesRead < data.OriginalBytes {
			args["offset"] = offset + data.BytesRead
			child := parent
			child.Label = "继续读取 · " + parent.Label
			child.Arguments = resourceArgs(args)
			seeds = append(seeds, child)
		}
	case ReadDocumentName:
		var data struct {
			Offset, Characters int
			Truncated          bool
		}
		if err := json.Unmarshal(result.Structured, &data); err != nil {
			return nil, err
		}
		// A full page can continue. A parser-truncated short page cannot be advanced
		// by inventing a byte/rune offset beyond the parsed content.
		if data.Truncated && data.Characters >= 6000 {
			args["offset"] = data.Offset + data.Characters
			child := parent
			child.Label = "继续读取资料"
			child.Arguments = resourceArgs(args)
			seeds = append(seeds, child)
		}
	}
	return seeds, nil
}

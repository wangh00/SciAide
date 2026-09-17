package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/opensciskill"
)

const SkillLoadName = "builtin.skill.load"

type DynamicSkillService interface {
	Catalog(ctx context.Context, projectID string) (opensciskill.Snapshot, error)
	Browse(ctx context.Context, projectID, category string) ([]opensciskill.Info, error)
	LoadStructuredForRun(ctx context.Context, runID, projectID, toolCallID, name, section string, offset, limit int) (opensciskill.Info, opensciskill.Chunk, bool, error)
}

type SkillLoad struct {
	skills   DynamicSkillService
	registry tool.Registry
}

func NewSkillLoad(skills DynamicSkillService, registries ...tool.Registry) *SkillLoad {
	value := &SkillLoad{skills: skills}
	if len(registries) > 0 {
		value.registry = registries[0]
	}
	return value
}

func (t *SkillLoad) Definition(ctx context.Context) (tool.Definition, error) {
	if t == nil || t.skills == nil {
		return tool.Definition{}, fmt.Errorf("Skill catalog is not configured")
	}
	snapshot, err := t.skills.Catalog(ctx, "")
	if err != nil {
		return tool.Definition{}, err
	}
	parts := make([]string, 0, len(snapshot.Categories))
	for _, category := range snapshot.Categories {
		parts = append(parts, fmt.Sprintf("%s (%d)", category.Name, category.Count))
	}
	description := "Load specialized research instructions before work whenever their procedure applies. Choose by task meaning, not keyword matching. Use category to browse and name to load; call silently and apply the loaded guidance subject to system rules."
	if len(parts) > 0 {
		description += " Available categories: " + strings.Join(parts, ", ") + "."
	}
	return tool.Definition{
		QualifiedName: SkillLoadName,
		Description:   description,
		InputSchema:   json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"name":{"type":"string","minLength":1,"maxLength":64,"description":"Exact frontmatter name of the Skill to load"},"category":{"type":"string","minLength":1,"maxLength":64,"description":"Category to browse when a Skill name is not known"},"section":{"type":"string","pattern":"^section-[0-9]+$","description":"Stable section ID returned by a long Skill index"},"offset":{"type":"integer","minimum":0,"maximum":2000000,"description":"Rune offset within a section, unheaded Skill, or category list"},"limit":{"type":"integer","minimum":1,"maximum":7500,"description":"Maximum instruction runes; category browse uses at most 30 entries"}}}`),
		OutputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["mode","name","category","origin","section","count","offset","nextOffset","total","truncated","loaded","skills","sections","capabilities"],"properties":{"mode":{"type":"string","enum":["categories","category","skill","index","section"]},"name":{"type":"string"},"category":{"type":"string"},"origin":{"type":"string"},"section":{"type":"string"},"count":{"type":"integer","minimum":0},"offset":{"type":"integer","minimum":0},"nextOffset":{"type":"integer","minimum":0},"total":{"type":"integer","minimum":0},"truncated":{"type":"boolean"},"loaded":{"type":"boolean"},"skills":{"type":"array","maxItems":30,"items":{"type":"object"}},"sections":{"type":"array","items":{"type":"object"}},"capabilities":{"type":"array","items":{"type":"object"}}}}`),
		Risk:          tool.RiskLow,
		Permissions:   []tool.PermissionRequirement{},
		Idempotent:    true,
		Version:       "1",
	}, nil
}

type skillLoadArguments struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Section  string `json:"section"`
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
}

type skillListing struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Category    string              `json:"category"`
	Origin      opensciskill.Origin `json:"origin"`
}

type skillLoadResult struct {
	Mode         string                            `json:"mode"`
	Name         string                            `json:"name"`
	Category     string                            `json:"category"`
	Origin       string                            `json:"origin"`
	Section      string                            `json:"section"`
	Count        int                               `json:"count"`
	Offset       int                               `json:"offset"`
	NextOffset   int                               `json:"nextOffset"`
	Total        int                               `json:"total"`
	Truncated    bool                              `json:"truncated"`
	Loaded       bool                              `json:"loaded"`
	Skills       []skillListing                    `json:"skills"`
	Sections     []opensciskill.InstructionSection `json:"sections"`
	Capabilities []skillCapability                 `json:"capabilities"`
}

type skillCapability struct {
	Declared string   `json:"declared"`
	Status   string   `json:"status"`
	Tools    []string `json:"tools"`
	Note     string   `json:"note"`
}

func (t *SkillLoad) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if t == nil || t.skills == nil {
		return tool.Result{}, fmt.Errorf("Skill catalog is not configured")
	}
	var args skillLoadArguments
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	args.Name, args.Category = strings.TrimSpace(args.Name), strings.ToLower(strings.TrimSpace(args.Category))
	if args.Name != "" {
		return t.load(ctx, invocation, args)
	}
	if args.Category != "" {
		return t.browse(ctx, invocation.ProjectID, args)
	}
	if args.Section != "" || args.Offset != 0 || args.Limit != 0 {
		return tool.Result{}, fmt.Errorf("offset and limit require a Skill name or category")
	}
	snapshot, err := t.skills.Catalog(ctx, invocation.ProjectID)
	if err != nil {
		return tool.Result{}, err
	}
	lines := []string{"## Skill categories", "", "Browse a category, then load the most relevant Skill by exact name."}
	for _, item := range snapshot.Categories {
		lines = append(lines, fmt.Sprintf("- **%s** (%d)", item.Name, item.Count))
	}
	metadata := skillLoadResult{Mode: "categories", Count: len(snapshot.Categories), Total: snapshot.EnabledCount, Skills: []skillListing{}}
	return skillToolResult(strings.Join(lines, "\n"), metadata, false)
}

func (t *SkillLoad) browse(ctx context.Context, projectID string, args skillLoadArguments) (tool.Result, error) {
	values, err := t.skills.Browse(ctx, projectID, args.Category)
	if err != nil {
		return tool.Result{}, err
	}
	if len(values) == 0 {
		// A catalog query with no matches is not an execution failure. Keep the
		// frozen Tool contract unchanged and offer actual selectable categories.
		snapshot, catalogErr := t.skills.Catalog(ctx, projectID)
		if catalogErr != nil {
			return tool.Result{}, catalogErr
		}
		lines := []string{fmt.Sprintf("未找到分类 %q 下的可用 Skill；这不表示相关学科的 Skill 不存在。", args.Category), "CATALOG ONLY: loaded=false. No Skill instructions were loaded. Do not guess category names; select from the actual categories below or load a known exact Skill name."}
		for _, category := range snapshot.Categories {
			lines = append(lines, fmt.Sprintf("- %s (%d)", category.Name, category.Count))
		}
		if len(snapshot.Categories) == 0 {
			lines = append(lines, "当前没有可用分类；请检查 Skill 是否已启用。")
		}
		metadata := skillLoadResult{Mode: "category", Category: args.Category, Count: 0, Total: 0, Skills: []skillListing{}}
		return skillToolResult(strings.Join(lines, "\n"), metadata, false)
	}
	if args.Offset > len(values) {
		return tool.Result{}, fmt.Errorf("category offset exceeds result count")
	}
	limit := args.Limit
	if limit <= 0 || limit > 30 {
		limit = 30
	}
	end := min(len(values), args.Offset+limit)
	listed := make([]skillListing, 0, end-args.Offset)
	lines := []string{fmt.Sprintf("## Skills in category: %s", args.Category), "", "CATALOG ONLY: loaded=false. This call lists candidates; it does not load any Skill instructions. Call builtin.skill.load with an exact name before claiming or applying that Skill."}
	for _, item := range values[args.Offset:end] {
		description := strings.TrimSpace(item.Description)
		if runes := []rune(description); len(runes) > 180 {
			description = string(runes[:180]) + "..."
		}
		listed = append(listed, skillListing{Name: item.Name, Description: description, Category: normalizedSkillCategory(item.Category), Origin: item.Origin})
		lines = append(lines, fmt.Sprintf("- **%s**: %s", item.Name, description))
	}
	metadata := skillLoadResult{Mode: "category", Category: args.Category, Count: len(listed), Offset: args.Offset, NextOffset: end, Total: len(values), Truncated: end < len(values), Skills: listed}
	if metadata.Truncated {
		lines = append(lines, "", fmt.Sprintf("More Skills remain. Continue with category=%q and offset=%d.", args.Category, end))
	}
	return skillToolResult(strings.Join(lines, "\n"), metadata, metadata.Truncated)
}

func (t *SkillLoad) load(ctx context.Context, invocation tool.Invocation, args skillLoadArguments) (tool.Result, error) {
	requestedName := args.Name
	if snapshot, err := t.skills.Catalog(ctx, invocation.ProjectID); err == nil {
		args.Name = canonicalSkillChoice(args.Name, snapshot.Skills)
	}
	info, chunk, created, err := t.skills.LoadStructuredForRun(ctx, invocation.RunID, invocation.ProjectID, invocation.CallID, args.Name, args.Section, args.Offset, args.Limit)
	if err != nil {
		if errors.Is(err, opensciskill.ErrSkillNotFound) {
			hint := "请先按 category 浏览目录，再逐字使用返回的 name 加载；不要根据学科名称猜测 Skill 名称。"
			if snapshot, catalogErr := t.skills.Catalog(ctx, invocation.ProjectID); catalogErr == nil {
				if suggestions := skillNameSuggestions(args.Name, snapshot.Skills); len(suggestions) > 0 {
					hint += "可核对这些候选名称：" + strings.Join(suggestions, ", ") + "。"
				}
			}
			return tool.Result{}, tool.NewUserFacingError(fmt.Sprintf("未找到 Skill %q，当前目录没有这个名称。%s", args.Name, hint))
		}
		return tool.Result{}, err
	}
	lines := []string{
		fmt.Sprintf("## Skill: %s", info.Name), "",
		fmt.Sprintf("**Origin**: %s", info.Origin),
		"**Resource access**: use builtin.skill.resource.list first; use builtin.skill.resource.read_text for UTF-8 guidance, or builtin.skill.resource.materialize to copy a script/template/asset into a new Workspace file before separately invoking an approved execution tool.", "",
	}
	if requestedName != args.Name {
		lines = append(lines, fmt.Sprintf("名称已按当前目录唯一匹配：%q → %q。后续引用请使用返回的规范名称。", requestedName, args.Name), "")
	}
	if info.Origin == opensciskill.OriginDefault {
		lines = append(lines, fmt.Sprintf("**Capability audit**: %s (%s). %s", info.Capability, info.CapabilityAuditVersion, info.CapabilityReason))
		if len(info.PythonPackages) > 0 {
			lines = append(lines, "**Python boundary**: required packages must be installed through builtin.python.environment.install into the project environment before execution. Package installation is separately approved and locked; a Skill may not install packages silently.")
		}
		if len(info.CLIDependencies) > 0 {
			lines = append(lines, "**CLI boundary**: the host does not bundle or prove these command-line dependencies: "+strings.Join(info.CLIDependencies, ", ")+". Probe them before use and report absence honestly.")
		}
		if len(info.ExternalServices) > 0 {
			lines = append(lines, "**External-service boundary**: use only a user-configured MCP/API Tool currently present in the Tool Registry. SciAide does not inject model API keys, MCP secrets, tokens, or provider credentials into Shell/Python; never ask the user to paste a secret into chat or assume an environment variable exists.")
		}
		if len(info.CapabilityLimitations) > 0 {
			lines = append(lines, "**Known limitations**: "+strings.Join(info.CapabilityLimitations, "; "))
		}
		lines = append(lines, "")
	}
	if chunk.Mode == "index" {
		lines = append(lines,
			"This long Skill is indexed by logical Markdown sections. Load every section relevant to the task before acting.",
			"Section IDs are not package files: call builtin.skill.load with the same name and section ID. Never append .md or pass a section ID to builtin.skill.resource.read_text.",
		)
		for _, section := range chunk.Sections {
			lines = append(lines, fmt.Sprintf("- `%s` (H%d, %d runes): %s", section.ID, section.Level, section.Runes, section.Heading))
		}
	} else {
		lines = append(lines, "<skill_instructions>", chunk.Content, "</skill_instructions>")
	}
	if chunk.Truncated && chunk.Mode != "index" {
		lines = append(lines, "", fmt.Sprintf("This Skill is longer than one model-context page. Continue before acting with name=%q and offset=%d.", info.Name, chunk.NextOffset))
	}
	capabilities := t.capabilityDiagnostics(ctx, info.AllowedTools)
	if len(capabilities) > 0 {
		lines = append(lines, "", "**Declared capability check** (metadata only; normal tool permissions still apply):")
		for _, item := range capabilities {
			lines = append(lines, fmt.Sprintf("- %s: %s. %s", item.Declared, item.Status, item.Note))
		}
	}
	mode := "skill"
	if chunk.Mode == "index" || chunk.Mode == "section" {
		mode = chunk.Mode
	}
	metadata := skillLoadResult{Mode: mode, Name: info.Name, Category: normalizedSkillCategory(info.Category), Origin: string(info.Origin), Section: chunk.Section, Count: 1, Offset: chunk.Offset, NextOffset: chunk.NextOffset, Total: chunk.TotalRunes, Truncated: chunk.Truncated, Loaded: true, Skills: []skillListing{}, Sections: chunk.Sections, Capabilities: capabilities}
	_ = created
	return skillToolResult(strings.Join(lines, "\n"), metadata, chunk.Truncated)
}

func (t *SkillLoad) capabilityDiagnostics(ctx context.Context, declared []string) []skillCapability {
	if len(declared) == 0 {
		return []skillCapability{}
	}
	definitions := []tool.Definition{}
	if t.registry != nil {
		definitions, _ = t.registry.Definitions(ctx)
	}
	result := make([]skillCapability, 0, len(declared))
	for _, value := range declared {
		matches := capabilityMatches(value, definitions)
		status, note := "missing", "No currently registered tool provides this capability."
		if len(matches) > 0 {
			status, note = "available", "Matched registered tools; each invocation still uses its own permission policy."
			for _, name := range matches {
				if strings.HasPrefix(name, "mcp.") {
					status, note = "mcp_available", "Provided by a connected MCP server; each invocation still uses its own permission policy."
					break
				}
			}
		}
		result = append(result, skillCapability{Declared: value, Status: status, Tools: matches, Note: note})
	}
	return result
}

func capabilityMatches(declared string, definitions []tool.Definition) []string {
	declared = strings.ToLower(strings.TrimSpace(declared))
	result := []string{}
	for _, definition := range definitions {
		name := strings.ToLower(definition.QualifiedName)
		matched := false
		switch declared {
		case "read":
			matched = hasPermission(definition, tool.PermissionWorkspaceRead) || strings.Contains(name, ".read") || strings.Contains(name, ".search") || strings.Contains(name, ".inspect") || strings.Contains(name, ".list")
		case "write", "edit":
			matched = hasPermission(definition, tool.PermissionWorkspaceWrite)
		case "bash", "shell", "python":
			matched = hasPermission(definition, tool.PermissionProcessExecute)
		case "generate_image", "generate-image":
			matched = strings.Contains(name, "generate_image") || strings.Contains(name, "generate-image") || strings.Contains(name, "image.generate")
		default:
			matched = name == declared || strings.HasSuffix(name, "."+declared)
		}
		if matched {
			result = append(result, definition.QualifiedName)
		}
	}
	sort.Strings(result)
	return result
}

func hasPermission(definition tool.Definition, kind tool.PermissionKind) bool {
	for _, permission := range definition.Permissions {
		if permission.Kind == kind {
			return true
		}
	}
	return false
}

func skillToolResult(text string, metadata skillLoadResult, truncated bool) (tool.Result, error) {
	if metadata.Skills == nil {
		metadata.Skills = []skillListing{}
	}
	if metadata.Sections == nil {
		metadata.Sections = []opensciskill.InstructionSection{}
	}
	if metadata.Capabilities == nil {
		metadata.Capabilities = []skillCapability{}
	}
	structured, err := json.Marshal(metadata)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Status: tool.ResultSuccess, Text: text, Structured: structured, Truncated: truncated}, nil
}

func normalizedSkillCategory(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "other"
	}
	return value
}

func skillNameSuggestions(query string, values []opensciskill.Info) []string {
	query = strings.ToLower(strings.TrimSpace(query))
	type candidate struct {
		name  string
		score int
	}
	candidates := make([]candidate, 0, len(values))
	for _, item := range values {
		name := strings.ToLower(item.Name)
		score := 0
		if strings.Contains(name, query) || strings.Contains(query, name) {
			score = 100 - absInt(len(name)-len(query))
		}
		if score > 0 {
			candidates = append(candidates, candidate{item.Name, score})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	result := []string{}
	for _, item := range candidates[:min(5, len(candidates))] {
		result = append(result, item.name)
	}
	return result
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

// Only an exact or unique case-insensitive catalog name is compatible. No
// edit-distance, substring or semantic guessing may change the loaded Skill.
func canonicalSkillChoice(requested string, values []opensciskill.Info) string {
	requested = strings.TrimSpace(requested)
	for _, value := range values {
		if value.Name == requested {
			return requested
		}
	}
	match := ""
	for _, value := range values {
		if !value.Enabled || !value.Entry || value.Capability == opensciskill.CapabilityUnavailable || !strings.EqualFold(value.Name, requested) {
			continue
		}
		if match != "" && match != value.Name {
			return requested
		}
		match = value.Name
	}
	if match != "" {
		return match
	}
	return requested
}

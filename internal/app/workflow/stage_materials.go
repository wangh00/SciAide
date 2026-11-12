package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/skillrun"
)

const stageMaterialHandoffVersion = "source-v1"

// StageMaterialRequest is host-only. Never accept a locator or source body from
// model arguments. Both ends bind the projection to the active frozen execution.
type StageMaterialRequest struct {
	ProjectID, ConversationID, WorkflowRunID, WorkflowStepID, ChatRunID string
	Attempt                                                             int
	InputSHA256                                                         string
}

type StageMaterial struct {
	Kind             string `json:"kind"`
	Locator          string `json:"locator"`
	Content          string `json:"content"`
	SHA256           string `json:"sha256"`
	SkillName        string `json:"skillName,omitempty"`
	SkillContentHash string `json:"skillContentHash,omitempty"`
	SkillPackageHash string `json:"skillPackageHash,omitempty"`
	SourceStepID     string `json:"sourceStepId"`
	SourceAttempt    int    `json:"sourceAttempt"`
	SourceCallID     string `json:"sourceCallId"`
}

// StageMaterials carries literal source snapshots, NOT previous reasoning,
// verdicts, read-completion flags, or an authorization to access other files.
type StageMaterials struct {
	Binding StageMaterialRequest
	Sources []StageMaterial
}

func (s *RuntimeService) StageMaterials(ctx context.Context, command StageMaterialRequest) (StageMaterials, error) {
	detail, err := s.repository.GetRun(ctx, command.ProjectID, command.WorkflowRunID)
	if err != nil {
		return StageMaterials{}, err
	}
	step := currentStep(detail)
	result := StageMaterials{Binding: command}
	if step == nil {
		return result, nil
	}
	node := compilationNodeMap(detail.Run.Compilation)[step.NodeID]
	if node.Kind != NodeAgentStage || node.StageMaterialsVersion != stageMaterialHandoffVersion {
		return result, nil // Existing frozen routes keep their original behavior.
	}
	if detail.Run.ID != command.WorkflowRunID || detail.Run.ProjectID != command.ProjectID ||
		detail.Run.ConversationID != command.ConversationID || detail.Run.ResearchTaskID == "" ||
		detail.Run.Status != RunRunning || step.Status != StepRunning || step.ID != command.WorkflowStepID ||
		step.WorkflowRunID != detail.Run.ID || step.Attempt != command.Attempt || command.Attempt < 1 ||
		step.InputSHA256 != command.InputSHA256 || hashJSON(step.Input) != command.InputSHA256 {
		return StageMaterials{}, fmt.Errorf("stage material binding no longer matches the active frozen input")
	}
	execution, exists, err := s.repository.GetAIExecutionForStep(ctx, step.ID, step.Attempt)
	if err != nil {
		return StageMaterials{}, err
	}
	if !exists || command.ChatRunID == "" || execution.ChatRunID != command.ChatRunID ||
		execution.WorkflowRunID != detail.Run.ID || execution.WorkflowStepID != step.ID ||
		execution.Attempt != step.Attempt || execution.InputSHA256 != command.InputSHA256 {
		return StageMaterials{}, fmt.Errorf("stage material execution binding is stale")
	}
	if s.tools == nil {
		return result, nil
	}
	return s.collectStageMaterials(ctx, detail, *step, node, command, s.tools.ListBySubject)
}

type stageMaterialCallReader func(context.Context, tool.SubjectKind, string) ([]tool.Call, error)

func (s *RuntimeService) collectStageMaterials(ctx context.Context, detail RunDetail, step Step, node CompiledNode, command StageMaterialRequest, readCalls stageMaterialCallReader) (StageMaterials, error) {
	result := StageMaterials{Binding: command}
	allowed := map[string]bool{}
	for _, def := range node.AllowedTools {
		allowed[def.QualifiedName] = true
	}
	currentSkills := map[string]skillrun.Snapshot{}
	expectedSkills := availableWorkflowSkills(detail.Run.Inputs, step, node)
	if s.skills != nil && allowed["builtin.skill.load"] && len(expectedSkills) > 0 {
		loaded, err := s.skills.ListRunSkillSnapshots(ctx, command.ChatRunID)
		if err != nil {
			if ctx.Err() != nil {
				return StageMaterials{}, ctx.Err()
			}
			loaded = nil // Optional reuse cannot replace original Skill loading/validation.
		}
		for _, skill := range loaded {
			expected, ok := expectedSkills[skill.Name]
			if ok && skill.ContentHash != "" && skill.PackageHash != "" && skill.ContentHash == expected.ContentHash && skill.PackageHash == expected.PackageHash {
				currentSkills[skill.Name] = skill
			}
		}
	}
	files := map[string]string{}
	if allowed["builtin.workspace.read_text"] {
		inputs, err := ResearchEffectiveInputs(detail)
		if err != nil {
			return StageMaterials{}, err
		}
		for _, path := range exactWorkflowInputPaths(inputs) {
			if digest, ok := workflowSnapshotExpectedHash(path); ok && validStageMaterialPath(path) {
				files[path] = digest
			}
		}
		// Only host tool outputs whose exact identities are still present in
		// this stage's frozen input are eligible. An old draft's paths are not.
		for _, prior := range detail.Steps {
			if prior.Ordinal >= step.Ordinal || prior.Status != StepCompleted || prior.WorkflowRunID != detail.Run.ID || (prior.NodeKind != NodePython && prior.NodeKind != NodeTool) {
				continue
			}
			var envelope struct {
				Artifacts []tool.ArtifactRef `json:"artifacts"`
			}
			if json.Unmarshal(prior.Output, &envelope) != nil {
				continue
			}
			for _, file := range envelope.Artifacts {
				if len(file.SHA256) == 64 && validStageMaterialPath(file.WorkspacePath) && stageInputHasArtifact(step.Input, file) {
					files[file.WorkspacePath] = file.SHA256
				}
			}
		}
	}
	if len(files) == 0 && len(currentSkills) == 0 {
		return result, nil
	}
	priorSteps := append([]Step(nil), detail.Steps...)
	sort.SliceStable(priorSteps, func(i, j int) bool { return priorSteps[i].Ordinal > priorSteps[j].Ordinal })
	seen, total := map[string]bool{}, 0
	for _, prior := range priorSteps {
		if len(result.Sources) >= 24 || total >= 48_000 {
			break
		}
		if prior.Ordinal >= step.Ordinal || prior.Status != StepCompleted || prior.WorkflowRunID != detail.Run.ID || prior.Attempt < 1 || hashJSON(prior.Input) != prior.InputSHA256 {
			continue
		}
		execution, exists, err := s.repository.GetAIExecutionForStep(ctx, prior.ID, prior.Attempt)
		if err != nil {
			if ctx.Err() != nil {
				return StageMaterials{}, ctx.Err()
			}
			continue // Unavailable historical source is absent, never "already read".
		}
		if !exists || execution.Status != "completed" || execution.WorkflowRunID != detail.Run.ID || execution.WorkflowStepID != prior.ID || execution.Attempt != prior.Attempt || execution.InputSHA256 != prior.InputSHA256 || execution.ChatRunID == "" {
			continue
		}
		priorSkills := map[string]skillrun.Snapshot{}
		if s.skills != nil && len(currentSkills) > 0 {
			loaded, err := s.skills.ListRunSkillSnapshots(ctx, execution.ChatRunID)
			if err != nil {
				if ctx.Err() != nil {
					return StageMaterials{}, ctx.Err()
				}
				loaded = nil
			}
			for _, skill := range loaded {
				priorSkills[skill.Name] = skill
			}
		}
		if len(files) == 0 {
			matching := false
			for name, skill := range priorSkills {
				if actual, exists := currentSkills[name]; exists && actual == skill {
					matching = true
					break
				}
			}
			if !matching {
				continue
			}
		}
		calls, err := readCalls(ctx, tool.SubjectChatRun, execution.ChatRunID)
		if err != nil {
			if ctx.Err() != nil {
				return StageMaterials{}, ctx.Err()
			}
			continue
		}
		for _, call := range calls {
			if call.RunID != execution.ChatRunID || tool.NormalizeSubjectKind(call.SubjectKind) != tool.SubjectChatRun {
				continue
			}
			material, ok := stageMaterialFromCall(call, files, currentSkills, priorSkills)
			if !ok {
				continue
			}
			key := material.Kind + ":" + material.Locator + ":" + material.SHA256
			if seen[key] || len(material.Content) > 12_000 || total+len(material.Content) > 48_000 || len(result.Sources) >= 24 {
				continue
			}
			seen[key] = true
			material.SourceStepID, material.SourceAttempt, material.SourceCallID = prior.ID, prior.Attempt, call.ID
			result.Sources = append(result.Sources, material)
			total += len(material.Content)
		}
	}
	return result, nil
}

func stageMaterialFromCall(call tool.Call, files map[string]string, current, prior map[string]skillrun.Snapshot) (StageMaterial, bool) {
	if call.ID == "" || call.Status != tool.CallCompleted || call.Result == nil || call.Result.Status != tool.ResultSuccess || call.Result.Truncated {
		return StageMaterial{}, false
	}
	name, args, source, text := call.ToolName, call.Arguments, call.Result.Structured, call.Result.Text
	if name == "builtin.resource.open" {
		var envelope struct {
			Tool   string          `json:"sourceTool"`
			Args   json.RawMessage `json:"sourceArguments"`
			Result json.RawMessage `json:"sourceResult"`
			Label  string          `json:"label"`
		}
		if json.Unmarshal(source, &envelope) != nil || envelope.Label == "" || !strings.HasPrefix(text, envelope.Label+"\n\n") {
			return StageMaterial{}, false
		}
		name, args, source, text = envelope.Tool, envelope.Args, envelope.Result, strings.TrimPrefix(text, envelope.Label+"\n\n")
	}
	switch name {
	case "builtin.skill.load":
		var a struct {
			Name, Section string
			Offset        int
		}
		var v struct {
			Name, Section, Mode       string
			Offset, NextOffset, Total int
			Truncated, Loaded         bool
		}
		if json.Unmarshal(args, &a) != nil || json.Unmarshal(source, &v) != nil || a.Name != v.Name || a.Section == "" || a.Section != v.Section || a.Offset != 0 || v.Offset != 0 || !v.Loaded || v.Mode != "section" || v.Truncated || v.NextOffset != v.Total {
			return StageMaterial{}, false
		}
		identity, ok := current[v.Name]
		if !ok || prior[v.Name] != identity {
			return StageMaterial{}, false
		}
		// Extract just the original section, not its old stage's wrappers,
		// action menu, capability diagnostics or model commentary.
		start, end := strings.Index(text, "<skill_instructions>\n"), strings.LastIndex(text, "\n</skill_instructions>")
		if start < 0 || end < start {
			return StageMaterial{}, false
		}
		content := text[start+len("<skill_instructions>\n") : end]
		if !utf8.ValidString(content) || utf8.RuneCountInString(content) != v.Total {
			return StageMaterial{}, false
		}
		return StageMaterial{Kind: "skill_section", Locator: v.Name + "/" + v.Section, Content: content, SHA256: hashBytes([]byte(content)), SkillName: v.Name, SkillContentHash: identity.ContentHash, SkillPackageHash: identity.PackageHash}, true
	case "builtin.workspace.read_text":
		var a struct {
			Path   string
			Offset int
		}
		var v struct {
			Path, Content            string
			BytesRead, OriginalBytes int
			Truncated                bool
		}
		if json.Unmarshal(args, &a) != nil || json.Unmarshal(source, &v) != nil || a.Offset != 0 || v.Truncated || a.Path != v.Path || !utf8.ValidString(v.Content) || len(v.Content) != v.BytesRead || v.BytesRead != v.OriginalBytes {
			return StageMaterial{}, false
		}
		digest := hashBytes([]byte(v.Content))
		if files[v.Path] != digest {
			return StageMaterial{}, false
		}
		return StageMaterial{Kind: "workspace_snapshot", Locator: v.Path, Content: v.Content, SHA256: digest}, true
	}
	return StageMaterial{}, false
}

func validStageMaterialPath(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(path))
	return path != "" && clean == path && !strings.HasPrefix(path, "/") && !filepath.IsAbs(path) && filepath.VolumeName(path) == "" && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../") && !workflowPrivatePath(clean)
}

func stageInputHasArtifact(input json.RawMessage, file tool.ArtifactRef) bool {
	var value any
	if json.Unmarshal(input, &value) != nil {
		return false
	}
	var find func(any) bool
	find = func(value any) bool {
		switch item := value.(type) {
		case map[string]any:
			if item["workspacePath"] == file.WorkspacePath && item["sha256"] == file.SHA256 && item["sizeBytes"] == float64(file.SizeBytes) {
				return true
			}
			for _, child := range item {
				if find(child) {
					return true
				}
			}
		case []any:
			for _, child := range item {
				if find(child) {
					return true
				}
			}
		}
		return false
	}
	return find(value)
}

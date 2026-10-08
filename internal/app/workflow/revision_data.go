package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/tools/pathguard"
)

// The selection is a complete data set, not an ambiguous append/replace flag.
// Keep existing frozen paths explicitly; new files must be sent in this chat.
type RevisionDataSelection struct {
	RetainedPaths []string `json:"retainedPaths"`
	AttachmentIDs []string `json:"attachmentIds"`
}

type RevisionDataFile struct {
	InputFile
	AttachmentID string `json:"attachmentId,omitempty"`
}

type ResearchDataVersion struct {
	PreviousPaths []string           `json:"previousPaths"`
	Files         []RevisionDataFile `json:"files"`
}

type revisionDataService interface {
	OpenRevisionData(context.Context, string, string, string) (attachment.Attachment, *os.File, error)
}

// Data updates reuse the actual dynamic table pipeline. This is structural
// compatibility with its input port, not a research-direction whitelist.
func revisionDataChain(d RunDetail) bool {
	if d.Run.ResearchTaskID == "" {
		return false
	}
	nodes := compilationNodeMap(d.Run.Compilation)
	preflight, implementation, analysis := findStepByNode(d.Steps, "data_preflight"), findStepByNode(d.Steps, "method_implementation"), findStepByNode(d.Steps, "python_analysis")
	if preflight == nil || implementation == nil || analysis == nil || preflight.Ordinal >= implementation.Ordinal || implementation.Ordinal >= analysis.Ordinal || nodes["data_preflight"].Kind != NodePython || nodes["python_analysis"].Kind != NodePython {
		return false
	}
	for _, port := range d.Run.Compilation.Inputs {
		if port.Name == "input_paths" && port.Type == TypeArray && port.FileKind == "tabular" {
			for _, id := range []string{"data_preflight", "python_analysis"} {
				found := false
				for _, edge := range d.Run.Compilation.Edges {
					if edge.ToNode == id && edge.ToPort == "inputPaths" {
						if edge.FromNode != "$input" || edge.FromPort != "input_paths" {
							return false
						}
						found = true
					}
				}
				if !found {
					return false
				}
			}
			return true
		}
	}
	return false
}

func researchDataFileInfo(selected project.Project, taskID, path string) (RevisionDataFile, error) {
	root, err := project.ResearchTaskWorkspacePath(selected, taskID)
	if err != nil {
		return RevisionDataFile{}, err
	}
	guard, err := pathguard.Open(root)
	if err != nil {
		return RevisionDataFile{}, err
	}
	defer guard.Close()
	file, _, err := guard.OpenFile(path)
	if err != nil {
		return RevisionDataFile{}, err
	}
	defer file.Close()
	if err := verifyContentAddressedWorkflowInput(file, path); err != nil {
		return RevisionDataFile{}, err
	}
	info, err := file.Stat()
	if err != nil {
		return RevisionDataFile{}, err
	}
	sha, ok := workflowSnapshotExpectedHash(path)
	if !ok {
		return RevisionDataFile{}, fmt.Errorf("保留的数据不是冻结输入")
	}
	return RevisionDataFile{InputFile: InputFile{RelativePath: path, Name: workflowSnapshotBase(filepath.Base(path), filepath.Ext(path)) + filepath.Ext(path), SizeBytes: info.Size(), SHA256: sha, Staged: true}}, nil
}

func researchDataInputsRecord(d RunDetail) (json.RawMessage, error) {
	inputs, err := ResearchEffectiveInputs(d)
	if err != nil {
		return nil, err
	}
	versions := []map[string]any{}
	for _, event := range d.Events {
		if event.Type != "workflow.user_revision_queued" {
			continue
		}
		var saved struct {
			Proposal ResearchRevisionProposal `json:"proposal"`
		}
		if err := json.Unmarshal(event.Payload, &saved); err != nil {
			return nil, err
		}
		if saved.Proposal.DataVersion != nil {
			versions = append(versions, map[string]any{"revisionId": saved.Proposal.ID, "confirmedAt": event.CreatedAt, "data": saved.Proposal.DataVersion})
		}
	}
	return rawObject(map[string]any{"originalPaths": exactWorkflowInputPaths(d.Run.Inputs), "currentPaths": exactWorkflowInputPaths(inputs), "versions": versions,
		"instruction": "Only confirmed versions are effective. A dataInputs proposal specifies the complete data set: retainedPaths from currentPaths plus attachmentIds from user-sent supplemental_materials. Unlisted old files are not used, but remain preserved. Tables are not auto-merged; describe their roles and any joins in changes. Confirmation replays from data_preflight."}), nil
}

func currentResearchDataVersion(d RunDetail) (*ResearchDataVersion, error) {
	for i := len(d.Events) - 1; i >= 0; i-- {
		if d.Events[i].Type != "workflow.user_revision_queued" {
			continue
		}
		var saved struct {
			Proposal ResearchRevisionProposal `json:"proposal"`
		}
		if err := json.Unmarshal(d.Events[i].Payload, &saved); err != nil {
			return nil, fmt.Errorf("decode confirmed revision: %w", err)
		}
		if saved.Proposal.DataVersion != nil {
			return saved.Proposal.DataVersion, nil
		}
	}
	return nil, nil
}

// Never mutates the original run input snapshot. A later wording-only or
// evidence revision continues using the last CONFIRMED data version.
func ResearchEffectiveInputs(d RunDetail) (json.RawMessage, error) {
	version, err := currentResearchDataVersion(d)
	if err != nil {
		return nil, err
	}
	if version == nil {
		return cloneRaw(d.Run.Inputs), nil
	}
	if !revisionDataChain(d) {
		return nil, fmt.Errorf("当前路线不能续接已确认的数据版本")
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(d.Run.Inputs, &values); err != nil || values == nil {
		return nil, fmt.Errorf("原始研究输入不是有效对象")
	}
	paths := make([]string, 0, len(version.Files))
	for _, file := range version.Files {
		sha, ok := workflowSnapshotExpectedHash(file.RelativePath)
		if !ok || sha != file.SHA256 {
			return nil, fmt.Errorf("数据版本文件身份与冻结路径不一致")
		}
		paths = append(paths, file.RelativePath)
	}
	values["input_paths"] = mustJSON(paths)
	raw, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	if _, _, err := validateRuntimeInputs(d.Run.Compilation.Inputs, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func (s *RuntimeService) prepareRevisionData(ctx context.Context, d RunDetail, command ProposeResearchRevisionCommand) (*ResearchDataVersion, error) {
	if command.DataInputs == nil {
		return nil, nil
	}
	if command.NodeID != "data_preflight" || !revisionDataChain(d) {
		return nil, fmt.Errorf("更新研究数据须从数据预检续做，不能只修改报告或绕过计算阶段")
	}
	selection := command.DataInputs
	if selection.RetainedPaths == nil || selection.AttachmentIDs == nil {
		return nil, fmt.Errorf("请显式列出完整数据集合：retainedPaths 与 attachmentIds（没有时填 []）")
	}
	if len(selection.RetainedPaths)+len(selection.AttachmentIDs) == 0 || len(selection.RetainedPaths)+len(selection.AttachmentIDs) > 16 {
		return nil, fmt.Errorf("确认后使用的数据须为 1 至 16 份表格")
	}
	inputs, err := ResearchEffectiveInputs(d)
	if err != nil {
		return nil, err
	}
	previous := exactWorkflowInputPaths(inputs)
	version := &ResearchDataVersion{PreviousPaths: previous, Files: []RevisionDataFile{}}
	known := map[string]bool{}
	for _, path := range selection.RetainedPaths {
		if !slices.Contains(previous, path) || known[path] {
			return nil, fmt.Errorf("保留的数据必须来自当前有效输入，且不能重复")
		}
		known[path] = true
		selected, err := s.projects.Get(ctx, d.Run.ProjectID)
		if err != nil {
			return nil, err
		}
		file, err := researchDataFileInfo(selected, d.Run.ResearchTaskID, path)
		if err != nil {
			return nil, err
		}
		version.Files = append(version.Files, file)
	}
	if len(selection.AttachmentIDs) > 0 {
		repo, ok := s.repository.(discussionMaterialsRepository)
		if !ok {
			return nil, fmt.Errorf("discussion material storage unavailable")
		}
		allowed, err := repo.DiscussionAttachmentIDs(ctx, d.Run.ID)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, id := range selection.AttachmentIDs {
			if !slices.Contains(allowed, id) || seen[id] {
				return nil, fmt.Errorf("新数据必须由用户在当前任务对话中发送，且不能重复")
			}
			seen[id] = true
			file, err := s.stageRevisionData(ctx, d, id)
			if err != nil {
				return nil, err
			}
			if known[file.RelativePath] {
				return nil, fmt.Errorf("数据集合包含重复文件，请只保留一份")
			}
			known[file.RelativePath] = true
			version.Files = append(version.Files, file)
		}
	}
	// Check the existing frozen port contract before a card can discard the
	// current delivery. Do not defer an incompatible file count until replay.
	candidate := d
	candidate.Events = append(append([]RuntimeEvent(nil), d.Events...), RuntimeEvent{Type: "workflow.user_revision_queued", Payload: rawObject(map[string]any{"proposal": ResearchRevisionProposal{DataVersion: version}})})
	if _, err := ResearchEffectiveInputs(candidate); err != nil {
		return nil, err
	}
	return version, nil
}

func (s *RuntimeService) stageRevisionData(ctx context.Context, d RunDetail, id string) (RevisionDataFile, error) {
	loader, ok := s.materialLoader.(revisionDataService)
	if !ok {
		return RevisionDataFile{}, fmt.Errorf("revision data service unavailable")
	}
	v, source, err := loader.OpenRevisionData(ctx, d.Run.ProjectID, d.Run.ConversationID, id)
	if err != nil {
		return RevisionDataFile{}, err
	}
	defer source.Close()
	selected, err := s.projects.Get(ctx, d.Run.ProjectID)
	if err != nil {
		return RevisionDataFile{}, err
	}
	file, err := (&Service{}).stageOpenedInputFile(selected, source, "", v.OriginalName, "tabular", d.Run.ResearchTaskID, v.SHA256)
	if err != nil {
		return RevisionDataFile{}, err
	}
	if file.SizeBytes != v.SizeBytes {
		return RevisionDataFile{}, fmt.Errorf("补充数据大小与导入记录不一致")
	}
	return RevisionDataFile{InputFile: file, AttachmentID: id}, nil
}

func (s *RuntimeService) validateRevisionData(ctx context.Context, d RunDetail, p ResearchRevisionProposal) error {
	if p.DataVersion == nil {
		return nil
	}
	selection := &RevisionDataSelection{RetainedPaths: []string{}, AttachmentIDs: []string{}}
	for _, file := range p.DataVersion.Files {
		if file.AttachmentID == "" {
			selection.RetainedPaths = append(selection.RetainedPaths, file.RelativePath)
		} else {
			selection.AttachmentIDs = append(selection.AttachmentIDs, file.AttachmentID)
		}
	}
	version, err := s.prepareRevisionData(ctx, d, ProposeResearchRevisionCommand{NodeID: p.NodeID, DataInputs: selection})
	if err != nil {
		return err
	}
	if !rawJSONEqual(mustJSON(version), mustJSON(p.DataVersion)) {
		return fmt.Errorf("数据版本已变化，请重新生成返修方案")
	}
	return nil
}

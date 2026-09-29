package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/attachment"
)

type RevisionMaterial struct {
	AttachmentID string `json:"attachmentId"`
	SHA256       string `json:"sha256"`
	Name         string `json:"name"`
}

type discussionMaterialsRepository interface {
	DiscussionAttachmentIDs(context.Context, string) ([]string, error)
}
type revisionMaterialService interface {
	RevisionMaterials(context.Context, string, string, []string) ([]attachment.Attachment, error)
	PromoteRevisionMaterial(context.Context, string, string, string, string, string) (attachment.Attachment, error)
}

func revisionSuffixSideEffects(d RunDetail, ordinal int) bool {
	nodes := compilationNodeMap(d.Run.Compilation)
	for _, step := range d.Steps {
		if step.Ordinal >= ordinal && nodes[step.NodeID].SideEffect {
			return true
		}
	}
	return false
}

// Only the explicit selected-material route may accept supplemental documents.
func revisionMaterialChain(d RunDetail) bool {
	nodes := compilationNodeMap(d.Run.Compilation)
	ids := []string{"evidence_import", "evidence_sync", "evidence_search", "evidence_screening"}
	names := []string{"builtin.research.workflow.import", "builtin.research.workflow.sync", "builtin.knowledge.search"}
	first := findStepByNode(d.Steps, ids[0])
	if first == nil {
		return false
	}
	for i, id := range ids {
		n := nodes[id]
		step := findStepByNode(d.Steps, id)
		if step == nil || step.Ordinal != first.Ordinal+i {
			return false
		}
		if i < 3 {
			if n.Kind != NodeTool || n.Tool == nil || n.Tool.QualifiedName != names[i] {
				return false
			}
		} else if n.Kind != NodeAgentStage || n.PromptVersion != selectedEvidenceVersion {
			return false
		}
	}
	for _, want := range []Edge{
		{FromNode: ids[0], FromPort: "structured.attachmentIds", ToNode: ids[1], ToPort: "attachmentIds"},
		{FromNode: ids[1], FromPort: "structured.documentIds", ToNode: ids[2], ToPort: "documentIds"},
		{FromNode: ids[2], FromPort: "citations", ToNode: ids[3], ToPort: "candidates"},
		{FromNode: ids[0], FromPort: "structured.materials", ToNode: ids[3], ToPort: "importedMaterials"},
		{FromNode: ids[1], FromPort: "structured.documentIds", ToNode: ids[3], ToPort: "documentIds"},
	} {
		count := 0
		for _, e := range d.Run.Compilation.Edges {
			if e.ToNode == want.ToNode && e.ToPort == want.ToPort {
				if e.FromNode != want.FromNode || e.FromPort != want.FromPort {
					return false
				}
				count++
			}
		}
		if count != 1 {
			return false
		}
	}
	return true
}

func validateRevisionMaterialSelection(command ProposeResearchRevisionCommand) error {
	// An omitted selection is not explicit consent to import (or omit) files.
	// Old completed proposals remain immutable; new import proposals must state
	// the selection, including [] when intentionally reusing existing materials.
	if command.NodeID == "evidence_import" && command.AttachmentIDs == nil {
		return fmt.Errorf("材料导入返修方案必须显式提供 attachmentIds：先读取 supplemental_materials，选择需要纳入的附件 ID；若不新增材料请明确传 []。仅在 summary/changes 中提到文件不会导入，确认卡必须列出实际补充文献")
	}
	return nil
}

func (s *RuntimeService) prepareRevisionMaterials(ctx context.Context, d RunDetail, command ProposeResearchRevisionCommand) ([]RevisionMaterial, error) {
	if len(command.AttachmentIDs) == 0 {
		return nil, nil
	}
	if command.NodeID != "evidence_import" || !revisionMaterialChain(d) {
		return nil, fmt.Errorf("补充文献须从导入所选研究材料开始返修，不能仅改报告或替换研究数据")
	}
	repo, ok := s.repository.(discussionMaterialsRepository)
	if !ok {
		return nil, fmt.Errorf("discussion material storage unavailable")
	}
	allowed, err := repo.DiscussionAttachmentIDs(ctx, d.Run.ID)
	if err != nil {
		return nil, err
	}
	set := stringSetOf(allowed)
	for _, id := range command.AttachmentIDs {
		if !set[id] {
			return nil, fmt.Errorf("补充资料必须由用户在当前任务对话中明确发送")
		}
	}
	service, ok := s.materialLoader.(revisionMaterialService)
	if !ok {
		return nil, fmt.Errorf("revision material service unavailable")
	}
	values, err := service.RevisionMaterials(ctx, d.Run.ProjectID, d.Run.ConversationID, command.AttachmentIDs)
	if err != nil {
		return nil, err
	}
	// Reject an impossible plan before showing its confirmation card, not after
	// discarding the current delivery. Existing material bytes are counted once.
	importStep := findStepByNode(d.Steps, "evidence_import")
	base := decodeObject(importStep.Input)
	baseIDs := stringSliceValue(base["selectedAttachmentIds"])
	existing, err := s.materialLoader.ReferenceMaterials(ctx, d.Run.ProjectID, d.Run.ResearchTaskID, baseIDs)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, v := range existing {
		seen[v.SHA256] = true
	}
	count := len(baseIDs)
	for _, v := range values {
		if !seen[v.SHA256] {
			count++
			seen[v.SHA256] = true
		}
	}
	if count > 16 || count+len(stringSliceValue(base["selectedCandidateIds"])) > 100 {
		return nil, fmt.Errorf("补充后超出参考资料上限（16 份用户资料、100 份总材料），请减少新增文献")
	}
	result := []RevisionMaterial{}
	for _, v := range values {
		result = append(result, RevisionMaterial{AttachmentID: v.ID, SHA256: v.SHA256, Name: v.OriginalName})
	}
	return result, nil
}

func (s *RuntimeService) validateRevisionMaterials(ctx context.Context, d RunDetail, p ResearchRevisionProposal) error {
	ids := []string{}
	for _, v := range p.Materials {
		ids = append(ids, v.AttachmentID)
	}
	values, err := s.prepareRevisionMaterials(ctx, d, ProposeResearchRevisionCommand{NodeID: p.NodeID, AttachmentIDs: ids})
	if err != nil {
		return err
	}
	for i, v := range values {
		if v != p.Materials[i] {
			return fmt.Errorf("补充材料快照已变化，请重新生成返修方案")
		}
	}
	return nil
}

// Promotion happens after confirmation, inside the normal retryable import
// step. Confirming the card never silently mutates frozen workflow inputs.
func (s *RuntimeService) bindRevisionMaterials(ctx context.Context, d RunDetail, n CompiledNode, input []byte) ([]byte, error) {
	if n.ID != "evidence_import" {
		return input, nil
	}
	var materials []RevisionMaterial
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
		materials = append(materials, saved.Proposal.Materials...)
	}
	if len(materials) == 0 {
		return input, nil
	}
	if !revisionMaterialChain(d) {
		return nil, fmt.Errorf("补充材料的索引与综合路线已变化")
	}
	service, ok := s.materialLoader.(revisionMaterialService)
	if !ok {
		return nil, fmt.Errorf("revision material service unavailable")
	}
	args := decodeObject(input)
	ids := stringSliceValue(args["selectedAttachmentIds"])
	knownHashes := map[string]bool{}
	// Preserve the existing selected identity when the same bytes are supplied
	// again from the library or a conversation upload.
	if s.materialLoader != nil && len(ids) > 0 {
		values, err := s.materialLoader.ReferenceMaterials(ctx, d.Run.ProjectID, d.Run.ResearchTaskID, ids)
		if err != nil {
			return nil, err
		}
		for _, v := range values {
			knownHashes[v.SHA256] = true
		}
	}
	for _, m := range materials {
		if knownHashes[m.SHA256] {
			continue
		}
		v, err := service.PromoteRevisionMaterial(ctx, d.Run.ProjectID, d.Run.ConversationID, d.Run.ResearchTaskID, m.AttachmentID, m.SHA256)
		if err != nil {
			return nil, err
		}
		ids = appendUnique(ids, v.ID)
		knownHashes[m.SHA256] = true
	}
	if len(ids) > 16 || len(ids)+len(stringSliceValue(args["selectedCandidateIds"])) > 100 {
		return nil, fmt.Errorf("补充后超出参考资料上限（16 份用户资料、100 份总材料）；请调整返修方案")
	}
	args["selectedAttachmentIds"] = ids
	return mustJSON(args), nil
}

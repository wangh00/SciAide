package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
)

type evidenceMaterialReader interface {
	GetCandidateForTask(context.Context, string, string, string) (research.Candidate, error)
}
type evidenceRefreshRepository interface {
	QueueEvidenceRefresh(context.Context, string, string, string, int, time.Time, RuntimeEvent) error
}

func (s *RuntimeService) SetEvidenceMaterialReader(reader evidenceMaterialReader) {
	s.evidenceMaterials = reader
}

// Refresh failures must be visible and retryable, not leave a driverless run.
// A concurrent pause/cancel wins; the saved material remains for later recovery.
func (s *RuntimeService) finishEvidenceRefresh(ctx context.Context, detail RunDetail, step Step, cause error) error {
	if cause == nil || ctx.Err() != nil {
		return cause
	}
	current, err := s.repository.GetRun(ctx, detail.Run.ProjectID, detail.Run.ID)
	if err != nil {
		return err
	}
	if current.Run.Status != RunRunning || current.Run.CancelRequested {
		return nil
	}
	return s.failDrive(ctx, detail, step, "WORKFLOW_EVIDENCE_REFRESH_FAILED", "补充全文已保存，但更新引用未完成；可重试当前阶段。原因："+cause.Error(), StepFailed, RunFailed)
}

// Compare persistent task material identity with the immutable input, not a
// model-controlled flag. This also detects a saved download after cancellation.
func (s *RuntimeService) changedEvidenceMaterials(ctx context.Context, detail RunDetail, step Step, node CompiledNode) ([]map[string]string, error) {
	if node.ID != "evidence_screening" || node.PromptVersion != selectedEvidenceVersion {
		return nil, nil
	}
	if s.evidenceMaterials == nil {
		return nil, nil
	}
	var input struct {
		Materials []struct {
			CandidateID  string `json:"candidateId"`
			AttachmentID string `json:"attachmentId"`
		} `json:"importedMaterials"`
	}
	if err := json.Unmarshal(step.Input, &input); err != nil {
		return nil, err
	}
	changes := []map[string]string{}
	for _, m := range input.Materials {
		if m.CandidateID == "" {
			continue
		}
		current, err := s.evidenceMaterials.GetCandidateForTask(ctx, detail.Run.ProjectID, m.CandidateID, detail.Run.ResearchTaskID)
		if err != nil {
			return nil, fmt.Errorf("check updated evidence: %w", err)
		}
		if current.ImportStatus == research.ImportImported && current.ImportKind == research.ImportFullText && current.AttachmentID != "" && current.AttachmentID != m.AttachmentID {
			changes = append(changes, map[string]string{"candidateId": m.CandidateID, "previousAttachmentId": m.AttachmentID, "attachmentId": current.AttachmentID})
		}
	}
	return changes, nil
}

func (s *RuntimeService) refreshChangedEvidence(ctx context.Context, detail RunDetail, step Step, node CompiledNode) (bool, error) {
	changes, err := s.changedEvidenceMaterials(ctx, detail, step, node)
	if err != nil || len(changes) == 0 {
		return false, err
	}
	// The trusted dynamic route contains only import, sync, search and screening
	// here. Never reuse this operation for arbitrary user-designed side effects.
	ids := []string{"evidence_import", "evidence_sync", "evidence_search", "evidence_screening"}
	first := findNodeStep(detail.Steps, ids[0])
	if first == nil || step.Ordinal-first.Ordinal != len(ids)-1 {
		return false, fmt.Errorf("evidence refresh dependency chain changed")
	}
	nodes := compilationNodeMap(detail.Run.Compilation)
	expected := []string{"builtin.research.workflow.import", "builtin.research.workflow.sync", "builtin.knowledge.search"}
	for i, id := range ids {
		n := nodes[id]
		if i < 3 {
			if n.Kind != NodeTool || n.Tool == nil || n.Tool.QualifiedName != expected[i] || s.registry == nil {
				return false, fmt.Errorf("untrusted evidence refresh node")
			}
			live, err := s.registry.Definition(ctx, expected[i])
			if err != nil || tool.DefinitionFingerprint(live) != tool.DefinitionFingerprint(definitionFromSnapshot(*n.Tool)) {
				return false, fmt.Errorf("evidence refresh tool contract changed")
			}
		} else if n.Kind != NodeAgentStage || n.PromptVersion != selectedEvidenceVersion {
			return false, fmt.Errorf("untrusted evidence synthesis node")
		}
		v := findNodeStep(detail.Steps, id)
		if v == nil || v.Ordinal != first.Ordinal+i || (i < len(ids)-1 && v.Status != StepCompleted) {
			return false, fmt.Errorf("evidence refresh requires completed material dependencies")
		}
	}
	requiredEdges := []Edge{{FromNode: ids[0], FromPort: "structured.materials", ToNode: ids[3], ToPort: "importedMaterials"}, {FromNode: ids[1], FromPort: "structured.documentIds", ToNode: ids[3], ToPort: "documentIds"}, {FromNode: ids[0], FromPort: "structured.attachmentIds", ToNode: ids[1], ToPort: "attachmentIds"}, {FromNode: ids[1], FromPort: "structured.documentIds", ToNode: ids[2], ToPort: "documentIds"}, {FromNode: ids[2], FromPort: "citations", ToNode: ids[3], ToPort: "candidates"}}
	for _, want := range requiredEdges {
		count := 0
		for _, e := range detail.Run.Compilation.Edges {
			if e.ToNode == want.ToNode && e.ToPort == want.ToPort {
				if e.FromNode != want.FromNode || e.FromPort != want.FromPort {
					return false, fmt.Errorf("evidence refresh source changed")
				}
				count++
			}
		}
		if count != 1 {
			return false, fmt.Errorf("evidence refresh dependency missing")
		}
	}
	repo, ok := s.repository.(evidenceRefreshRepository)
	if !ok {
		return false, fmt.Errorf("atomic evidence refresh is unavailable")
	}
	event, err := s.event(detail.Run.ID, "workflow.evidence_refreshed", map[string]any{"stepId": step.ID, "attempt": step.Attempt, "inputSha256": step.InputSHA256, "changes": changes, "message": "已保存补充全文，正在更新引用并重新综合；保留原材料，不重复文献检索。"}, s.now())
	if err != nil {
		return false, err
	}
	return true, repo.QueueEvidenceRefresh(ctx, detail.Run.ID, first.ID, step.ID, step.Attempt, s.now(), event)
}

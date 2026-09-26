package research

import (
	"context"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/attachment"
)

// CollectCandidate is an explicit user-library action, independent of the
// research inclusion decision and task import state. It never changes either.
func (s *DiscoveryService) CollectCandidate(ctx context.Context, projectID, candidateID, taskID string) (attachment.Material, error) {
	projectID, candidateID, taskID = strings.TrimSpace(projectID), strings.TrimSpace(candidateID), strings.TrimSpace(taskID)
	p, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return attachment.Material{}, err
	}
	if taskID != "" {
		if err = s.validateTask(ctx, projectID, taskID); err != nil {
			return attachment.Material{}, err
		}
	}
	candidate, err := s.GetCandidateForTask(ctx, projectID, candidateID, taskID)
	if err != nil {
		return attachment.Material{}, err
	}
	collector, ok := s.attachments.(interface {
		GetMaterial(context.Context, string, string, string) (attachment.Material, error)
		CollectMaterial(context.Context, string, string, string) (attachment.Material, error)
	})
	if !ok || s.materializer == nil || s.knowledge == nil {
		return attachment.Material{}, fmt.Errorf("资料库导入服务未配置")
	}
	sourceID := candidate.AttachmentID
	if sourceID != "" {
		if _, err = collector.GetMaterial(ctx, projectID, taskID, sourceID); err != nil {
			return attachment.Material{}, err
		}
	} else {
		// Preserve the candidate snapshot without requiring inclusion or a PDF download.
		// The material is stored separately from task imports, so saving a reference
		// cannot change the inputs of an already running research workflow.
		staged, e := s.materializer.Materialize(ctx, p, candidate, MaterializeMetadata)
		if e != nil {
			return attachment.Material{}, e
		}
		defer s.materializer.Cleanup(staged)
		imported, e := s.attachments.ImportResearchStaged(ctx, projectID, staged.Path, staged.Name)
		if e != nil {
			return attachment.Material{}, e
		}
		if staged.SHA256 != "" && !strings.EqualFold(staged.SHA256, imported.SHA256) {
			return attachment.Material{}, fmt.Errorf("文献材料哈希校验失败")
		}
		sourceID = imported.ID
	}
	material, err := collector.CollectMaterial(ctx, projectID, taskID, sourceID)
	if err != nil {
		return material, err
	}
	if err = s.knowledge.Enqueue(ctx, material.Attachment); err != nil {
		return material, fmt.Errorf("已加入资料库，但索引准备失败，可重新点击加入重试：%w", err)
	}
	return material, nil
}

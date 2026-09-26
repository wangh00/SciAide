package attachment

import (
	"context"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/document"
)

// ReferenceMaterials validates every selected owner before parsing any bytes.
// Shared source objects are reused; selection never changes their ownership.
func (s *Service) ReferenceMaterials(ctx context.Context, projectID, taskID string, ids []string) ([]Attachment, error) {
	if len(ids) > 16 {
		return nil, fmt.Errorf("一次最多选择 16 份参考资料")
	}
	projectID, taskID = strings.TrimSpace(projectID), strings.TrimSpace(taskID)
	if projectID == "" {
		return nil, fmt.Errorf("project id is required")
	}
	if taskID != "" {
		if err := s.validateTask(ctx, projectID, taskID); err != nil {
			return nil, err
		}
	}
	values := []Attachment{}
	seen := map[string]bool{}
	for _, id := range ids {
		if id != strings.TrimSpace(id) {
			return nil, fmt.Errorf("参考资料标识不能包含首尾空格")
		}
		if seen[id] || strings.TrimSpace(id) == "" {
			return nil, fmt.Errorf("参考资料标识无效或重复")
		}
		seen[id] = true
		v, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if v.ProjectID != projectID || (v.ScopeKind != ScopeProjectShared && !(taskID != "" && v.ScopeKind == ScopeTask && v.ResearchTaskID == taskID)) {
			return nil, fmt.Errorf("参考资料不属于当前项目或任务")
		}
		if v.Status != StatusReady || v.Format == document.FormatImage {
			return nil, fmt.Errorf("参考资料尚未解析完成：%s", v.OriginalName)
		}
		values = append(values, v)
	}
	for i, v := range values {
		parsed, _, err := s.Parsed(ctx, projectID, v.ID)
		if err != nil {
			return nil, err
		}
		values[i] = parsed
	}
	return values, nil
}

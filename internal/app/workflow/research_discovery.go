package workflow

import (
	"context"
	"fmt"
)

const ResearchDiscoverySearchTool = "builtin.research.discovery.search"
const ResearchDiscoveryReadTool = "builtin.research.discovery.read"

type researchDiscussionToolValidator interface {
	ValidateResearchDiscussionTool(context.Context, string, string, string, string, string) error
}

// This is the existing discussion's persisted identity, not a task id supplied
// by model arguments. Discovery cannot confer route or material permissions.
func (s *RuntimeService) ValidateResearchDiscoveryTool(ctx context.Context, projectID, taskID, runID, callID, name string) error {
	if name != ResearchDiscoverySearchTool && name != ResearchDiscoveryReadTool {
		return fmt.Errorf("未知科研发现工具")
	}
	validator, ok := s.repository.(researchDiscussionToolValidator)
	if !ok {
		return fmt.Errorf("科研发现的讨论身份校验未配置")
	}
	return validator.ValidateResearchDiscussionTool(ctx, projectID, taskID, runID, callID, name)
}

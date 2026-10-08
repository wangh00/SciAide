package workflow

import (
	"context"
	"fmt"
	"strings"
)

type ExecutedPythonSource struct {
	Code     string `json:"code"`
	SHA256   string `json:"sha256"`
	FileName string `json:"fileName"`
}

// Export the frozen Kernel input, never the current AI draft or client text.
// This is a read-only projection; it neither runs code nor registers a result.
func (s *RuntimeService) ExecutedPythonCode(ctx context.Context, projectID, runID, nodeID string, attempt int) (ExecutedPythonSource, error) {
	detail, err := s.repository.GetRun(ctx, strings.TrimSpace(projectID), strings.TrimSpace(runID))
	if err != nil {
		return ExecutedPythonSource{}, err
	}
	node, exists := compilationNodeMap(detail.Run.Compilation)[nodeID]
	if !exists || node.Kind != NodePython {
		return ExecutedPythonSource{}, fmt.Errorf("selected stage is not a Python execution")
	}
	for _, step := range detail.Steps {
		if step.NodeID != nodeID || step.Attempt != attempt || step.Status != StepCompleted {
			continue
		}
		input := decodeObject(step.Input)
		output := decodeObject(step.Output)
		kernel := decodeObject(mustJSON(output["structured"]))
		code, _ := input["code"].(string)
		hash, _ := kernel["codeSha256"].(string)
		if code == "" || kernel["status"] != "success" || hashBytes([]byte(code)) != hash {
			return ExecutedPythonSource{}, fmt.Errorf("saved Python execution source is missing or does not match its hash")
		}
		return ExecutedPythonSource{Code: code, SHA256: hash, FileName: fmt.Sprintf("analysis-%s-%d.py", hash[:12], attempt)}, nil
	}
	return ExecutedPythonSource{}, fmt.Errorf("successful Python stage attempt was not found; refresh the task")
}

package project

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ResearchTaskWorkspacePath is the private, per-task execution root. It is
// deliberately below .sciaide so ordinary project files and legacy material
// cannot be mistaken for inputs of a new research task.
func ResearchTaskWorkspacePath(value Project, taskID string) (string, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || filepath.Base(taskID) != taskID || taskID == "." || taskID == ".." {
		return "", fmt.Errorf("research task id is invalid")
	}
	if strings.TrimSpace(value.WorkspacePath) == "" {
		return "", fmt.Errorf("workspace root is required")
	}
	return filepath.Join(PrivateDataPath(value), "tasks", taskID), nil
}

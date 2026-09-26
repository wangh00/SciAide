package attachment

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/project"
)

type referenceTaskValidator struct{ allowed map[string]bool }

func (v referenceTaskValidator) Exists(_ context.Context, projectID, taskID string) (bool, error) {
	return v.allowed[projectID+"/"+taskID], nil
}

func TestReferenceMaterialsRestrictsProjectAndTaskScope(t *testing.T) {
	workspace := t.TempDir()
	createPrivateFixture(t, workspace)
	repository := &attachmentMemoryRepository{values: map[string]Attachment{}}
	service := NewService(repository, attachmentProjectLoader{value: project.Project{ID: "project", WorkspacePath: workspace}})
	service.SetTaskValidator(referenceTaskValidator{allowed: map[string]bool{"project/task-a": true, "project/task-b": true}})

	sharedPath := filepath.Join(workspace, "shared.md")
	taskPath := filepath.Join(workspace, "task.md")
	if err := os.WriteFile(sharedPath, []byte("# Shared\nsource"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskPath, []byte("# Task\nsource"), 0o600); err != nil {
		t.Fatal(err)
	}
	shared, err := service.ImportPaths(context.Background(), "project", []string{sharedPath})
	if err != nil || len(shared.Attachments) != 1 {
		t.Fatalf("shared import=%#v err=%v", shared, err)
	}
	task, err := service.ImportPathsForTask(context.Background(), "project", []string{taskPath}, "task-a")
	if err != nil || len(task.Attachments) != 1 {
		t.Fatalf("task import=%#v err=%v", task, err)
	}

	selected, err := service.ReferenceMaterials(context.Background(), "project", "task-a", []string{shared.Attachments[0].ID, task.Attachments[0].ID})
	if err != nil || len(selected) != 2 || selected[0].Status != StatusReady || selected[1].Status != StatusReady {
		t.Fatalf("selected=%#v err=%v", selected, err)
	}
	if _, err := service.ReferenceMaterials(context.Background(), "project", "task-b", []string{task.Attachments[0].ID}); err == nil || !strings.Contains(err.Error(), "当前项目或任务") {
		t.Fatalf("another task accessed task-local reference: %v", err)
	}
	if _, err := service.ReferenceMaterials(context.Background(), "other-project", "", []string{shared.Attachments[0].ID}); err == nil {
		t.Fatal("another project accessed reference")
	}
}

func TestReferenceMaterialsRejectsOverLimitAndNormalizedDuplicates(t *testing.T) {
	service := &Service{repository: &attachmentMemoryRepository{values: map[string]Attachment{}}, projects: attachmentProjectLoader{value: project.Project{ID: "project"}}}
	ids := make([]string, 17)
	if _, err := service.ReferenceMaterials(context.Background(), "project", "", ids); err == nil || !strings.Contains(err.Error(), "16") {
		t.Fatalf("over-limit references accepted: %v", err)
	}
	if _, err := service.ReferenceMaterials(context.Background(), "project", "", []string{" reference-id "}); err == nil || !strings.Contains(err.Error(), "首尾空格") {
		t.Fatalf("space-padded reference accepted: %v", err)
	}
}

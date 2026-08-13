package wails

import (
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/wangh00/SciAide/internal/app/project"
)

type ProjectFacade struct {
	lifecycle *LifecycleContext
	service   *project.Service
}

type CreateProjectRequest struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	WorkspacePath string `json:"workspacePath"`
}

func NewProjectFacade(lifecycle *LifecycleContext, service *project.Service) *ProjectFacade {
	return &ProjectFacade{lifecycle: lifecycle, service: service}
}

func (f *ProjectFacade) CreateProject(request CreateProjectRequest) (project.Project, error) {
	return f.service.CreateWithWorkspace(f.lifecycle.Context(), project.CreateCommand{Name: request.Name, Description: request.Description, WorkspacePath: request.WorkspacePath})
}

func (f *ProjectFacade) ChooseWorkspaceDirectory() (string, error) {
	return runtime.OpenDirectoryDialog(f.lifecycle.Context(), runtime.OpenDialogOptions{Title: "选择科研项目目录"})
}

func (f *ProjectFacade) RemoveProject(projectID string) (project.RemoveResult, error) {
	return f.service.Remove(f.lifecycle.Context(), projectID)
}

func (f *ProjectFacade) ListProjects() ([]project.Project, error) {
	return f.service.List(f.lifecycle.Context())
}

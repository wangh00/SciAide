package wails

import (
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/pythonenv"
)

type ProjectFacade struct {
	lifecycle *LifecycleContext
	service   *project.Service
	python    *pythonenv.Service
}

type CreateProjectRequest struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	WorkspacePath string `json:"workspacePath"`
}

func NewProjectFacade(lifecycle *LifecycleContext, service *project.Service, python ...*pythonenv.Service) *ProjectFacade {
	facade := &ProjectFacade{lifecycle: lifecycle, service: service}
	if len(python) > 0 {
		facade.python = python[0]
	}
	return facade
}

func (f *ProjectFacade) CreateProject(request CreateProjectRequest) (project.Project, error) {
	return f.service.CreateWithWorkspace(f.lifecycle.Context(), project.CreateCommand{Name: request.Name, Description: request.Description, WorkspacePath: request.WorkspacePath})
}

func (f *ProjectFacade) ChooseWorkspaceDirectory() (string, error) {
	return runtime.OpenDirectoryDialog(f.lifecycle.Context(), runtime.OpenDialogOptions{Title: "选择科研项目目录"})
}

func (f *ProjectFacade) RemoveProject(projectID string) (project.RemoveResult, error) {
	result, err := f.service.Remove(f.lifecycle.Context(), projectID)
	if err != nil {
		return result, err
	}
	if f.python != nil {
		if err := f.python.CleanupRemovedProject(projectID); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (f *ProjectFacade) ListProjects() ([]project.Project, error) {
	return f.service.List(f.lifecycle.Context())
}

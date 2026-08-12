package wails

import (
	"github.com/wangh00/SciAide/internal/app/project"
)

type ProjectFacade struct {
	lifecycle *LifecycleContext
	service   *project.Service
}

type CreateProjectRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func NewProjectFacade(lifecycle *LifecycleContext, service *project.Service) *ProjectFacade {
	return &ProjectFacade{lifecycle: lifecycle, service: service}
}

func (f *ProjectFacade) CreateProject(request CreateProjectRequest) (project.Project, error) {
	return f.service.Create(f.lifecycle.Context(), request.Name, request.Description)
}

func (f *ProjectFacade) ListProjects() ([]project.Project, error) {
	return f.service.List(f.lifecycle.Context())
}

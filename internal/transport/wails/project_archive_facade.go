package wails

import (
	"path/filepath"
	"strings"
	"unicode"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/projectarchive"
)

type ProjectArchiveFacade struct {
	lifecycle *LifecycleContext
	service   *projectarchive.Service
	projects  *project.Service
}

func NewProjectArchiveFacade(lifecycle *LifecycleContext, service *projectarchive.Service, projects *project.Service) *ProjectArchiveFacade {
	return &ProjectArchiveFacade{lifecycle: lifecycle, service: service, projects: projects}
}

func (f *ProjectArchiveFacade) ExportProject(projectID string) (projectarchive.ExportResult, error) {
	selected, err := f.projects.Get(f.lifecycle.Context(), projectID)
	if err != nil {
		return projectarchive.ExportResult{}, err
	}
	fileName := archiveFileName(selected.Name)
	destination, err := runtime.SaveFileDialog(f.lifecycle.Context(), runtime.SaveDialogOptions{
		Title:           "导出无密钥项目归档",
		DefaultFilename: fileName,
		Filters: []runtime.FileFilter{
			{DisplayName: "SciAide 项目归档 (*.sciaide-project)", Pattern: "*.sciaide-project"},
		},
	})
	if err != nil || strings.TrimSpace(destination) == "" {
		return projectarchive.ExportResult{}, err
	}
	return f.service.Export(f.lifecycle.Context(), selected.ID, destination)
}

func (f *ProjectArchiveFacade) RestoreProject() (projectarchive.RestoreReport, error) {
	source, err := runtime.OpenFileDialog(f.lifecycle.Context(), runtime.OpenDialogOptions{
		Title: "恢复 SciAide 项目归档",
		Filters: []runtime.FileFilter{
			{DisplayName: "SciAide 项目归档 (*.sciaide-project)", Pattern: "*.sciaide-project"},
		},
	})
	if err != nil || strings.TrimSpace(source) == "" {
		return projectarchive.RestoreReport{}, err
	}
	return f.service.Restore(f.lifecycle.Context(), projectarchive.RestoreCommand{Path: source})
}

func archiveFileName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Map(func(value rune) rune {
		if value < 32 || strings.ContainsRune(`<>:"/\|?*`, value) || unicode.IsControl(value) {
			return '-'
		}
		return value
	}, name)
	name = strings.Trim(name, ". ")
	if name == "" {
		name = "SciAide-project"
	}
	if len([]rune(name)) > 120 {
		name = string([]rune(name)[:120])
	}
	return filepath.Base(name) + projectarchive.Extension
}

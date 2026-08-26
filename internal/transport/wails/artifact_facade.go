package wails

import (
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/wangh00/SciAide/internal/app/artifact"
)

type ArtifactFacade struct {
	lifecycle *LifecycleContext
	service   *artifact.Service
}

type SaveAssistantArtifactRequest struct {
	ProjectID  string `json:"projectId"`
	MessageID  string `json:"messageId"`
	Name       string `json:"name"`
	ArtifactID string `json:"artifactId,omitempty"`
}

func NewArtifactFacade(lifecycle *LifecycleContext, service *artifact.Service) *ArtifactFacade {
	return &ArtifactFacade{lifecycle: lifecycle, service: service}
}

func (f *ArtifactFacade) ListArtifacts(projectID string, includeTrashed bool) ([]artifact.Artifact, error) {
	return f.service.List(f.lifecycle.Context(), projectID, includeTrashed)
}

func (f *ArtifactFacade) GetArtifact(projectID, artifactID string) (artifact.Detail, error) {
	return f.service.Get(f.lifecycle.Context(), projectID, artifactID)
}

func (f *ArtifactFacade) SaveAssistantAnswer(request SaveAssistantArtifactRequest) (artifact.SaveResult, error) {
	return f.service.SaveAssistantAnswer(f.lifecycle.Context(), request.ProjectID, request.MessageID, request.Name, request.ArtifactID)
}

func (f *ArtifactFacade) ChooseAndRegisterWorkspaceFile(projectID, artifactID string) (artifact.SaveResult, error) {
	path, err := runtime.OpenFileDialog(f.lifecycle.Context(), runtime.OpenDialogOptions{Title: "选择要登记为科研产物的 Workspace 文件"})
	if err != nil || strings.TrimSpace(path) == "" {
		return artifact.SaveResult{}, err
	}
	return f.service.RegisterWorkspaceFile(f.lifecycle.Context(), artifact.RegisterWorkspaceCommand{ProjectID: projectID, Path: path, ArtifactID: artifactID})
}

func (f *ArtifactFacade) RegisterWorkspaceFile(request artifact.RegisterWorkspaceCommand) (artifact.SaveResult, error) {
	return f.service.RegisterWorkspaceFile(f.lifecycle.Context(), request)
}

func (f *ArtifactFacade) RenameArtifact(projectID, artifactID, name string) (artifact.Artifact, error) {
	return f.service.Rename(f.lifecycle.Context(), projectID, artifactID, name)
}

func (f *ArtifactFacade) TrashArtifact(projectID, artifactID string) (artifact.Artifact, error) {
	return f.service.Trash(f.lifecycle.Context(), projectID, artifactID)
}

func (f *ArtifactFacade) RestoreArtifact(projectID, artifactID string) (artifact.Artifact, error) {
	return f.service.Restore(f.lifecycle.Context(), projectID, artifactID)
}

func (f *ArtifactFacade) PreviewArtifactVersion(projectID, versionID string) (artifact.Preview, error) {
	return f.service.Preview(f.lifecycle.Context(), projectID, versionID)
}

func (f *ArtifactFacade) CheckArtifactIntegrity(projectID, versionID string) (artifact.IntegrityResult, error) {
	return f.service.CheckIntegrity(f.lifecycle.Context(), projectID, versionID)
}

func (f *ArtifactFacade) CreateArtifactExport(request artifact.ExportCommand) (artifact.ExportResult, error) {
	return f.service.CreateExport(f.lifecycle.Context(), request)
}

func (f *ArtifactFacade) DownloadArtifactVersion(projectID, versionID, fileName string) (string, error) {
	fileName = filepath.Base(strings.TrimSpace(fileName))
	if fileName == "." || fileName == "" {
		fileName = "artifact"
	}
	destination, err := runtime.SaveFileDialog(f.lifecycle.Context(), runtime.SaveDialogOptions{Title: "下载科研产物版本", DefaultFilename: fileName})
	if err != nil || strings.TrimSpace(destination) == "" {
		return "", err
	}
	if err := f.service.Download(f.lifecycle.Context(), projectID, versionID, destination); err != nil {
		return "", err
	}
	return destination, nil
}

func (f *ArtifactFacade) DownloadArtifactExport(projectID, exportID, fileName string) (string, error) {
	fileName = filepath.Base(strings.TrimSpace(fileName))
	if fileName == "." || fileName == "" {
		fileName = "artifact-export"
	}
	destination, err := runtime.SaveFileDialog(f.lifecycle.Context(), runtime.SaveDialogOptions{Title: "下载科研产物导出", DefaultFilename: fileName})
	if err != nil || strings.TrimSpace(destination) == "" {
		return "", err
	}
	if err := f.service.DownloadExport(f.lifecycle.Context(), projectID, exportID, destination); err != nil {
		return "", err
	}
	return destination, nil
}

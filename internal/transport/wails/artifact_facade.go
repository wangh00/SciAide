package wails

import (
	"fmt"
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

func (f *ArtifactFacade) ListTaskArtifacts(projectID, taskID string, includeTrashed bool) ([]artifact.Artifact, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, fmt.Errorf("科研任务 ID 不能为空")
	}
	return f.service.ListForTask(f.lifecycle.Context(), projectID, taskID, includeTrashed)
}

func (f *ArtifactFacade) GetArtifact(projectID, artifactID string) (artifact.Detail, error) {
	return f.service.Get(f.lifecycle.Context(), projectID, artifactID)
}

func (f *ArtifactFacade) GetTaskArtifact(projectID, taskID, artifactID string) (artifact.Detail, error) {
	return f.service.GetForTask(f.lifecycle.Context(), projectID, taskID, artifactID)
}

func (f *ArtifactFacade) requireTaskArtifact(projectID, taskID, artifactID string) (artifact.Detail, error) {
	return f.service.GetForTask(f.lifecycle.Context(), projectID, taskID, artifactID)
}

func (f *ArtifactFacade) SaveAssistantAnswer(request SaveAssistantArtifactRequest) (artifact.SaveResult, error) {
	return f.service.SaveAssistantAnswer(f.lifecycle.Context(), request.ProjectID, request.MessageID, request.Name, request.ArtifactID)
}

func (f *ArtifactFacade) ChooseAndRegisterWorkspaceFile(projectID, artifactID string) (artifact.SaveResult, error) {
	path, err := runtime.OpenFileDialog(f.lifecycle.Context(), runtime.OpenDialogOptions{Title: "选择要登记为科研产物的 Workspace 文件"})
	if err != nil || strings.TrimSpace(path) == "" {
		return artifact.SaveResult{}, err
	}
	return f.service.RegisterWorkspaceFile(f.lifecycle.Context(), artifact.RegisterWorkspaceCommand{ProjectID: projectID, Path: path, ArtifactID: artifactID, ScopeKind: artifact.ScopeProjectShared})
}

// ChooseAndRegisterTaskWorkspaceFile registers a file under the selected
// research task scope. The task workspace is used as the source root by the
// artifact service, so task outputs cannot be mistaken for project-shared
// files.
func (f *ArtifactFacade) ChooseAndRegisterTaskWorkspaceFile(projectID, taskID, artifactID string) (artifact.SaveResult, error) {
	path, err := runtime.OpenFileDialog(f.lifecycle.Context(), runtime.OpenDialogOptions{Title: "选择当前科研任务的 Workspace 文件"})
	if err != nil || strings.TrimSpace(path) == "" {
		return artifact.SaveResult{}, err
	}
	return f.service.RegisterWorkspaceFile(f.lifecycle.Context(), artifact.RegisterWorkspaceCommand{ProjectID: projectID, Path: path, ArtifactID: artifactID, ScopeKind: artifact.ScopeTask, ResearchTaskID: taskID})
}

func (f *ArtifactFacade) RegisterWorkspaceFile(request artifact.RegisterWorkspaceCommand) (artifact.SaveResult, error) {
	return f.service.RegisterWorkspaceFile(f.lifecycle.Context(), request)
}

func (f *ArtifactFacade) RenameArtifact(projectID, artifactID, name string) (artifact.Artifact, error) {
	return f.service.Rename(f.lifecycle.Context(), projectID, artifactID, name)
}

func (f *ArtifactFacade) RenameTaskArtifact(projectID, taskID, artifactID, name string) (artifact.Artifact, error) {
	if err := f.requireTaskArtifactOwned(projectID, taskID, artifactID); err != nil {
		return artifact.Artifact{}, err
	}
	return f.service.Rename(f.lifecycle.Context(), projectID, artifactID, name)
}

func (f *ArtifactFacade) TrashArtifact(projectID, artifactID string) (artifact.Artifact, error) {
	return f.service.Trash(f.lifecycle.Context(), projectID, artifactID)
}

func (f *ArtifactFacade) TrashTaskArtifact(projectID, taskID, artifactID string) (artifact.Artifact, error) {
	if err := f.requireTaskArtifactOwned(projectID, taskID, artifactID); err != nil {
		return artifact.Artifact{}, err
	}
	return f.service.Trash(f.lifecycle.Context(), projectID, artifactID)
}

func (f *ArtifactFacade) RestoreArtifact(projectID, artifactID string) (artifact.Artifact, error) {
	return f.service.Restore(f.lifecycle.Context(), projectID, artifactID)
}

func (f *ArtifactFacade) RestoreTaskArtifact(projectID, taskID, artifactID string) (artifact.Artifact, error) {
	if err := f.requireTaskArtifactOwned(projectID, taskID, artifactID); err != nil {
		return artifact.Artifact{}, err
	}
	return f.service.Restore(f.lifecycle.Context(), projectID, artifactID)
}

func (f *ArtifactFacade) PreviewArtifactVersion(projectID, versionID string) (artifact.Preview, error) {
	return f.service.Preview(f.lifecycle.Context(), projectID, versionID)
}

func (f *ArtifactFacade) PreviewTaskArtifactVersion(projectID, taskID, versionID string) (artifact.Preview, error) {
	if err := f.requireTaskArtifactVersion(projectID, taskID, versionID); err != nil {
		return artifact.Preview{}, err
	}
	return f.service.Preview(f.lifecycle.Context(), projectID, versionID)
}

func (f *ArtifactFacade) CheckArtifactIntegrity(projectID, versionID string) (artifact.IntegrityResult, error) {
	return f.service.CheckIntegrity(f.lifecycle.Context(), projectID, versionID)
}

func (f *ArtifactFacade) CheckTaskArtifactIntegrity(projectID, taskID, versionID string) (artifact.IntegrityResult, error) {
	if err := f.requireTaskArtifactVersion(projectID, taskID, versionID); err != nil {
		return artifact.IntegrityResult{}, err
	}
	return f.service.CheckIntegrity(f.lifecycle.Context(), projectID, versionID)
}

func (f *ArtifactFacade) CreateArtifactExport(request artifact.ExportCommand) (artifact.ExportResult, error) {
	return f.service.CreateExport(f.lifecycle.Context(), request)
}

func (f *ArtifactFacade) CreateTaskArtifactExport(taskID string, request artifact.ExportCommand) (artifact.ExportResult, error) {
	version, err := f.service.GetVersion(f.lifecycle.Context(), strings.TrimSpace(request.ProjectID), strings.TrimSpace(request.VersionID))
	if err != nil {
		return artifact.ExportResult{}, err
	}
	if err := f.requireTaskArtifactOwned(request.ProjectID, taskID, version.ArtifactID); err != nil {
		return artifact.ExportResult{}, err
	}
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

func (f *ArtifactFacade) DownloadTaskArtifactVersion(projectID, taskID, versionID, fileName string) (string, error) {
	if err := f.requireTaskArtifactVersion(projectID, taskID, versionID); err != nil {
		return "", err
	}
	return f.downloadArtifactVersion(projectID, versionID, fileName)
}

func (f *ArtifactFacade) downloadArtifactVersion(projectID, versionID, fileName string) (string, error) {
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

func (f *ArtifactFacade) DownloadTaskArtifactExport(projectID, taskID, exportID, fileName string) (string, error) {
	value, err := f.service.GetExport(f.lifecycle.Context(), projectID, exportID)
	if err != nil {
		return "", err
	}
	if err := f.requireTaskArtifactVersion(projectID, taskID, value.ArtifactVersionID); err != nil {
		return "", err
	}
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

func (f *ArtifactFacade) requireTaskArtifactVersion(projectID, taskID, versionID string) error {
	version, err := f.service.GetVersion(f.lifecycle.Context(), strings.TrimSpace(projectID), strings.TrimSpace(versionID))
	if err != nil {
		return err
	}
	if version.ArtifactID == "" {
		return fmt.Errorf("科研产物版本无效")
	}
	_, err = f.requireTaskArtifact(projectID, taskID, version.ArtifactID)
	return err
}

func (f *ArtifactFacade) requireTaskArtifactOwned(projectID, taskID, artifactID string) error {
	detail, err := f.requireTaskArtifact(projectID, taskID, artifactID)
	if err != nil {
		return err
	}
	if detail.Artifact.ScopeKind != artifact.ScopeTask || detail.Artifact.ResearchTaskID != strings.TrimSpace(taskID) {
		return fmt.Errorf("项目共享产物不可在任务内修改")
	}
	return nil
}

package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/id"
)

type Service struct {
	repository  Repository
	managedRoot string
	trashRoot   string
	now         func() time.Time
}

func NewService(repository Repository, managedRoot, trashRoot string) *Service {
	return &Service{repository: repository, managedRoot: managedRoot, trashRoot: trashRoot, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Create(ctx context.Context, name, description string) (Project, error) {
	return s.CreateWithWorkspace(ctx, CreateCommand{Name: name, Description: description})
}

func (s *Service) CreateWithWorkspace(ctx context.Context, cmd CreateCommand) (Project, error) {
	name, description := strings.TrimSpace(cmd.Name), strings.TrimSpace(cmd.Description)
	name = strings.TrimSpace(name)
	if name == "" {
		return Project{}, fmt.Errorf("project name is required")
	}
	projectID, err := id.New()
	if err != nil {
		return Project{}, err
	}
	now := s.now()
	value := Project{
		ID:          projectID,
		Name:        name,
		Description: description,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	path := strings.TrimSpace(cmd.WorkspacePath)
	if path == "" {
		if s.managedRoot == "" {
			return Project{}, fmt.Errorf("managed workspace root is not configured")
		}
		path = filepath.Join(s.managedRoot, projectID)
		value.WorkspaceKind = WorkspaceManaged
	} else {
		path, err = filepath.Abs(path)
		if err != nil {
			return Project{}, fmt.Errorf("resolve workspace path: %w", err)
		}
		value.WorkspaceKind = WorkspaceExternal
	}
	value.WorkspacePath = filepath.Clean(path)
	if err := os.MkdirAll(value.WorkspacePath, 0o700); err != nil {
		return Project{}, fmt.Errorf("create workspace directory: %w", err)
	}
	if err := writeMarker(value.WorkspacePath, projectID); err != nil {
		if value.WorkspaceKind == WorkspaceManaged {
			_ = os.RemoveAll(value.WorkspacePath)
		}
		return Project{}, err
	}
	if err := s.repository.Create(ctx, value); err != nil {
		if value.WorkspaceKind == WorkspaceManaged {
			_ = os.RemoveAll(value.WorkspacePath)
		}
		return Project{}, fmt.Errorf("create project: %w", err)
	}
	return value, nil
}

func (s *Service) Remove(ctx context.Context, projectID string) (RemoveResult, error) {
	value, err := s.repository.Get(ctx, strings.TrimSpace(projectID))
	if err != nil {
		return RemoveResult{}, err
	}
	result := RemoveResult{ProjectID: value.ID, WorkspacePath: value.WorkspacePath, WorkspacePreserved: value.WorkspaceKind != WorkspaceManaged}
	if value.WorkspaceKind == WorkspaceManaged && value.WorkspacePath != "" {
		if err := s.verifyManagedPath(value.WorkspacePath); err != nil {
			return RemoveResult{}, err
		}
		if err := verifyMarker(value.WorkspacePath, value.ID); err != nil {
			return RemoveResult{}, err
		}
		trash := filepath.Join(s.trashRoot, s.now().Format("20060102T150405.000000000Z")+"-"+value.ID)
		if err := os.MkdirAll(filepath.Dir(trash), 0o700); err != nil {
			return result, err
		}
		if _, err := os.Stat(value.WorkspacePath); err == nil {
			if err := os.Rename(value.WorkspacePath, trash); err != nil {
				return result, fmt.Errorf("move managed workspace to trash: %w", err)
			}
			result.TrashPath = trash
		} else if !os.IsNotExist(err) {
			return result, err
		}
	}
	if err := s.repository.Delete(ctx, value.ID); err != nil {
		if result.TrashPath != "" {
			_ = os.Rename(result.TrashPath, value.WorkspacePath)
		}
		return result, fmt.Errorf("remove project: %w", err)
	}
	return result, nil
}

// ReconcileWorkspacePaths upgrades projects created before workspace_path was
// introduced. It is safe to run at every startup.
func (s *Service) ReconcileWorkspacePaths(ctx context.Context) error {
	if s.managedRoot == "" {
		return fmt.Errorf("managed workspace root is not configured")
	}
	values, err := s.repository.List(ctx)
	if err != nil {
		return fmt.Errorf("list projects for workspace reconciliation: %w", err)
	}
	for _, value := range values {
		if strings.TrimSpace(value.WorkspacePath) != "" {
			continue
		}
		path := filepath.Join(s.managedRoot, value.ID)
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create legacy project workspace: %w", err)
		}
		if err := writeMarker(path, value.ID); err != nil {
			return err
		}
		if err := s.repository.UpdateWorkspace(ctx, value.ID, filepath.Clean(path), WorkspaceManaged, s.now()); err != nil {
			return fmt.Errorf("save reconciled workspace path: %w", err)
		}
	}
	return nil
}

func writeMarker(workspacePath, projectID string) error {
	marker := filepath.Join(workspacePath, ".sciaide-workspace.json")
	if _, err := os.Stat(marker); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect workspace marker: %w", err)
	}
	contents := []byte(fmt.Sprintf("{\"version\":1,\"projectId\":%q}\n", projectID))
	if err := os.WriteFile(marker, contents, 0o600); err != nil {
		return fmt.Errorf("write workspace marker: %w", err)
	}
	return nil
}

func verifyMarker(workspacePath, projectID string) error {
	marker := filepath.Join(workspacePath, ".sciaide-workspace.json")
	contents, err := os.ReadFile(marker)
	if os.IsNotExist(err) {
		// A missing workspace is harmless: the database record may outlive a
		// directory the user removed manually.
		if _, statErr := os.Stat(workspacePath); os.IsNotExist(statErr) {
			return nil
		}
		return fmt.Errorf("managed workspace marker is missing; refusing to move directory")
	}
	if err != nil {
		return fmt.Errorf("read managed workspace marker: %w", err)
	}
	want := fmt.Sprintf("\"projectId\":%q", projectID)
	if !strings.Contains(string(contents), want) {
		return fmt.Errorf("managed workspace marker does not match project; refusing to move directory")
	}
	return nil
}

func (s *Service) verifyManagedPath(path string) error {
	if s.managedRoot == "" || s.trashRoot == "" {
		return fmt.Errorf("managed workspace roots are not configured")
	}
	root, err := filepath.Abs(s.managedRoot)
	if err != nil {
		return err
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("managed workspace path is outside SciAide root")
	}
	return nil
}

func (s *Service) Get(ctx context.Context, projectID string) (Project, error) {
	return s.repository.Get(ctx, projectID)
}

func (s *Service) List(ctx context.Context) ([]Project, error) {
	return s.repository.List(ctx)
}

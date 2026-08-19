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
	privateCreated, err := ensurePrivateLayout(value.WorkspacePath, projectID)
	if err != nil {
		if value.WorkspaceKind == WorkspaceManaged {
			_ = os.RemoveAll(value.WorkspacePath)
		}
		return Project{}, err
	}
	if err := s.repository.Create(ctx, value); err != nil {
		if value.WorkspaceKind == WorkspaceManaged {
			_ = os.RemoveAll(value.WorkspacePath)
		} else if privateCreated {
			_ = os.RemoveAll(PrivateDataPath(value))
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
		if err := verifyProjectMarker(value.WorkspacePath, value.ID); err != nil {
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
		if strings.TrimSpace(value.WorkspacePath) == "" {
			path := filepath.Join(s.managedRoot, value.ID)
			if err := os.MkdirAll(path, 0o700); err != nil {
				return fmt.Errorf("create legacy project workspace: %w", err)
			}
			value.WorkspacePath = filepath.Clean(path)
			value.WorkspaceKind = WorkspaceManaged
			if err := s.repository.UpdateWorkspace(ctx, value.ID, value.WorkspacePath, value.WorkspaceKind, s.now()); err != nil {
				return fmt.Errorf("save reconciled workspace path: %w", err)
			}
		} else if info, err := os.Stat(value.WorkspacePath); os.IsNotExist(err) {
			// External drives and network folders may be temporarily unavailable.
			// Keep the project record and reconcile its private layout when the
			// workspace is available again.
			continue
		} else if err != nil {
			return fmt.Errorf("inspect project workspace %q: %w", value.ID, err)
		} else if !info.IsDir() {
			return fmt.Errorf("project workspace %q is not a directory", value.ID)
		}
		if _, err := ensurePrivateLayout(value.WorkspacePath, value.ID); err != nil {
			if value.WorkspaceKind == WorkspaceExternal {
				// A read-only or user-managed external workspace must not prevent
				// SciAide from starting. Attachment import will report the same
				// layout error when the user tries to write project-local data.
				continue
			}
			return fmt.Errorf("reconcile project data layout for %q: %w", value.ID, err)
		}
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

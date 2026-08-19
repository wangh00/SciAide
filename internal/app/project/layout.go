package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	PrivateDirectoryName = ".sciaide"
	projectMarkerName    = "project.json"
	legacyMarkerName     = ".sciaide-workspace.json"
)

var privateSubdirectories = []string{"attachments", "cache", "artifacts", "tmp"}

type projectMarker struct {
	Version   int    `json:"version"`
	ProjectID string `json:"projectId"`
}

// PrivateDataPath returns the only project-local root SciAide may use for
// managed attachments, derived caches, temporary files, and generated output.
func PrivateDataPath(value Project) string {
	return filepath.Join(value.WorkspacePath, PrivateDirectoryName)
}

// VerifyPrivateDataLayout prevents project-scoped services from accepting a
// replaced or unrelated .sciaide directory after the project was created.
func VerifyPrivateDataLayout(value Project) error {
	if strings.TrimSpace(value.ID) == "" || strings.TrimSpace(value.WorkspacePath) == "" {
		return fmt.Errorf("project workspace identity is incomplete")
	}
	info, err := os.Stat(value.WorkspacePath)
	if err != nil {
		return fmt.Errorf("project workspace is unavailable: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("project workspace is not a directory")
	}
	return verifyProjectMarker(value.WorkspacePath, value.ID)
}

func ensurePrivateLayout(workspacePath, projectID string) (bool, error) {
	root := filepath.Join(workspacePath, PrivateDirectoryName)
	_, statErr := os.Stat(root)
	created := os.IsNotExist(statErr)
	if statErr != nil && !os.IsNotExist(statErr) {
		return false, fmt.Errorf("inspect project data directory: %w", statErr)
	}
	if created {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return false, fmt.Errorf("create project data directory: %w", err)
		}
	} else {
		markerPath := filepath.Join(root, projectMarkerName)
		if _, err := os.Stat(markerPath); os.IsNotExist(err) {
			if err := verifyLegacyMarker(workspacePath, projectID); err != nil {
				return false, fmt.Errorf("existing %s directory is not owned by this project", PrivateDirectoryName)
			}
		} else if err != nil {
			return false, fmt.Errorf("inspect project data marker: %w", err)
		} else if err := writeProjectMarker(root, projectID); err != nil {
			return false, err
		}
	}
	if err := verifyPrivateRootConfinement(workspacePath, root); err != nil {
		if created {
			_ = os.RemoveAll(root)
		}
		return false, err
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return false, fmt.Errorf("open project data directory: %w", err)
	}
	defer rootHandle.Close()
	for _, name := range privateSubdirectories {
		if err := rootHandle.MkdirAll(name, 0o700); err != nil {
			if created {
				_ = os.RemoveAll(root)
			}
			return false, fmt.Errorf("create project %s directory: %w", name, err)
		}
	}
	if err := writeProjectMarker(root, projectID); err != nil {
		if created {
			_ = os.RemoveAll(root)
		}
		return false, err
	}
	// Keep runtime data out of a Git worktree without modifying the user's
	// repository-level ignore rules.
	if info, err := rootHandle.Lstat(".gitignore"); os.IsNotExist(err) {
		if err := rootHandle.WriteFile(".gitignore", []byte("*\n"), 0o600); err != nil {
			return created, fmt.Errorf("write project data ignore rule: %w", err)
		}
	} else if err != nil {
		return created, fmt.Errorf("inspect project data ignore rule: %w", err)
	} else if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return created, fmt.Errorf("project data ignore rule is not a regular file")
	}
	if err := removeMatchingLegacyMarker(workspacePath, projectID); err != nil {
		return created, err
	}
	return created, nil
}

func writeProjectMarker(root, projectID string) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return fmt.Errorf("project marker id is required")
	}
	path := filepath.Join(root, projectMarkerName)
	if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return fmt.Errorf("project data marker is not a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect project data marker: %w", err)
	}
	if contents, err := os.ReadFile(path); err == nil {
		var existing projectMarker
		if json.Unmarshal(contents, &existing) != nil || existing.Version != 1 || existing.ProjectID != projectID {
			return fmt.Errorf("project data marker does not match this project")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect project data marker: %w", err)
	}
	contents, err := json.Marshal(projectMarker{Version: 1, ProjectID: projectID})
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		return fmt.Errorf("write project data marker: %w", err)
	}
	return nil
}

func verifyProjectMarker(workspacePath, projectID string) error {
	root := filepath.Join(workspacePath, PrivateDirectoryName)
	path := filepath.Join(root, projectMarkerName)
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		if _, statErr := os.Stat(workspacePath); os.IsNotExist(statErr) {
			return nil
		}
		return verifyLegacyMarker(workspacePath, projectID)
	} else if err != nil {
		return fmt.Errorf("inspect project data directory: %w", err)
	}
	if err := verifyPrivateRootConfinement(workspacePath, root); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return fmt.Errorf("project data marker is not a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect project data marker: %w", err)
	}
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if _, statErr := os.Stat(workspacePath); os.IsNotExist(statErr) {
			return nil
		}
		return verifyLegacyMarker(workspacePath, projectID)
	}
	if err != nil {
		return fmt.Errorf("read project data marker: %w", err)
	}
	var marker projectMarker
	if json.Unmarshal(contents, &marker) != nil || marker.Version != 1 || marker.ProjectID != projectID {
		return fmt.Errorf("project data marker does not match project; refusing to move directory")
	}
	return nil
}

func verifyPrivateRootConfinement(workspacePath, root string) error {
	workspaceResolved, err := filepath.EvalSymlinks(workspacePath)
	if err != nil {
		return fmt.Errorf("resolve project workspace: %w", err)
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect project data directory: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("project data directory must be a real directory")
	}
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve project data directory: %w", err)
	}
	relative, err := filepath.Rel(workspaceResolved, rootResolved)
	if err != nil || !strings.EqualFold(relative, PrivateDirectoryName) {
		return fmt.Errorf("project data directory escapes the workspace")
	}
	return nil
}

func verifyLegacyMarker(workspacePath, projectID string) error {
	contents, err := os.ReadFile(filepath.Join(workspacePath, legacyMarkerName))
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("managed workspace marker is missing; refusing to move directory")
		}
		return fmt.Errorf("read managed workspace marker: %w", err)
	}
	var marker projectMarker
	if json.Unmarshal(contents, &marker) != nil || marker.ProjectID != projectID {
		return fmt.Errorf("managed workspace marker does not match project; refusing to move directory")
	}
	return nil
}

func removeMatchingLegacyMarker(workspacePath, projectID string) error {
	path := filepath.Join(workspacePath, legacyMarkerName)
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read legacy workspace marker: %w", err)
	}
	var marker projectMarker
	if json.Unmarshal(contents, &marker) != nil || marker.ProjectID != projectID {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove legacy workspace marker: %w", err)
	}
	return nil
}

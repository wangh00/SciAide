// Package pathguard resolves project-relative paths without allowing a file
// operation to escape its trusted Workspace root.
package pathguard

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const maxRelativePathBytes = 4096

type Guard struct {
	root    string
	rootDir *os.Root
}

func Open(root string) (*Guard, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("workspace root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("inspect workspace root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace root is not a directory")
	}
	rootDir, err := os.OpenRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("open workspace root: %w", err)
	}
	if err := rejectReparsePath(abs, "."); err != nil {
		rootDir.Close()
		return nil, err
	}
	return &Guard{root: abs, rootDir: rootDir}, nil
}

func (g *Guard) Close() error {
	if g == nil || g.rootDir == nil {
		return nil
	}
	return g.rootDir.Close()
}

// Relative validates a user/model supplied relative path. filepath.Rel on the
// absolute candidate prevents sibling-prefix tricks such as workspace-other.
func (g *Guard) Relative(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "." {
		return ".", nil
	}
	if len(value) > maxRelativePathBytes || strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("workspace path is invalid")
	}
	if filepath.IsAbs(value) || filepath.VolumeName(value) != "" {
		return "", fmt.Errorf("absolute workspace paths are not allowed")
	}
	clean := filepath.Clean(value)
	candidate := filepath.Join(g.root, clean)
	relative, err := filepath.Rel(g.root, candidate)
	if err != nil || escapes(relative) {
		return "", fmt.Errorf("workspace path escapes the project root")
	}
	return relative, nil
}

func (g *Guard) OpenFile(relative string) (*os.File, string, error) {
	clean, err := g.Relative(relative)
	if err != nil {
		return nil, "", err
	}
	if err := rejectReparsePath(g.root, clean); err != nil {
		return nil, "", err
	}
	file, err := g.rootDir.Open(clean)
	if err != nil {
		return nil, "", fmt.Errorf("open workspace path: %w", err)
	}
	return file, clean, nil
}

// CreateFile creates a new regular file through the root handle after every
// existing component has been checked for symlinks or reparse points.
func (g *Guard) CreateFile(relative string, perm os.FileMode) (*os.File, string, error) {
	clean, err := g.Relative(relative)
	if err != nil || clean == "." {
		return nil, "", fmt.Errorf("workspace file path is invalid")
	}
	if err := rejectReparsePath(g.root, clean); err != nil {
		return nil, "", err
	}
	file, err := g.rootDir.OpenFile(clean, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return nil, "", fmt.Errorf("create workspace file: %w", err)
	}
	return file, clean, nil
}

func (g *Guard) Remove(relative string) error {
	clean, err := g.Relative(relative)
	if err != nil || clean == "." {
		return fmt.Errorf("workspace path is invalid")
	}
	if err := rejectReparsePath(g.root, clean); err != nil {
		return err
	}
	return g.rootDir.Remove(clean)
}

// RemoveAll recursively removes one path through the already-open Workspace
// root. Callers must still constrain which subtree they own before calling.
func (g *Guard) RemoveAll(relative string) error {
	clean, err := g.Relative(relative)
	if err != nil || clean == "." {
		return fmt.Errorf("workspace path is invalid")
	}
	if err := rejectReparsePath(g.root, clean); err != nil {
		return err
	}
	return g.rootDir.RemoveAll(clean)
}

// WalkDir traverses a validated Workspace subtree through the already-open
// root handle.
func (g *Guard) WalkDir(relative string, fn fs.WalkDirFunc) error {
	clean, err := g.Relative(relative)
	if err != nil || fn == nil {
		return fmt.Errorf("workspace path is invalid")
	}
	if err := rejectReparsePath(g.root, clean); err != nil {
		return err
	}
	return fs.WalkDir(g.rootDir.FS(), filepath.ToSlash(clean), fn)
}

// MkdirAll creates a real directory tree below the Workspace root. Each
// existing and newly created component is checked so a symlink, junction, or
// other reparse point cannot become an implicit output redirect.
func (g *Guard) MkdirAll(relative string, perm os.FileMode) (string, error) {
	clean, err := g.Relative(relative)
	if err != nil {
		return "", err
	}
	if clean == "." {
		return clean, nil
	}
	current := ""
	for _, component := range splitRelativePath(clean) {
		current = filepath.Join(current, component)
		info, statErr := g.rootDir.Lstat(current)
		if os.IsNotExist(statErr) {
			if err := g.rootDir.Mkdir(current, perm); err != nil && !os.IsExist(err) {
				return "", fmt.Errorf("create workspace directory: %w", err)
			}
			info, statErr = g.rootDir.Lstat(current)
		}
		if statErr != nil {
			return "", fmt.Errorf("inspect workspace directory: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("workspace directory path contains a non-directory component")
		}
		if err := rejectReparsePath(g.root, current); err != nil {
			return "", err
		}
	}
	return clean, nil
}

func (g *Guard) Absolute(relative string) (string, error) {
	clean, err := g.Relative(relative)
	if err != nil {
		return "", err
	}
	return filepath.Join(g.root, clean), nil
}

func escapes(relative string) bool {
	return relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func splitRelativePath(relative string) []string {
	parts := strings.FieldsFunc(filepath.Clean(relative), func(value rune) bool {
		return value == '/' || value == '\\'
	})
	return parts
}

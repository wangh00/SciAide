// Package pathguard resolves project-relative paths without allowing a file
// operation to escape its trusted Workspace root.
package pathguard

import (
	"fmt"
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

func (g *Guard) Root() string { return g.root }

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

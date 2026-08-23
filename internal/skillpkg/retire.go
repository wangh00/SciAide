package skillpkg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wangh00/SciAide/internal/app/skill"
)

// ArchiveRetiredPackage moves one formerly built-in package out of the live
// Skill catalog. The migration supplies the exact trusted package path, so a
// later user-installed Skill with the same ID is not affected.
func ArchiveRetiredPackage(installedRoot, backupRoot, packageRelativePath string) (bool, error) {
	if err := os.MkdirAll(backupRoot, 0o700); err != nil {
		return false, fmt.Errorf("create retired Skill backup root: %w", err)
	}
	if err := requireSafeDirectory(backupRoot); err != nil {
		return false, fmt.Errorf("retired Skill backup root is unsafe")
	}
	parts := strings.Split(filepath.ToSlash(strings.TrimSpace(packageRelativePath)), "/")
	if len(parts) != 2 || !skill.ValidID(parts[0]) || !skill.ValidVersion(parts[1]) {
		return false, fmt.Errorf("invalid retired Skill package path")
	}
	source, err := safeManagedJoin(installedRoot, filepath.ToSlash(filepath.Join(parts[0], parts[1])))
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(source)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect retired Skill package: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("retired Skill package path is unsafe")
	}
	relativeParent := filepath.ToSlash(filepath.Join("retired", parts[0], parts[1]))
	if err := ensureManagedDirectory(backupRoot, relativeParent); err != nil {
		return false, fmt.Errorf("prepare retired Skill archive: %w", err)
	}
	parent, err := safeManagedJoin(backupRoot, relativeParent)
	if err != nil {
		return false, err
	}
	destination, err := os.MkdirTemp(parent, "package-*")
	if err != nil {
		return false, fmt.Errorf("allocate retired Skill archive: %w", err)
	}
	if err := os.Remove(destination); err != nil {
		return false, err
	}
	if err := os.Rename(source, destination); err != nil {
		return false, fmt.Errorf("archive retired Skill package: %w", err)
	}
	_ = os.Remove(filepath.Dir(source))
	return true, nil
}

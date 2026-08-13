//go:build !windows

package pathguard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func rejectReparsePath(root, relative string) error {
	current := filepath.Clean(root)
	if relative == "." {
		return nil
	}
	for _, component := range splitPath(relative) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect workspace path: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace path contains a symbolic link")
		}
	}
	return nil
}

func splitPath(relative string) []string {
	relative = filepath.Clean(relative)
	if relative == "." || relative == string(filepath.Separator) {
		return nil
	}
	return strings.FieldsFunc(relative, func(value rune) bool { return value == rune(filepath.Separator) })
}

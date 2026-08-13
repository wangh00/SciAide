//go:build windows

package pathguard

import (
	"fmt"
	"path/filepath"
	"syscall"
)

// rejectReparsePath fails closed for every existing reparse-point component.
// This covers symlinks, junctions and mount-point reparse records before the
// file is opened through os.Root.
func rejectReparsePath(root, relative string) error {
	root = filepath.Clean(root)
	volume := filepath.VolumeName(root)
	current := volume + string(filepath.Separator)
	for _, component := range splitPath(root[len(volume):]) {
		current = filepath.Join(current, component)
		if reparse, err := isReparsePoint(current); err != nil {
			return err
		} else if reparse {
			return fmt.Errorf("workspace root contains a reparse point")
		}
	}
	if relative == "." {
		return nil
	}
	current = root
	for _, component := range splitPath(relative) {
		current = filepath.Join(current, component)
		reparse, err := isReparsePoint(current)
		if err != nil {
			return err
		}
		if reparse {
			return fmt.Errorf("workspace path contains a reparse point")
		}
	}
	return nil
}

func isReparsePoint(path string) (bool, error) {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return false, fmt.Errorf("encode workspace path: %w", err)
	}
	attributes, err := syscall.GetFileAttributes(pointer)
	if err != nil {
		return false, fmt.Errorf("inspect workspace path attributes: %w", err)
	}
	return attributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}

func splitPath(relative string) []string {
	volume := filepath.VolumeName(relative)
	relative = relative[len(volume):]
	var values []string
	for relative != "." && relative != "" {
		directory, file := filepath.Split(relative)
		if file != "" {
			values = append([]string{file}, values...)
		}
		relative = filepath.Clean(directory)
		if relative == string(filepath.Separator) {
			break
		}
	}
	return values
}

//go:build windows

package debugapi

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

// Get the path followed by the OS, including directory junctions. Go's
// EvalSymlinks can leave junctions unresolved with legacy winsymlink settings.
func resolveExistingPath(path string) (string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(p, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 32768)
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
	if err != nil {
		return "", fmt.Errorf("resolve debug workspace path: %w", err)
	}
	if n >= uint32(len(buf)) {
		return "", fmt.Errorf("resolved debug workspace path is too long")
	}
	final := windows.UTF16ToString(buf[:n])
	if strings.HasPrefix(final, `\\?\UNC\`) {
		return `\\` + strings.TrimPrefix(final, `\\?\UNC\`), nil
	}
	return strings.TrimPrefix(final, `\\?\`), nil
}

func hasMultipleLinks(path string) (bool, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	h, err := windows.CreateFile(p, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(h, &info); err != nil {
		return false, err
	}
	return info.NumberOfLinks > 1, nil
}

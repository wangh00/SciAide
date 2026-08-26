//go:build windows

package filepublish

import (
	"os"

	"golang.org/x/sys/windows"
)

// NoReplaceOpen atomically publishes a file and returns a handle that remains
// bound to the published file identity. FILE_SHARE_DELETE lets MoveFileEx move
// the source while the handle is held for ownership-aware rollback.
func NoReplaceOpen(source, destination string) (*os.File, error) {
	sourceUTF16, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(sourceUTF16, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), source)
	if err := NoReplace(source, destination); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

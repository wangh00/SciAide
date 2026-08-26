//go:build windows

package localexec

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

func ExecutableVersion(path string) (string, error) {
	var ignored windows.Handle
	size, err := windows.GetFileVersionInfoSize(path, &ignored)
	if err != nil {
		return "", err
	}
	if size == 0 {
		return "", fmt.Errorf("executable has no version resource")
	}
	buffer := make([]byte, size)
	if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buffer[0])); err != nil {
		return "", err
	}
	var fixed *windows.VS_FIXEDFILEINFO
	var fixedSize uint32
	if err := windows.VerQueryValue(unsafe.Pointer(&buffer[0]), `\`, unsafe.Pointer(&fixed), &fixedSize); err != nil {
		return "", err
	}
	if fixed == nil || fixedSize < uint32(unsafe.Sizeof(*fixed)) {
		return "", fmt.Errorf("executable version resource is incomplete")
	}
	return fmt.Sprintf("%d.%d.%d.%d", fixed.FileVersionMS>>16, fixed.FileVersionMS&0xffff, fixed.FileVersionLS>>16, fixed.FileVersionLS&0xffff), nil
}

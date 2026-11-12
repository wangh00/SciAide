//go:build !windows

package debugapi

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func resolveExistingPath(path string) (string, error) { return filepath.EvalSymlinks(path) }

func hasMultipleLinks(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("cannot verify debug file link identity")
	}
	return stat.Nlink > 1, nil
}

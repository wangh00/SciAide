package pythonruntime

import (
	"path/filepath"
	"runtime"
	"strings"
)

// CommandPath changes only the OS spelling used to launch Python or to pass an
// absolute directory to it. Keep stored/user-facing paths logical. In Windows,
// descendants such as pip's package files can exceed MAX_PATH even when the
// interpreter path itself is shorter; never wait until the exe exceeds 260.
func CommandPath(path string) string {
	if runtime.GOOS != "windows" || !filepath.IsAbs(path) {
		return path
	}
	path = filepath.Clean(path)
	if strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\\.\`) {
		return path
	}
	if strings.HasPrefix(path, `\\`) {
		return `\\?\UNC\` + path[2:]
	}
	return `\\?\` + path
}

func logicalPythonPath(path string) string {
	if runtime.GOOS == "windows" {
		if len(path) >= 8 && strings.EqualFold(path[:8], `\\?\UNC\`) {
			path = `\\` + path[8:]
		} else if len(path) >= 7 && strings.HasPrefix(path, `\\?\`) && path[5] == ':' {
			path = path[4:]
		}
	}
	return filepath.Clean(path)
}

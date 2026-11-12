package pythonruntime

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestPythonCommandPathPreservesLogicalIdentities(t *testing.T) {
	if runtime.GOOS != "windows" {
		if CommandPath("/tmp/python") != "/tmp/python" {
			t.Fatal("POSIX path changed")
		}
		return
	}
	for _, tc := range []struct{ logical, native string }{{`C:\Python\python.exe`, `\\?\C:\Python\python.exe`}, {`\\server\share\py\python.exe`, `\\?\UNC\server\share\py\python.exe`}, {`C:\中文 path\python.exe`, `\\?\C:\中文 path\python.exe`}} {
		if CommandPath(tc.logical) != tc.native || CommandPath(tc.native) != tc.native || logicalPythonPath(tc.native) != filepath.Clean(tc.logical) {
			t.Fatal("path spelling or logical identity changed", tc)
		}
	}
	if CommandPath("python") != "python" {
		t.Fatal("relative name became an absolute execution capability")
	}
}

func TestPythonPackageCachePinsRelativeRoot(t *testing.T) {
	expected, err := filepath.Abs("relative-python-package-cache")
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewWithPackageCache(nil, "relative-python-package-cache")
	if adapter.cachePathErr != nil || adapter.packageCache != expected {
		t.Fatal("relative application cache was not pinned at construction")
	}
	python := filepath.Join(t.TempDir(), "venv", "Scripts", "python.exe")
	legacy := filepath.Join(filepath.Dir(filepath.Dir(python)), ".pip-cache")
	if cache, err := New(nil).packageCachePath(python); err != nil || cache != legacy {
		t.Fatal("legacy constructor changed its explicit project cache", cache, err)
	}
}

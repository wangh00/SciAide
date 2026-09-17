package builtin

import (
	"errors"
	"strings"
	"testing"
)

func TestPythonKernelUserFacingErrorOnlyProjectsKnownResourceLimits(t *testing.T) {
	message, safe := pythonKernelUserFacingError(errors.New("Python Kernel exited under its 1024 MiB process-tree memory budget: OpenBLAS error: Memory allocation still failed"))
	if !safe || !strings.Contains(message, "数值库") || strings.Contains(message, "OpenBLAS") {
		t.Fatalf("memory projection = %q, %t", message, safe)
	}
	message, safe = pythonKernelUserFacingError(errors.New(`private input failed at D:\Users\researcher\secret.csv`))
	if safe || message != "" {
		t.Fatalf("private error was projected = %q, %t", message, safe)
	}
}

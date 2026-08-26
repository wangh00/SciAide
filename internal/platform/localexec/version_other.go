//go:build !windows

package localexec

import "fmt"

func ExecutableVersion(string) (string, error) {
	return "", fmt.Errorf("executable version resources are unavailable on this platform")
}

//go:build darwin

package folderopen

import (
	"fmt"
	"os/exec"
)

func open(directory string) error {
	if err := exec.Command("open", directory).Start(); err != nil {
		return fmt.Errorf("start Finder: %w", err)
	}
	return nil
}

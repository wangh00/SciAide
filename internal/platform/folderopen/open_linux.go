//go:build linux

package folderopen

import (
	"fmt"
	"os/exec"
)

func open(directory string) error {
	if err := exec.Command("xdg-open", directory).Start(); err != nil {
		return fmt.Errorf("start file manager: %w", err)
	}
	return nil
}

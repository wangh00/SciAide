//go:build windows

package folderopen

import (
	"fmt"
	"os/exec"
)

func open(directory string) error {
	if err := exec.Command("explorer.exe", directory).Start(); err != nil {
		return fmt.Errorf("start Windows Explorer: %w", err)
	}
	return nil
}

package folderopen

import (
	"fmt"
	"os"
	"path/filepath"
)

func Open(directory string) error {
	directory = filepath.Clean(directory)
	info, err := os.Stat(directory)
	if err != nil {
		return fmt.Errorf("open folder: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("open folder: target is not a directory")
	}
	return open(directory)
}

//go:build !windows && !linux && !darwin

package filepublish

import (
	"fmt"
	"os"
)

func NoReplace(source, destination string) error {
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("destination already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(source, destination)
}

func Replace(source, destination string) error {
	return os.Rename(source, destination)
}

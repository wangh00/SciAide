//go:build !windows && !darwin && !linux

package folderopen

import "fmt"

func open(string) error { return fmt.Errorf("opening folders is not supported on this platform") }

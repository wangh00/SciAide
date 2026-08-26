//go:build !windows

package filepublish

import "os"

// NoReplaceOpen atomically publishes a file and returns a handle that remains
// bound to the published inode for ownership-aware rollback.
func NoReplaceOpen(source, destination string) (*os.File, error) {
	file, err := os.Open(source)
	if err != nil {
		return nil, err
	}
	if err := NoReplace(source, destination); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

//go:build darwin

package filepublish

import "golang.org/x/sys/unix"

func NoReplace(source, destination string) error {
	return unix.RenamexNp(source, destination, unix.RENAME_EXCL)
}

func Replace(source, destination string) error {
	return unix.Rename(source, destination)
}

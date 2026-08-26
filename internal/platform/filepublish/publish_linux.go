//go:build linux

package filepublish

import "golang.org/x/sys/unix"

func NoReplace(source, destination string) error {
	return unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
}

func Replace(source, destination string) error {
	return unix.Rename(source, destination)
}

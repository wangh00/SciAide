//go:build !windows

package main

func reportStartupError(err error) {
	logStartupError(err)
}

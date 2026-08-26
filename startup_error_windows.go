//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	messageBoxOK            = 0x00000000
	messageBoxIconError     = 0x00000010
	messageBoxSetForeground = 0x00010000
)

var messageBoxW = windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")

func reportStartupError(err error) {
	logStartupError(err)
	message, messageErr := windows.UTF16PtrFromString(startupErrorMessage(err))
	title, titleErr := windows.UTF16PtrFromString("SciAide 启动失败")
	if messageErr != nil || titleErr != nil {
		return
	}
	_, _, _ = messageBoxW.Call(
		0,
		uintptr(unsafe.Pointer(message)),
		uintptr(unsafe.Pointer(title)),
		messageBoxOK|messageBoxIconError|messageBoxSetForeground,
	)
}

//go:build windows && !bindings

package main

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const existingWindowWait = 8 * time.Second

var (
	user32              = windows.NewLazySystemDLL("user32.dll")
	findWindowW         = user32.NewProc("FindWindowW")
	isIconic            = user32.NewProc("IsIconic")
	setForegroundWindow = user32.NewProc("SetForegroundWindow")
	showWindow          = user32.NewProc("ShowWindow")
)

func acquireApplicationInstance() (func(), bool, error) {
	release, primary, err := acquireProcessMutex(singleInstanceMutexName)
	if err != nil || primary {
		return release, primary, err
	}
	activateExistingWindow(singleInstanceWindowClass, existingWindowWait)
	return release, false, nil
}

func acquireProcessMutex(name string) (func(), bool, error) {
	namePointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return func() {}, false, fmt.Errorf("encode mutex name: %w", err)
	}
	handle, err := windows.CreateMutex(nil, false, namePointer)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
		return func() {}, false, nil
	}
	if err != nil {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
		return func() {}, false, fmt.Errorf("create mutex: %w", err)
	}
	if handle == 0 {
		return func() {}, false, errors.New("create mutex returned an invalid handle")
	}
	var once sync.Once
	return func() {
		once.Do(func() { _ = windows.CloseHandle(handle) })
	}, true, nil
}

func activateExistingWindow(className string, timeout time.Duration) {
	classPointer, err := windows.UTF16PtrFromString(className)
	if err != nil {
		return
	}
	deadline := time.Now().Add(timeout)
	for {
		window, _, _ := findWindowW.Call(uintptr(unsafe.Pointer(classPointer)), 0)
		if window != 0 {
			minimised, _, _ := isIconic.Call(window)
			if minimised != 0 {
				showWindow.Call(window, 9) // SW_RESTORE
			} else {
				showWindow.Call(window, 5) // SW_SHOW
			}
			setForegroundWindow.Call(window)
			return
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

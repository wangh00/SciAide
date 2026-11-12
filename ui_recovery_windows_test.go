//go:build windows && !bindings

package main

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestNativeRecoveryDoesNotTreatHostAsWebViewRenderer(t *testing.T) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if isWebViewRenderer(handle) {
		t.Fatal("host confused with renderer")
	}
	for _, pid := range descendantRenderers(uint32(os.Getpid())) {
		if pid == uint32(os.Getpid()) {
			t.Fatal("host included in renderer candidates")
		}
	}
}

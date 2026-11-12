//go:build windows && !bindings

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unsafe"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/wangh00/SciAide/internal/transport/wails"
	"golang.org/x/sys/windows"
)

// Observe only WebView renderer children of this host. Holding a process handle
// prevents PID reuse from being confused with a failure; no other applications
// are queried for command lines, changed or terminated.
func startUIRecovery(ctx context.Context, system *wails.SystemFacade) func() {
	stop, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		tracked := map[uint32]windows.Handle{}
		defer func() {
			for _, handle := range tracked {
				_ = windows.CloseHandle(handle)
			}
		}()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		lastTick := time.Now()
		for {
			select {
			case <-stop.Done():
				return
			case now := <-ticker.C:
				if now.Sub(lastTick) > 20*time.Second {
					wails.SuspendUIRecovery(system)
				}
				lastTick = now
				for pid, handle := range tracked {
					state, err := windows.WaitForSingleObject(handle, 0)
					if err != nil || state != windows.WAIT_OBJECT_0 {
						continue
					}
					var code uint32
					if windows.GetExitCodeProcess(handle, &code) == nil && code != 0 {
						wails.ObserveRendererExit(system, fmt.Sprintf("%d:%08x", pid, code))
					}
					_ = windows.CloseHandle(handle)
					delete(tracked, pid)
				}
				for _, pid := range descendantRenderers(uint32(os.Getpid())) {
					if _, exists := tracked[pid]; exists {
						continue
					}
					handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
					if err != nil {
						continue
					}
					if isWebViewRenderer(handle) {
						tracked[pid] = handle
					} else {
						_ = windows.CloseHandle(handle)
					}
				}
				if wails.RecoverUI(system, !wailsruntime.WindowIsMinimised(ctx)) {
					// Release builds patch this API with native navigation:
					// stock Wails routes both reload APIs through the failed page JS.
					wailsruntime.WindowReloadApp(ctx)
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}

func descendantRenderers(host uint32) []uint32 {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snapshot)
	var entries []windows.ProcessEntry32
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		entries = append(entries, entry)
	}
	children := map[uint32]bool{host: true}
	for changed := true; changed; {
		changed = false
		for _, value := range entries {
			if children[value.ParentProcessID] && !children[value.ProcessID] {
				children[value.ProcessID] = true
				changed = true
			}
		}
	}
	var result []uint32
	for _, value := range entries {
		if value.ProcessID != host && children[value.ProcessID] && strings.EqualFold(windows.UTF16ToString(value.ExeFile[:]), "msedgewebview2.exe") {
			result = append(result, value.ProcessID)
		}
	}
	return result
}

func isWebViewRenderer(handle windows.Handle) bool {
	image := make([]uint16, 32768)
	n := uint32(len(image))
	if windows.QueryFullProcessImageName(handle, 0, &image[0], &n) != nil || !strings.EqualFold(filepath.Base(windows.UTF16ToString(image[:n])), "msedgewebview2.exe") {
		return false
	}
	var length uint32
	_ = windows.NtQueryInformationProcess(handle, windows.ProcessCommandLineInformation, nil, 0, &length)
	if length < uint32(unsafe.Sizeof(windows.NTUnicodeString{})) || length > 256*1024 {
		return false
	}
	data := make([]byte, length)
	if windows.NtQueryInformationProcess(handle, windows.ProcessCommandLineInformation, unsafe.Pointer(&data[0]), length, &length) != nil {
		return false
	}
	command := (*windows.NTUnicodeString)(unsafe.Pointer(&data[0])).String()
	runtime.KeepAlive(data)
	for _, arg := range strings.Fields(command) {
		if arg == "--type=renderer" {
			return true
		}
	}
	return false
}

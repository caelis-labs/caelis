//go:build windows

package runnercmd

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func hideCurrentUserProfileDir() {
	profile := strings.TrimSpace(os.Getenv("USERPROFILE"))
	if !shouldHideCurrentUserProfileDir(profile) {
		return
	}
	ptr, err := windows.UTF16PtrFromString(profile)
	if err != nil {
		return
	}
	attrs, err := windows.GetFileAttributes(ptr)
	if err != nil || attrs == windows.INVALID_FILE_ATTRIBUTES {
		return
	}
	next := attrs | syscall.FILE_ATTRIBUTE_HIDDEN | syscall.FILE_ATTRIBUTE_SYSTEM
	if next == attrs {
		return
	}
	_, _, _ = syscall.SyscallN(
		windows.NewLazySystemDLL("kernel32.dll").NewProc("SetFileAttributesW").Addr(),
		uintptr(unsafe.Pointer(ptr)),
		uintptr(next),
	)
}

func shouldHideCurrentUserProfileDir(profile string) bool {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(filepath.Base(profile)))
	return strings.HasPrefix(name, "caelissbxoff") ||
		strings.HasPrefix(name, "caelissbxon") ||
		strings.HasPrefix(name, "caelissandboxoffline") ||
		strings.HasPrefix(name, "caelissandboxonline")
}

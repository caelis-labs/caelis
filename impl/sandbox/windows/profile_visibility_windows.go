//go:build windows

package windows

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	xwindows "golang.org/x/sys/windows"
)

func restoreCurrentUserProfileVisibilityBestEffort() {
	profile := strings.TrimSpace(os.Getenv("USERPROFILE"))
	if !shouldRestoreCurrentUserProfileVisibility(profile) {
		return
	}
	_ = clearHiddenSystemAttributes(profile)
}

func shouldRestoreCurrentUserProfileVisibility(profile string) bool {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return false
	}
	clean := filepath.Clean(profile)
	name := strings.ToLower(strings.TrimSpace(filepath.Base(clean)))
	if isCaelisSandboxProfileName(name) {
		return false
	}
	parent := strings.ToLower(strings.TrimSpace(filepath.Base(filepath.Dir(clean))))
	return parent == "users"
}

func isCaelisSandboxProfileName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(name, "caelissbxoff") ||
		strings.HasPrefix(name, "caelissbxon") ||
		strings.HasPrefix(name, "caelissandboxoffline") ||
		strings.HasPrefix(name, "caelissandboxonline")
}

func clearHiddenSystemAttributes(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	ptr, err := xwindows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attrs, err := xwindows.GetFileAttributes(ptr)
	if err != nil || attrs == xwindows.INVALID_FILE_ATTRIBUTES {
		return err
	}
	next := attrs &^ (syscall.FILE_ATTRIBUTE_HIDDEN | syscall.FILE_ATTRIBUTE_SYSTEM)
	if next == attrs {
		return nil
	}
	r1, _, callErr := syscall.SyscallN(
		xwindows.NewLazySystemDLL("kernel32.dll").NewProc("SetFileAttributesW").Addr(),
		uintptr(unsafe.Pointer(ptr)),
		uintptr(next),
	)
	if r1 == 0 {
		return callErr
	}
	return nil
}

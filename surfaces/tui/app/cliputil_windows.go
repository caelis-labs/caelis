//go:build windows

package tuiapp

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsClipboardUnicodeText = 13
	windowsGlobalMoveable       = 0x0002
	windowsGlobalZeroInit       = 0x0040
)

var (
	errWindowsClipboardBusy = errors.New("windows clipboard is busy")

	windowsClipboardOpenProc  = windows.NewLazySystemDLL("user32.dll").NewProc("OpenClipboard")
	windowsClipboardCloseProc = windows.NewLazySystemDLL("user32.dll").NewProc("CloseClipboard")
	windowsClipboardEmptyProc = windows.NewLazySystemDLL("user32.dll").NewProc("EmptyClipboard")
	windowsClipboardSetProc   = windows.NewLazySystemDLL("user32.dll").NewProc("SetClipboardData")
	windowsGlobalAllocProc    = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalAlloc")
	windowsGlobalLockProc     = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalLock")
	windowsGlobalUnlockProc   = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalUnlock")
	windowsGlobalFreeProc     = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalFree")
)

func writeWindowsClipboardText(text string) error {
	deadline := time.Now().Add(clipboardCommandTimeoutFor(clipboardCommand{}))
	var last error
	for {
		err := writeWindowsClipboardTextOnce(text)
		if err == nil {
			return nil
		}
		last = err
		if !errors.Is(err, errWindowsClipboardBusy) || time.Now().After(deadline) {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func writeWindowsClipboardTextOnce(text string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	data := windowsClipboardUTF16(text)
	byteLen := uintptr(len(data) * 2)
	handle, _, err := windowsGlobalAllocProc.Call(windowsGlobalMoveable|windowsGlobalZeroInit, byteLen)
	if handle == 0 {
		return windowsClipboardCallError("GlobalAlloc", err)
	}
	owned := true
	defer func() {
		if owned {
			_, _, _ = windowsGlobalFreeProc.Call(handle)
		}
	}()

	ptr, _, err := windowsGlobalLockProc.Call(handle)
	if ptr == 0 {
		return windowsClipboardCallError("GlobalLock", err)
	}
	target := unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), len(data))
	copy(target, data)
	runtime.KeepAlive(data)
	if ok, _, err := windowsGlobalUnlockProc.Call(handle); ok == 0 {
		if errno, ok := err.(syscall.Errno); ok && errno != 0 {
			return windowsClipboardCallError("GlobalUnlock", err)
		}
	}

	if ok, _, err := windowsClipboardOpenProc.Call(0); ok == 0 {
		return fmt.Errorf("%w: %v", errWindowsClipboardBusy, windowsClipboardCallError("OpenClipboard", err))
	}
	defer windowsClipboardCloseProc.Call()

	if ok, _, err := windowsClipboardEmptyProc.Call(); ok == 0 {
		return windowsClipboardCallError("EmptyClipboard", err)
	}
	if ok, _, err := windowsClipboardSetProc.Call(windowsClipboardUnicodeText, handle); ok == 0 {
		return windowsClipboardCallError("SetClipboardData", err)
	}
	owned = false
	return nil
}

func windowsClipboardUTF16(text string) []uint16 {
	encoded := utf16.Encode([]rune(text))
	return append(encoded, 0)
}

func windowsClipboardCallError(name string, err error) error {
	if errno, ok := err.(syscall.Errno); ok && errno == 0 {
		return fmt.Errorf("%s failed", name)
	}
	return fmt.Errorf("%s: %w", name, err)
}

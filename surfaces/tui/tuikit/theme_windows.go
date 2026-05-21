//go:build windows

package tuikit

import "golang.org/x/sys/windows"

func darkBackgroundFromPlatform() (bool, bool) {
	handle, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	if err != nil || handle == 0 || handle == windows.InvalidHandle {
		return false, false
	}
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(handle, &info); err != nil {
		return false, false
	}
	return terminalColorIndexIsDark(int((info.Attributes >> 4) & 0x0f))
}

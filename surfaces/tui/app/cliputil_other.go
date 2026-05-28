//go:build !windows

package tuiapp

import "fmt"

func writeWindowsClipboardText(text string) error {
	return fmt.Errorf("windows clipboard write is unsupported on %s", clipboardGOOS)
}

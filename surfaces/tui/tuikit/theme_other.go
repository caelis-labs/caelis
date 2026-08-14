//go:build !windows

package tuikit

func darkBackgroundFromPlatform() (bool, bool) {
	return false, false
}

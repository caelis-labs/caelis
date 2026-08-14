package testenv

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// SetHome makes os.UserHomeDir deterministic on both Unix and Windows.
func SetHome(t testing.TB, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	if runtime.GOOS != "windows" {
		return
	}
	t.Setenv("USERPROFILE", home)
	volume := filepath.VolumeName(home)
	if volume == "" {
		return
	}
	t.Setenv("HOMEDRIVE", volume)
	t.Setenv("HOMEPATH", strings.TrimPrefix(home, volume))
}

// CommandScriptName adds the Windows command-script suffix used by PATH lookup.
func CommandScriptName(name string) string {
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		return name + ".cmd"
	}
	return name
}

// ExecutableName adds the Windows executable suffix for built test binaries.
func ExecutableName(name string) string {
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		return name + ".exe"
	}
	return name
}

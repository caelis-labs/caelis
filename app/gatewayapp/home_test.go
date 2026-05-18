package gatewayapp

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func setHomeForGatewayAppTest(t *testing.T, home string) {
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

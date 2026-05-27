//go:build !windows

package winproc

import "os/exec"

func ConfigureHiddenConsole(*exec.Cmd) {}

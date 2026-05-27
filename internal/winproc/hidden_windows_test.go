//go:build windows

package winproc

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestConfigureHiddenConsolePreservesExistingSysProcAttr(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "echo ok")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x4}

	ConfigureHiddenConsole(cmd)

	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr = nil")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Fatal("HideWindow = false, want true")
	}
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("CreationFlags = %#x, want CREATE_NO_WINDOW", cmd.SysProcAttr.CreationFlags)
	}
	if cmd.SysProcAttr.CreationFlags&0x4 == 0 {
		t.Fatalf("CreationFlags = %#x, want existing flags preserved", cmd.SysProcAttr.CreationFlags)
	}
}

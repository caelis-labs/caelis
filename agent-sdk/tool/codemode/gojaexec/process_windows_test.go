//go:build windows

package gojaexec

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"golang.org/x/sys/windows"
)

func TestWorkerWindowsDetachedHost(t *testing.T) {
	t.Parallel()
	if os.Getenv("CAELIS_GOJA_WINDOWS_HOST") != "1" {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		parent := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkerWindowsDetachedHost$", "-test.v")
		parent.Env = append(os.Environ(), "CAELIS_GOJA_WINDOWS_HOST=1")
		parent.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
		out, err := parent.CombinedOutput()
		if err != nil {
			t.Fatalf("detached Host: %v\n%s", err, out)
		}
		t.Logf("detached Host:\n%s", out)
		return
	}
	executor := &Executor{}
	env := executor.NewEnvironment().(*environment)
	defer env.Close()
	if out, err := env.Execute(t.Context(), fixed(`return 1;`), codemode.Limits{}, testHost{}); err != nil || string(out.Value) != "1" {
		t.Fatalf("worker execution: %s, %v", out.Value, err)
	}
	kernel := windows.NewLazySystemDLL("kernel32.dll")
	_, _, _ = kernel.NewProc("AttachConsole").Call(uintptr(env.child.cmd.Process.Pid))
	window, _, _ := kernel.NewProc("GetConsoleWindow").Call()
	_, _, _ = kernel.NewProc("FreeConsole").Call()
	if window != 0 {
		t.Fatal("pipe worker allocated a console")
	}
	if out, err := env.Execute(t.Context(), fixed(`return 2;`), codemode.Limits{}, testHost{}); err != nil || string(out.Value) != "2" {
		t.Fatalf("persistent worker stdio: %s, %v", out.Value, err)
	}
	if err := env.child.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-env.child.done
	if _, err := env.Execute(t.Context(), fixed(`return 3;`), codemode.Limits{}, testHost{}); err == nil {
		t.Fatal("terminated worker did not report its failure")
	}
	fresh := executor.NewEnvironment()
	defer fresh.Close()
	if out, err := fresh.Execute(t.Context(), fixed(`return 4;`), codemode.Limits{}, testHost{}); err != nil || string(out.Value) != "4" {
		t.Fatalf("independent Session after worker exit: %s, %v", out.Value, err)
	}
	t.Log("no worker console; persistent stdio and independent Session execution preserved")
}

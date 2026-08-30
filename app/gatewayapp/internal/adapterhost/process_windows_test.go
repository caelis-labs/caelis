//go:build windows

package adapterhost

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const adapterHostJobHelper = "CAELIS_ADAPTERHOST_JOB_HELPER"

func TestProcessTreeTerminatesDescendants(t *testing.T) {
	if os.Getenv(adapterHostJobHelper) != "" {
		child := exec.Command("cmd.exe", "/c", "ping", "-t", "127.0.0.1")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Getenv(adapterHostJobHelper), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			os.Exit(3)
		}
		for {
			time.Sleep(time.Hour)
		}
	}

	pidFile := t.TempDir() + `\child.pid`
	command := exec.Command(os.Args[0], "-test.run=^TestProcessTreeTerminatesDescendants$")
	command.Env = append(os.Environ(), adapterHostJobHelper+"="+pidFile)
	process, err := prepareProcess(command)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		_ = killProcess(command, process)
		if !waited {
			_ = command.Wait()
		}
	}()
	if err := process.Started(command); err != nil {
		t.Fatal(err)
	}

	var childPID uint64
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, readErr := os.ReadFile(pidFile)
		if readErr == nil {
			childPID, err = strconv.ParseUint(string(raw), 10, 32)
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("helper descendant did not start")
	}
	child, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(childPID))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := windows.CloseHandle(child); closeErr != nil {
			t.Errorf("close descendant process handle: %v", closeErr)
		}
	}()

	if err := killProcess(command, process); err != nil {
		t.Fatal(err)
	}
	// Termination is expected to produce a non-zero exit status; Wait still
	// has to complete so the descendant check is meaningful.
	_ = command.Wait()
	waited = true
	event, err := windows.WaitForSingleObject(child, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if event != windows.WAIT_OBJECT_0 {
		t.Fatalf("descendant still running after Job termination: wait event %#x", event)
	}
}

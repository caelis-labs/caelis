//go:build windows

package capability

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreFileLockReleasedWhenHolderProcessTerminates(t *testing.T) {
	if os.Getenv("CAELIS_STORE_LOCK_CRASH_HELPER") == "1" {
		storePath := os.Getenv("CAELIS_STORE_LOCK_CRASH_STORE")
		readyPath := os.Getenv("CAELIS_STORE_LOCK_CRASH_READY")
		unlock, err := lockStore(storePath)
		if err != nil {
			t.Fatalf("lockStore(helper) error = %v", err)
		}
		defer unlock()
		if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
			t.Fatalf("WriteFile(ready) error = %v", err)
		}
		for {
			time.Sleep(time.Second)
		}
	}

	dir := t.TempDir()
	storePath := filepath.Join(dir, "cap_sids.json")
	readyPath := filepath.Join(dir, "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestStoreFileLockReleasedWhenHolderProcessTerminates$")
	cmd.Env = append(os.Environ(),
		"CAELIS_STORE_LOCK_CRASH_HELPER=1",
		"CAELIS_STORE_LOCK_CRASH_STORE="+storePath,
		"CAELIS_STORE_LOCK_CRASH_READY="+readyPath,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start(helper) error = %v", err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not acquire Store lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if lock, contended, err := tryAcquireStoreFileLock(storePath + ".lock"); err != nil || !contended || lock != nil {
		t.Fatalf("tryAcquireStoreFileLock(while helper alive) = %#v/%v/%v, want contention", lock, contended, err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("Kill(helper) error = %v", err)
	}
	_ = cmd.Wait()

	deadline = time.Now().Add(5 * time.Second)
	for {
		lock, contended, err := tryAcquireStoreFileLock(storePath + ".lock")
		if err == nil && !contended {
			if releaseErr := releaseStoreFileLock(lock); releaseErr != nil {
				t.Fatalf("releaseStoreFileLock() error = %v", releaseErr)
			}
			return
		}
		if err != nil {
			t.Fatalf("tryAcquireStoreFileLock(after crash) error = %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("kernel Store lock was not released after holder process termination")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

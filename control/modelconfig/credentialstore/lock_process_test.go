package credentialstore

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestReferenceFileLockHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_CREDENTIALSTORE_LOCK_HELPER") != "1" {
		return
	}
	lock, err := acquireReferenceFileLock(context.Background(), os.Getenv("CAELIS_CREDENTIALSTORE_LOCK_PATH"))
	if err != nil {
		fmt.Fprintf(os.Stdout, "error: %v\n", err)
		os.Exit(2)
	}
	fmt.Fprintln(os.Stdout, "ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
	if err := lock.Close(); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestReferenceLockSerializesAcrossProcesses(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := BuildReference("openai", "cross-process")
	lockPath := store.lockPath(ref)
	if err := ensureDir(filepath.Dir(lockPath)); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestReferenceFileLockHelperProcess$")
	cmd.Env = append(os.Environ(),
		"CAELIS_CREDENTIALSTORE_LOCK_HELPER=1",
		"CAELIS_CREDENTIALSTORE_LOCK_PATH="+lockPath,
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)
	if line, readErr := reader.ReadString('\n'); readErr != nil || line != "ready\n" {
		_ = stdin.Close()
		_ = cmd.Wait()
		t.Fatalf("helper readiness = %q, %v", line, readErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	if lock, lockErr := store.acquireReferenceLock(ctx, ref); !errors.Is(lockErr, context.DeadlineExceeded) {
		if lock != nil {
			_ = lock.Close()
		}
		t.Fatalf("cross-process lock error = %v, want deadline exceeded", lockErr)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	lock, err := store.acquireReferenceLock(context.Background(), ref)
	if err != nil {
		t.Fatalf("acquire after helper exit: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("release after helper exit: %v", err)
	}
}

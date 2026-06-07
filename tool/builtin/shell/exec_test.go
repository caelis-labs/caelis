package shell

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/OnslaughtSnail/caelis/sandbox"
)

func TestRunCommandEcho(t *testing.T) {
	cmd := "echo hello"
	if runtime.GOOS == "windows" {
		cmd = "cmd /C echo hello"
	}

	result, err := executeHostCommand(t.Context(), sandbox.CommandRequest{
		Command: cmd,
		Timeout: 10,
	})
	if err != nil {
		t.Fatalf("executeHostCommand: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code: got %d, want 0", result.ExitCode)
	}
	if len(result.Stdout) == 0 {
		t.Error("expected non-empty stdout")
	}
}

func TestRunCommandFailure(t *testing.T) {
	cmd := "false"
	if runtime.GOOS == "windows" {
		cmd = "cmd /C exit 1"
	}

	result, err := executeHostCommand(t.Context(), sandbox.CommandRequest{
		Command: cmd,
		Timeout: 10,
	})
	if err != nil {
		t.Fatalf("executeHostCommand: %v", err)
	}
	if result.ExitCode == 0 {
		t.Error("expected non-zero exit code")
	}
}

func TestRunCommandTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("timeout test not reliable on windows")
	}

	result, err := executeHostCommand(t.Context(), sandbox.CommandRequest{
		Command: "sleep 60",
		Timeout: 1,
	})
	if err != nil {
		// Timeout may produce an error — that's fine.
		return
	}
	// If no error, the command should have been killed.
	if result.ExitCode == 0 {
		t.Error("expected non-zero exit code for timeout")
	}
}

func TestRunCommandConstraintsDeniedWorkdir(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "workspace")
	denied := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatalf("mkdir allowed: %v", err)
	}
	if err := os.MkdirAll(denied, 0o755); err != nil {
		t.Fatalf("mkdir denied: %v", err)
	}

	_, err := executeHostCommand(t.Context(), sandbox.CommandRequest{
		Command: "echo denied",
		Dir:     denied,
		Constraints: sandbox.Constraints{
			Paths: []sandbox.PathRule{{Path: allowed, Access: sandbox.PathAccessWrite}},
		},
	})
	if err == nil {
		t.Fatal("expected denied workdir error")
	}
}

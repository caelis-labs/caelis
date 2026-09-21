package acpinstall

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/agents"
)

func TestManualSetupCommandsWorkWithQuotedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable permission instructions")
	}
	directory := filepath.Join(t.TempDir(), "user's tools")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	setup := agents.RuntimeSetup{Directory: directory, Command: "agy_acp_server.par"}
	for _, name := range []string{setup.Command, "localharness_external"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	steps := manualSetupSteps(setup, runtime.GOOS)
	_, commands, ok := strings.Cut(steps[2], "\n")
	if !ok {
		t.Fatal("missing executable permission commands")
	}
	if out, err := exec.Command("sh", "-c", commands).CombinedOutput(); err != nil {
		t.Fatalf("manual commands: %s %v", out, err)
	}
	for _, name := range []string{setup.Command, "localharness_external"} {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil || info.Mode()&0o100 == 0 {
			t.Fatalf("%s is not executable: %v", name, err)
		}
	}
	if err := os.Remove(filepath.Join(directory, "localharness_external")); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-c", commands).CombinedOutput(); err != nil {
		t.Fatalf("optional companion: %s %v", out, err)
	}
}

func TestWindowsManualSetupUsesExtractionWithoutPOSIXCommands(t *testing.T) {
	steps := strings.Join(manualSetupSteps(agents.RuntimeSetup{Directory: `C:\Users\me\AppData\Local\antigravity-acp`, Command: "agy_acp_server.exe"}, "windows"), "\n")
	if !strings.Contains(steps, "Extract all files") || !strings.Contains(steps, "Check installation") || strings.Contains(steps, "chmod") {
		t.Fatalf("Windows guide: %s", steps)
	}
}

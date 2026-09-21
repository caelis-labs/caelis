package acpinstall

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/agents"
)

func TestSetupFollowsCurrentHostUser(t *testing.T) {
	source := Source{DirectoryName: "antigravity-acp", Command: "agy_acp_server.par"}
	for range 2 {
		home, localAppData := t.TempDir(), t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("LOCALAPPDATA", localAppData)
		// Windows discovery only requires LOCALAPPDATA, not USERPROFILE.
		t.Setenv("USERPROFILE", "")
		want := filepath.Join(home, ".local", "share", "antigravity-acp")
		if runtime.GOOS == "windows" {
			want = filepath.Join(localAppData, "antigravity-acp")
		}
		setup, err := source.Setup()
		if err != nil || setup.Directory != want {
			t.Fatalf("Host directory = %q, %v; want %q", setup.Directory, err, want)
		}
	}
}

func TestInstallationPlanAndAgentPromptByPlatform(t *testing.T) {
	binaries := map[string]binaryDistribution{}
	for _, platform := range []string{"darwin-aarch64", "linux-x86_64", "linux-aarch64", "windows-x86_64", "windows-aarch64"} {
		binary := binaryDistribution{Archive: "https://dl.google.com/agy-extensions/releases/" + platform + ".zip", Cmd: "./agy_acp_server.par"}
		if strings.HasPrefix(platform, "windows-") {
			binary.Cmd = "./agy_acp_server.exe"
		}
		if strings.HasPrefix(platform, "linux-") {
			binary.Args = []string{"--uid="}
		}
		binaries[platform] = binary
	}
	for _, test := range []struct{ goos, goarch, platform string }{
		{"darwin", "arm64", "darwin-aarch64"},
		{"linux", "amd64", "linux-x86_64"},
		{"linux", "arm64", "linux-aarch64"},
		{"windows", "amd64", "windows-x86_64"},
		{"windows", "arm64", "windows-aarch64"},
	} {
		t.Run(test.platform, func(t *testing.T) {
			command, directory := "agy_acp_server.par", "/home/用户's tools/antigravity-acp"
			var args []string
			if test.goos == "windows" {
				command, directory = "agy_acp_server.exe", `C:\Users\用户's tools\AppData\Local\antigravity-acp`
			}
			if test.goos == "linux" {
				args = []string{"--uid="}
			}
			source := Source{Command: command, Args: args, ArchiveHost: "dl.google.com", ArchivePathPrefix: "/agy-extensions/releases/"}
			plan, err := source.resolvePlan(agents.RuntimeSetup{Command: command, Directory: directory}, binaries, test.goos, test.goarch)
			if err != nil || plan.ArchiveURL != binaries[test.platform].Archive {
				t.Fatalf("wrong platform archive: %#v, %v", plan, err)
			}
			var facts struct {
				OS, Arch, Directory, Command string
				Archive                      string `json:"archive_url"`
				Args                         []string
			}
			parts := strings.Split(plan.InstallPrompt, "\n\n")
			if len(parts) < 3 || json.Unmarshal([]byte(parts[2]), &facts) != nil {
				t.Fatalf("unreadable Host facts: %s", plan.InstallPrompt)
			}
			if facts.OS != test.goos || facts.Arch != test.goarch || facts.Directory != directory || facts.Command != command || facts.Archive != plan.ArchiveURL || !slices.Equal(facts.Args, args) {
				t.Fatalf("prompt lost Host facts: %#v", facts)
			}
			manual := strings.Join(plan.ManualSteps, "\n")
			if !strings.Contains(manual, directory) {
				t.Fatal("manual instructions lost destination")
			}
			if test.goos == "windows" {
				if strings.Contains(manual, "chmod") || !strings.Contains(plan.InstallPrompt, "PowerShell Invoke-WebRequest and Expand-Archive") {
					t.Fatal("Windows installation uses POSIX steps")
				}
			} else if !strings.Contains(manual, `cd '/home/用户'"'"'s tools/antigravity-acp'`) || !strings.Contains(plan.InstallPrompt, "Set executable permission") {
				t.Fatal("POSIX installation lost quoted paths or executable permissions")
			}
			for _, want := range []string{"complete bundle", "ACP initialize", "Stop the verification process", "Browser sign-in remains my action"} {
				if !strings.Contains(plan.InstallPrompt, want) {
					t.Fatalf("installation task missing %q", want)
				}
			}
		})
	}
	source := Source{Command: "agy_acp_server.par", ArchiveHost: "dl.google.com", ArchivePathPrefix: "/agy-extensions/releases/"}
	for _, test := range []struct{ goos, goarch string }{{"darwin", "amd64"}, {"linux", "386"}, {"freebsd", "amd64"}} {
		if _, err := source.resolvePlan(agents.RuntimeSetup{}, binaries, test.goos, test.goarch); err == nil {
			t.Fatalf("offered unsupported platform %v", test)
		}
	}
	if _, err := source.resolvePlan(agents.RuntimeSetup{}, binaries, "linux", "amd64"); err == nil {
		t.Fatal("accepted registry arguments differing from catalog launch arguments")
	}
}

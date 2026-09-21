package tuiapp

import (
	"context"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/charmbracelet/x/ansi"
)

func acpInstallationWizardFixture(t *testing.T) (*Model, *[]agents.ConnectState, *bool) {
	t.Helper()
	requests := []agents.ConnectState{}
	installed := false
	setup := agents.RuntimeSetup{Command: "agy_acp_server.par", Directory: "/home/me/.local/share/antigravity-acp"}
	m := NewModel(Config{Commands: DefaultCommands(), Wizards: DefaultWizards(),
		ExecuteLine: func(submission Submission) TaskResultMsg {
			state, err := parseACPConnectWizardPayload(strings.TrimPrefix(submission.Text, "/connect acp "))
			if err != nil {
				t.Fatal(err)
			}
			requests = append(requests, state)
			return TaskResultMsg{}
		},
		SlashArgComplete: func(_ context.Context, command, query string, _ int) ([]SlashArgCandidate, error) {
			switch {
			case command == "connect":
				return []SlashArgCandidate{{Value: "acp", Display: "Local ACP Agent"}}, nil
			case command == "connect-acp-agent":
				return []SlashArgCandidate{{Value: "antigravity", Display: "Google Antigravity"}}, nil
			case command == "connect-acp-launcher:antigravity":
				if installed {
					return []SlashArgCandidate{{Value: "installed", Display: "Installed command"}}, nil
				}
				return []SlashArgCandidate{{Value: "install", Display: "Install agy_acp_server.par", RuntimeSetup: &setup}, {Value: "manual", Display: "Manual setup", RuntimeSetup: &setup}}, nil
			case command == "connect-acp-install:antigravity":
				plan := setup
				plan.ArchiveURL = "https://dl.google.com/agy-extensions/releases/runtime.zip"
				plan.InstallPrompt = "Install the official Antigravity ACP runtime on this Host.\nDestination: " + setup.Directory + "\nArchive: " + plan.ArchiveURL
				plan.ManualSteps = []string{"1. Download the ZIP using the link above.", "2. Create this directory. Extract all files together into it:\n" + setup.Directory, "3. In a terminal on the Host, run:\ncd '" + setup.Directory + "'\nchmod +x agy_acp_server.par\n[ ! -f localharness_external ] || chmod +x localharness_external", "4. Choose Check installation to sign in and select a model.", "For updates, stop Antigravity sessions before replacing the entire runtime bundle."}
				return []SlashArgCandidate{{Value: "confirm", RuntimeSetup: &plan}}, nil
			case strings.HasPrefix(command, "connect-acp-model:"):
				state, err := parseACPConnectWizardPayload(strings.TrimPrefix(command, "connect-acp-model:"))
				if err != nil {
					t.Fatal(err)
				}
				requests = append(requests, state)
				return []SlashArgCandidate{{Value: "remote-model", Display: "Remote model"}}, nil
			}
			return nil, nil
		},
	})
	m.width, m.height, m.ready = 100, 30, true
	m.statusView.Model = "existing-model"
	runConnectTestCmd(m, m.openSlashArgPicker("connect"))
	connectPress(m, "enter")
	connectPress(m, "enter")
	return m, &requests, &installed
}

func TestACPInstallationWizardRequiresConfirmationAndContinues(t *testing.T) {
	m, requests, _ := acpInstallationWizardFixture(t)
	for _, size := range [][2]int{{120, 30}, {80, 24}, {40, 18}} {
		m.width, m.height = size[0], size[1]
		frame := ansi.Strip(m.renderWizardOverlay())
		for _, want := range []string{"Install agy_acp_server.par", "Manual setup"} {
			if !strings.Contains(frame, want) {
				t.Fatalf("%v: missing %q\n%s", size, want, frame)
			}
		}
		for _, bad := range []string{"HTTP", "gatewayapp:", "Nothing configured", "Search"} {
			if strings.Contains(frame, bad) {
				t.Fatalf("setup exposed %q\n%s", bad, frame)
			}
		}
		for _, line := range strings.Split(frame, "\n") {
			if ansi.StringWidth(line) > size[0]-4 {
				t.Fatalf("setup overflow: %s", line)
			}
		}
		t.Logf("setup %dx%d:\n%s", size[0], size[1], frame)
	}
	m.width, m.height = 100, 30
	connectPress(m, "enter")
	if m.wizardStepKey() != "acp_install" || len(*requests) != 0 || len(m.wizardOverlay.fields) != 1 {
		t.Fatalf("installation started before confirmation: %s %#v", m.wizardStepKey(), requests)
	}
	frame := ansi.Strip(m.renderWizardOverlay())
	for _, want := range []string{"dl.google.com", "Directory", "Install and continue"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("confirmation missing %q\n%s", want, frame)
		}
	}
	t.Logf("confirmation:\n%s", frame)
	connectPress(m, "ctrl+u")
	connectPaste(m, "/home/me/tools/antigravity")
	connectPress(m, "enter")
	if m.wizardStepKey() != "acp_model" || len(*requests) != 1 || (*requests)[0].Install == nil || (*requests)[0].Install.Directory != "/home/me/tools/antigravity" {
		t.Fatalf("confirmed request = %#v step=%s", requests, m.wizardStepKey())
	}
	connectPress(m, "enter")
	if len(*requests) != 2 || (*requests)[1].Install == nil || (*requests)[1].Model != "remote-model" {
		t.Fatalf("final connection lost confirmed installation: %#v", requests)
	}
}

func TestACPInstallationWizardBackAndManualDoNotInstall(t *testing.T) {
	m, requests, installed := acpInstallationWizardFixture(t)
	connectPress(m, "enter")
	connectPress(m, "esc")
	if m.wizardStepKey() != "acp_launcher" || len(*requests) != 0 {
		t.Fatal("Back from confirmation installed the runtime")
	}
	connectPress(m, "down")
	connectPress(m, "enter")
	if !m.isACPManualSetup() || len(*requests) != 0 || len(m.wizardOverlay.fields) != 0 {
		t.Fatal("Manual setup must show instructions without submitting installation")
	}
	frame := m.renderWizardOverlay()
	if !strings.Contains(frame, "]8;;https://dl.google.com/agy-extensions/releases/runtime.zip") {
		t.Fatalf("missing direct ZIP download: %s", frame)
	}
	plain := ansi.Strip(frame)
	for _, want := range []string{"Drag to select text", "Extract all files", setupDirectoryForTest, "chmod +x", "Check installation"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("manual steps missing %q\n%s", want, plain)
		}
	}
	for _, bad := range []string{"/zed/", "Official setup guide", "Install and continue", "Search"} {
		if strings.Contains(frame, bad) {
			t.Fatalf("manual setup contains %q", bad)
		}
	}
	t.Logf("manual steps:\n%s", plain)
	connectPress(m, "esc")
	if m.isACPManualSetup() || m.wizardStepKey() != "acp_launcher" || len(*requests) != 0 {
		t.Fatal("Back from manual setup must return to installation choices")
	}
	m.slashArgIndex = 1
	connectPress(m, "enter")
	*installed = true
	connectPress(m, "enter")
	if m.wizardStepKey() != "acp_model" || len(*requests) != 1 || (*requests)[0].Install != nil {
		t.Fatalf("manual recheck did not reuse installed runtime: %s %#v", m.wizardStepKey(), requests)
	}
}

const setupDirectoryForTest = "/home/me/.local/share/antigravity-acp"

func TestACPInstallationPlanFailureHasCleanRetryAndBack(t *testing.T) {
	m, requests, _ := acpInstallationWizardFixture(t)
	complete := m.cfg.SlashArgComplete
	failed := true
	const detail = "could not check the official download; try again"
	m.cfg.SlashArgComplete = func(ctx context.Context, command, query string, limit int) ([]SlashArgCandidate, error) {
		if failed && command == "connect-acp-install:antigravity" {
			return nil, &httpclient.RemoteError{StatusCode: 400, Detail: detail}
		}
		return complete(ctx, command, query, limit)
	}
	connectPress(m, "enter")
	frame := ansi.Strip(m.renderWizardOverlay())
	if !strings.Contains(frame, detail) {
		t.Fatalf("plan failure lost recovery instructions\n%s", frame)
	}
	t.Logf("plan failure:\n%s", frame)
	for _, bad := range []string{"HTTP", "control http client", "Nothing configured", "Search"} {
		if strings.Contains(frame, bad) {
			t.Fatalf("plan failure exposed %q\n%s", bad, frame)
		}
	}
	if !strings.Contains(frame, "Retry") || len(*requests) != 0 {
		t.Fatalf("failed plan started install or lost retry\n%s", frame)
	}
	failed = false
	connectPress(m, "enter")
	if len(m.wizardOverlay.fields) != 1 || len(*requests) != 0 {
		t.Fatal("retry must reopen confirmation before installation")
	}
	connectPress(m, "esc")
	if m.wizardStepKey() != "acp_launcher" {
		t.Fatal("cannot return to manual setup after failed plan")
	}
}

func TestACPManualStepsScrollWithoutLosingCheckAction(t *testing.T) {
	m, requests, _ := acpInstallationWizardFixture(t)
	connectPress(m, "down")
	connectPress(m, "enter")
	m.width, m.height = 40, 18
	var observed strings.Builder
	for range 40 {
		frame := ansi.Strip(m.renderWizardOverlay())
		if !strings.Contains(frame, "Check installation") {
			t.Fatalf("check action clipped\n%s", frame)
		}
		for _, line := range strings.Split(frame, "\n") {
			if ansi.StringWidth(line) > 36 {
				t.Fatalf("manual step overflow: %s", line)
			}
		}
		observed.WriteString(frame)
		connectPress(m, "down")
	}
	if !strings.Contains(observed.String(), "chmod +x") || !strings.Contains(observed.String(), "For updates") || len(*requests) != 0 {
		t.Fatal("manual instructions were unreachable or scrolling installed the runtime")
	}
	t.Logf("scrolled manual steps:\n%s", ansi.Strip(m.renderWizardOverlay()))
}

func TestACPManualOverlayExpandsOnLargerTerminals(t *testing.T) {
	m, _, _ := acpInstallationWizardFixture(t)
	connectPress(m, "down")
	connectPress(m, "enter")
	previousWidth := 0
	for _, size := range [][2]int{{80, 24}, {140, 40}, {200, 60}} {
		m.width, m.height = size[0], size[1]
		frame := ansi.Strip(m.renderWizardOverlay())
		g := m.wizardOverlay.geometry
		if g.width <= previousWidth || g.width > m.width-4 || g.height > m.height-5 {
			t.Fatalf("overlay size %dx%d in %v", g.width, g.height, size)
		}
		previousWidth = g.width
		for _, want := range []string{"Drag to select text", "[Check installation ↵]", "Download ZIP:"} {
			if !strings.Contains(frame, want) {
				t.Fatalf("%v missing %s\n%s", size, want, frame)
			}
		}
		if size[0] >= 140 && (!strings.Contains(frame, "chmod +x") || !strings.Contains(frame, "For updates") || strings.Contains(frame, " / ")) {
			t.Fatalf("large overlay unnecessarily hides instructions\n%s", frame)
		}
		t.Logf("manual %dx%d:\n%s", size[0], size[1], frame)
	}
}

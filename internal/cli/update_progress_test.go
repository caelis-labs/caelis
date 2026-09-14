package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/internal/updater"
)

func TestUpdateProgressRendererPlainOutputUsesStableStageLines(t *testing.T) {
	var output bytes.Buffer
	renderer := &updateProgressRenderer{writer: &output}
	for _, event := range []updater.ProgressEvent{
		{Stage: updater.ProgressChecking},
		{Stage: updater.ProgressChecking, Done: true},
		{Stage: updater.ProgressInstalling, Detail: "caelis"},
		{Stage: updater.ProgressInstalling, Detail: "caelis", Done: true},
	} {
		renderer.Report(event)
	}
	want := strings.Join([]string{
		"Checking for updates…",
		"✓ Checked for updates",
		"Installing update…",
		"✓ Installed caelis",
		"",
	}, "\n")
	if output.String() != want {
		t.Fatalf("plain progress = %q, want %q", output.String(), want)
	}
}

func TestUpdateProgressRendererInteractiveRewritesStageInPlace(t *testing.T) {
	var output bytes.Buffer
	renderer := &updateProgressRenderer{writer: &output, interactive: true}
	renderer.Report(updater.ProgressEvent{Stage: updater.ProgressChecking})
	renderer.Report(updater.ProgressEvent{Stage: updater.ProgressChecking, Done: true})

	want := "\rChecking for updates…\r✓ Checked for updates\n"
	if output.String() != want {
		t.Fatalf("interactive progress = %q, want %q", output.String(), want)
	}
}

func TestUpdateProgressRendererFinishesLineBeforeForegroundInstallerOutput(t *testing.T) {
	var output bytes.Buffer
	renderer := &updateProgressRenderer{writer: &output, interactive: true}

	renderer.Report(updater.ProgressEvent{
		Stage:  updater.ProgressInstalling,
		Detail: "caelis",
	})
	renderer.Report(updater.ProgressEvent{
		Stage:  updater.ProgressInstalling,
		Detail: "caelis",
		Done:   true,
	})

	// The official installer writes its own output, so the status line must end
	// before it starts.
	want := "\rInstalling update…\n\r✓ Installed caelis\n"
	if output.String() != want {
		t.Fatalf("interactive raw progress = %q, want %q", output.String(), want)
	}
}

func TestUpdateProgressRendererFinishesLineBeforeForegroundNPMOutput(t *testing.T) {
	var output bytes.Buffer
	renderer := &updateProgressRenderer{writer: &output, interactive: true}

	renderer.Report(updater.ProgressEvent{
		Stage:  updater.ProgressInstalling,
		Detail: updater.MethodNPM,
	})
	renderer.Report(updater.ProgressEvent{
		Stage:  updater.ProgressInstalling,
		Detail: updater.MethodNPM,
		Done:   true,
	})

	want := "\rInstalling update with npm…\n\r✓ npm install completed\n"
	if output.String() != want {
		t.Fatalf("interactive npm progress = %q, want %q", output.String(), want)
	}
}

func TestUpdateProgressRendererFailIsVisibleAfterCompletedStage(t *testing.T) {
	var output bytes.Buffer
	renderer := &updateProgressRenderer{writer: &output, interactive: true}

	renderer.Report(updater.ProgressEvent{Stage: updater.ProgressChecking, Done: true})
	renderer.Fail()

	want := "\r✓ Checked for updates\n\r✗ Update failed\n"
	if output.String() != want {
		t.Fatalf("interactive failure = %q, want %q", output.String(), want)
	}
}

func TestWriteUpdateResultLeavesForegroundHandoffOutputToNPMLauncher(t *testing.T) {
	var output bytes.Buffer
	if err := writeUpdateResult(&output, updater.Result{Handoff: true}); err != nil {
		t.Fatalf("writeUpdateResult() error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("handoff output = %q, want empty", output.String())
	}
}

func TestFormatUpdateResultUsesFriendlyCompletionMessages(t *testing.T) {
	updated := formatUpdateResult(updater.Result{
		CurrentVersion: "v1.0.0",
		LatestVersion:  "v1.2.0",
		InstallMethod:  updater.MethodRaw,
		Updated:        true,
	})
	wantUpdated := "Caelis v1.2.0 is installed (updated from v1.0.0 via raw); it takes effect on the next start."
	if updated != wantUpdated {
		t.Fatalf("updated result = %q, want %q", updated, wantUpdated)
	}
	available := formatUpdateResult(updater.Result{
		CurrentVersion: "v1.0.0",
		LatestVersion:  "v1.2.0",
		InstallMethod:  updater.MethodRaw,
		Available:      true,
	})
	wantAvailable := "update available: v1.0.0 -> v1.2.0 (raw)"
	if available != wantAvailable {
		t.Fatalf("available result = %q, want %q", available, wantAvailable)
	}
}

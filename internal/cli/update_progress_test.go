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
		{Stage: updater.ProgressDownloading},
		{Stage: updater.ProgressDownloading, Current: 5 << 20, Total: 10 << 20},
		{Stage: updater.ProgressDownloading, Current: 10 << 20, Total: 10 << 20, Done: true},
		{Stage: updater.ProgressVerifying},
		{Stage: updater.ProgressVerifying, Done: true},
		{Stage: updater.ProgressInstalling, Detail: "caelis.exe"},
		{Stage: updater.ProgressInstalling, Detail: "caelis.exe", Done: true, Deferred: true},
	} {
		renderer.Report(event)
	}
	want := strings.Join([]string{
		"Checking for updates…",
		"✓ Checked for updates",
		"Downloading update…",
		"✓ Downloaded 10.0 MB",
		"Verifying checksum…",
		"✓ Checksum verified",
		"Installing update…",
		"✓ Prepared update for installation",
		"",
	}, "\n")
	if output.String() != want {
		t.Fatalf("plain progress = %q, want %q", output.String(), want)
	}
}

func TestUpdateProgressRendererInteractiveRewritesDownloadLine(t *testing.T) {
	var output bytes.Buffer
	renderer := &updateProgressRenderer{writer: &output, interactive: true}
	renderer.Report(updater.ProgressEvent{Stage: updater.ProgressDownloading})
	renderer.Report(updater.ProgressEvent{
		Stage:   updater.ProgressDownloading,
		Current: 5 << 20,
		Total:   10 << 20,
	})
	renderer.Report(updater.ProgressEvent{
		Stage:   updater.ProgressDownloading,
		Current: 10 << 20,
		Total:   10 << 20,
		Done:    true,
	})

	got := output.String()
	if strings.Count(got, "\n") != 1 {
		t.Fatalf("interactive progress newlines = %d, want 1: %q", strings.Count(got, "\n"), got)
	}
	for _, want := range []string{
		"\rDownloading update…",
		"████████████░░░░░░░░░░░░",
		"5.0 MB / 10.0 MB  50%",
		"\r✓ Downloaded 10.0 MB",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("interactive progress = %q, want fragment %q", got, want)
		}
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
	if updated != "Caelis v1.2.0 is ready (updated from v1.0.0 via raw)." {
		t.Fatalf("updated result = %q", updated)
	}
	deferred := formatUpdateResult(updater.Result{
		CurrentVersion: "v1.0.0",
		LatestVersion:  "v1.2.0",
		InstallMethod:  updater.MethodRaw,
		Deferred:       true,
	})
	wantDeferred := "Caelis v1.2.0 is prepared (current v1.0.0 via raw). Installation will finish after this process exits."
	if deferred != wantDeferred {
		t.Fatalf("deferred result = %q, want %q", deferred, wantDeferred)
	}
	degraded := formatUpdateResult(updater.Result{
		CurrentVersion: "v1.0.0",
		LatestVersion:  "v1.2.0",
		InstallMethod:  updater.MethodNPM,
		Deferred:       true,
		Reason:         "The npm launcher handoff is unavailable; wait for the background update to finish before starting Caelis again",
	})
	wantDegraded := "Caelis v1.2.0 is prepared (current v1.0.0 via npm). Installation will finish after this process exits. The npm launcher handoff is unavailable; wait for the background update to finish before starting Caelis again."
	if degraded != wantDegraded {
		t.Fatalf("degraded result = %q, want %q", degraded, wantDegraded)
	}
}

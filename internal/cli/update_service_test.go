package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/caelis-labs/caelis/internal/updater"
)

func TestRunUpdateReportsInstalledArtifact(t *testing.T) {
	previous := runUpdateOperation
	t.Cleanup(func() { runUpdateOperation = previous })
	storeDir := t.TempDir()
	runUpdateOperation = func(context.Context, updater.Config, updater.UpdateOptions) (updater.Result, error) {
		return updater.Result{
			CurrentVersion: "v1.0.0", LatestVersion: "v1.1.0", InstallMethod: updater.MethodRaw, Updated: true,
		}, nil
	}
	var stdout bytes.Buffer
	if err := runUpdate(context.Background(), storeDir, false, &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	// The installer only replaces the artifact; the running Host is upgraded by
	// the next Caelis start, so the completion text must say so.
	want := "Caelis v1.1.0 is installed (updated from v1.0.0 via raw); it takes effect on the next start.\n"
	if stdout.String() != want {
		t.Fatalf("update output = %q, want %q", stdout.String(), want)
	}
}

func TestRunUpdatePropagatesUpdaterFailure(t *testing.T) {
	previous := runUpdateOperation
	t.Cleanup(func() { runUpdateOperation = previous })
	runUpdateOperation = func(context.Context, updater.Config, updater.UpdateOptions) (updater.Result, error) {
		return updater.Result{CurrentVersion: "v1.0.0"}, errors.New("official installer failed")
	}
	if err := runUpdate(context.Background(), t.TempDir(), false, io.Discard, io.Discard); err == nil {
		t.Fatal("runUpdate() error = nil, want installer failure")
	}
}

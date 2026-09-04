package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/internal/updater"
)

func TestRunUpdateReportsSuccessOnlyAfterUpdatedCaelisIsActivated(t *testing.T) {
	previousUpdate := runUpdateOperation
	previousActivate := activateUpdatedCaelis
	t.Cleanup(func() {
		runUpdateOperation = previousUpdate
		activateUpdatedCaelis = previousActivate
	})
	storeDir := t.TempDir()
	runUpdateOperation = func(context.Context, updater.Config, updater.UpdateOptions) (updater.Result, error) {
		return updater.Result{
			CurrentVersion: "v1.0.0", LatestVersion: "v1.1.0", InstallMethod: updater.MethodRaw, Updated: true,
		}, nil
	}
	activated := false
	activateUpdatedCaelis = func(_ context.Context, gotStoreDir string) error {
		activated = true
		if gotStoreDir != storeDir {
			t.Fatalf("activation Store = %q", gotStoreDir)
		}
		return nil
	}
	var stdout bytes.Buffer
	if err := runUpdate(context.Background(), storeDir, false, &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !activated {
		t.Fatal("updated Caelis was reported ready before activation")
	}
}

func TestRunUpdateActivationFailureDoesNotExposeServiceDetails(t *testing.T) {
	previousUpdate := runUpdateOperation
	previousActivate := activateUpdatedCaelis
	t.Cleanup(func() {
		runUpdateOperation = previousUpdate
		activateUpdatedCaelis = previousActivate
	})
	storeDir := t.TempDir()
	runUpdateOperation = func(context.Context, updater.Config, updater.UpdateOptions) (updater.Result, error) {
		return updater.Result{
			CurrentVersion: "v1.0.0", LatestVersion: "v1.1.0", InstallMethod: updater.MethodRaw, Updated: true,
		}, nil
	}
	activateUpdatedCaelis = func(context.Context, string) error {
		return errors.New("servicelifecycle: selected process did not become ready")
	}
	err := runUpdate(context.Background(), storeDir, false, io.Discard, io.Discard)
	want := "caelis startup failed [CAELIS_STARTUP_TIMEOUT]: local Control Host did not become ready before the startup deadline; details: " + filepath.Join(storeDir, "logs", localHostLogFilename)
	if err == nil || err.Error() != want {
		t.Fatalf("product update error = %v", err)
	}
	raw, readErr := os.ReadFile(filepath.Join(storeDir, "logs", localHostLogFilename))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(raw), "servicelifecycle: selected process did not become ready") {
		t.Fatalf("diagnostic log = %q", raw)
	}
}

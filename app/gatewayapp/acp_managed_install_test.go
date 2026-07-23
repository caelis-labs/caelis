package gatewayapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	controlagents "github.com/caelis-labs/caelis/control/agents"
)

func TestManagedACPInstallPublishesVerifiedTreeAndStreamsPhases(t *testing.T) {
	useFakeManagedACPInstaller(t, func(_ context.Context, req builtinACPAgentNPMInstallRequest) (builtinACPAgentNPMInstallResult, error) {
		writeCompleteManagedACPInstall(t, req.Root, req.Package)
		return builtinACPAgentNPMInstallResult{Command: []string{"npm", "install", req.InstallSpec}}, nil
	})
	base := t.TempDir()
	pkg := builtinACPAdapterPackage{Package: "@example/acp", Version: "1.2.3", Bin: "example-acp"}
	var phases []controlagents.SetupPhase
	ctx := controlagents.WithSetupProgress(context.Background(), func(progress controlagents.SetupProgress) {
		phases = append(phases, progress.Phase)
	})

	root, err := installManagedACPAdapter(ctx, base, "example", pkg)
	if err != nil {
		t.Fatalf("installManagedACPAdapter() error = %v", err)
	}
	if root != managedACPAdapterRoot(base, pkg) {
		t.Fatalf("install root = %q, want %q", root, managedACPAdapterRoot(base, pkg))
	}
	if err := validateManagedACPAdapterRoot(root, pkg); err != nil {
		t.Fatalf("published install is invalid: %v", err)
	}
	for _, want := range []controlagents.SetupPhase{
		controlagents.SetupPhaseChecking,
		controlagents.SetupPhaseInstalling,
		controlagents.SetupPhaseVerifying,
		controlagents.SetupPhaseReady,
	} {
		if !containsManagedACPSetupPhase(phases, want) {
			t.Fatalf("progress phases = %#v, want %q", phases, want)
		}
	}
	assertManagedACPStagingEmpty(t, base, pkg)
}

func TestManagedACPInstallCancellationLeavesNoPublishedOrStagedTree(t *testing.T) {
	started := make(chan struct{})
	useFakeManagedACPInstaller(t, func(ctx context.Context, req builtinACPAgentNPMInstallRequest) (builtinACPAgentNPMInstallResult, error) {
		if err := os.WriteFile(filepath.Join(req.Root, "partial"), []byte("partial"), 0o600); err != nil {
			t.Errorf("write partial install: %v", err)
		}
		close(started)
		<-ctx.Done()
		return builtinACPAgentNPMInstallResult{Command: []string{"npm", "install"}}, ctx.Err()
	})
	base := t.TempDir()
	pkg := builtinACPAdapterPackage{Package: "@example/cancel-acp", Version: "1.0.0", Bin: "cancel-acp"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := installManagedACPAdapter(ctx, base, "cancel", pkg)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("managed install did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("install error = %v, want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled managed install did not return")
	}
	if _, err := os.Stat(managedACPAdapterRoot(base, pkg)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled install published a target: %v", err)
	}
	assertManagedACPStagingEmpty(t, base, pkg)
}

func TestManagedACPInstallFailureDoesNotAffectAnotherAdapter(t *testing.T) {
	base := t.TempDir()
	working := builtinACPAdapterPackage{Package: "@example/working", Version: "1.0.0", Bin: "working-acp"}
	writeCompleteManagedACPInstall(t, managedACPAdapterRoot(base, working), working)
	failing := builtinACPAdapterPackage{Package: "@example/failing", Version: "2.0.0", Bin: "failing-acp"}
	useFakeManagedACPInstaller(t, func(_ context.Context, req builtinACPAgentNPMInstallRequest) (builtinACPAgentNPMInstallResult, error) {
		if err := os.WriteFile(filepath.Join(req.Root, "partial"), []byte("bad"), 0o600); err != nil {
			t.Errorf("write partial install: %v", err)
		}
		return builtinACPAgentNPMInstallResult{Output: "network timeout"}, errors.New("exit status 1")
	})

	_, err := installManagedACPAdapter(context.Background(), base, "failing", failing)
	if err == nil || !strings.Contains(err.Error(), "network timeout") {
		t.Fatalf("install error = %v, want bounded npm failure", err)
	}
	if err := validateManagedACPAdapterRoot(managedACPAdapterRoot(base, working), working); err != nil {
		t.Fatalf("working adapter was affected by failed sibling install: %v", err)
	}
	if _, statErr := os.Stat(managedACPAdapterRoot(base, failing)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed sibling install published a target: %v", statErr)
	}
	assertManagedACPStagingEmpty(t, base, failing)
}

func TestManagedACPInstallRejectsMissingPlatformRuntime(t *testing.T) {
	useFakeManagedACPInstaller(t, func(_ context.Context, req builtinACPAgentNPMInstallRequest) (builtinACPAgentNPMInstallResult, error) {
		writeManagedACPAdapterFiles(t, req.Root, req.Package)
		return builtinACPAgentNPMInstallResult{}, nil
	})
	base := t.TempDir()
	pkg := builtinACPAdapterPackage{Package: "@agentclientprotocol/codex-acp", Version: "1.1.2", Bin: "codex-acp"}

	_, err := installManagedACPAdapter(context.Background(), base, "codex", pkg)
	if err == nil || !strings.Contains(err.Error(), "platform runtime") {
		t.Fatalf("install error = %v, want missing platform runtime", err)
	}
	if _, statErr := os.Stat(managedACPAdapterRoot(base, pkg)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("incomplete Codex install was published: %v", statErr)
	}
	assertManagedACPStagingEmpty(t, base, pkg)
}

func TestManagedACPInstallsForDifferentAdaptersRunIndependently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	useFakeManagedACPInstaller(t, func(_ context.Context, req builtinACPAgentNPMInstallRequest) (builtinACPAgentNPMInstallResult, error) {
		started <- req.AdapterID
		<-release
		writeCompleteManagedACPInstall(t, req.Root, req.Package)
		return builtinACPAgentNPMInstallResult{}, nil
	})
	base := t.TempDir()
	packages := []builtinACPAdapterPackage{
		{Package: "@example/first", Version: "1.0.0", Bin: "first-acp"},
		{Package: "@example/second", Version: "1.0.0", Bin: "second-acp"},
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(packages))
	for i, pkg := range packages {
		wg.Add(1)
		go func(adapterID string, candidate builtinACPAdapterPackage) {
			defer wg.Done()
			_, err := installManagedACPAdapter(context.Background(), base, adapterID, candidate)
			errs <- err
		}(string(rune('a'+i)), pkg)
	}
	seen := map[string]bool{}
	for range packages {
		select {
		case adapterID := <-started:
			seen[adapterID] = true
		case <-time.After(time.Second):
			close(release)
			t.Fatal("different adapters were serialized behind one global install lock")
		}
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent managed install error = %v", err)
		}
	}
	if len(seen) != len(packages) {
		t.Fatalf("started adapters = %#v", seen)
	}
}

func TestManagedACPInstallWaitForSameAdapterIsCancelable(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	useFakeManagedACPInstaller(t, func(_ context.Context, req builtinACPAgentNPMInstallRequest) (builtinACPAgentNPMInstallResult, error) {
		close(started)
		<-release
		writeCompleteManagedACPInstall(t, req.Root, req.Package)
		return builtinACPAgentNPMInstallResult{}, nil
	})
	base := t.TempDir()
	pkg := builtinACPAdapterPackage{Package: "@example/serialized", Version: "1.0.0", Bin: "serialized-acp"}
	firstDone := make(chan error, 1)
	go func() {
		_, err := installManagedACPAdapter(context.Background(), base, "serialized", pkg)
		firstDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first managed install did not start")
	}

	ctx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		_, err := installManagedACPAdapter(ctx, base, "serialized", pkg)
		secondDone <- err
	}()
	cancel()
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting install error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("waiting install ignored cancellation")
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first managed install error = %v", err)
	}
}

func TestManagedACPInstallReusesVerifiedVersionWithoutRunningNPM(t *testing.T) {
	base := t.TempDir()
	pkg := builtinACPAdapterPackage{Package: "@example/reuse", Version: "1.0.0", Bin: "reuse-acp"}
	target := managedACPAdapterRoot(base, pkg)
	writeCompleteManagedACPInstall(t, target, pkg)
	useFakeManagedACPInstaller(t, func(context.Context, builtinACPAgentNPMInstallRequest) (builtinACPAgentNPMInstallResult, error) {
		t.Fatal("verified managed install unexpectedly ran npm")
		return builtinACPAgentNPMInstallResult{}, nil
	})

	root, err := installManagedACPAdapter(context.Background(), base, "reuse", pkg)
	if err != nil || root != target {
		t.Fatalf("installManagedACPAdapter() = %q, %v; want %q", root, err, target)
	}
}

func TestLegacyManagedACPInstallCleanupWaitsUntilRosterStopsUsingIt(t *testing.T) {
	base := t.TempDir()
	legacyBin := managedACPAgentBinPath(base, "codex-acp")
	if err := os.MkdirAll(filepath.Dir(legacyBin), 0o700); err != nil {
		t.Fatalf("MkdirAll(legacy bin) error = %v", err)
	}
	if err := os.WriteFile(legacyBin, []byte("partial"), 0o700); err != nil {
		t.Fatalf("WriteFile(legacy bin) error = %v", err)
	}
	roster := controlagents.Configuration{Connections: []controlagents.Connection{{
		ID: "codex", Launcher: controlagents.Launcher{Kind: controlagents.LaunchKindManaged, Command: legacyBin},
	}}}
	if err := cleanupLegacyManagedACPInstall(base, roster); err != nil {
		t.Fatalf("cleanupLegacyManagedACPInstall(in use) error = %v", err)
	}
	if _, err := os.Stat(legacyBin); err != nil {
		t.Fatalf("cleanup removed an in-use legacy launcher: %v", err)
	}
	stale := time.Now().Add(-managedACPStagingMaxAge - time.Hour)
	if err := os.Chtimes(filepath.Join(base, "node_modules"), stale, stale); err != nil {
		t.Fatalf("Chtimes(legacy node_modules) error = %v", err)
	}
	if err := cleanupLegacyManagedACPInstall(base, controlagents.Configuration{}); err != nil {
		t.Fatalf("cleanupLegacyManagedACPInstall(unused) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "node_modules")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unused legacy node_modules remains: %v", err)
	}
}

func useFakeManagedACPInstaller(t *testing.T, fn func(context.Context, builtinACPAgentNPMInstallRequest) (builtinACPAgentNPMInstallResult, error)) {
	t.Helper()
	previous := runBuiltinACPAgentNPMInstall
	runBuiltinACPAgentNPMInstall = fn
	t.Cleanup(func() { runBuiltinACPAgentNPMInstall = previous })
}

func writeCompleteManagedACPInstall(t *testing.T, root string, pkg builtinACPAdapterPackage) {
	t.Helper()
	writeManagedACPAdapterFiles(t, root, pkg)
	if err := validateManagedACPPlatformRuntime(root, pkg); err == nil {
		return
	}
	// Core transaction tests use synthetic packages without a platform runtime.
	// Built-in runtime validation has a dedicated negative regression above.
	if strings.HasPrefix(pkg.Package, "@agentclientprotocol/") {
		t.Fatalf("test helper requires explicit runtime files for %s", pkg.Package)
	}
}

func writeManagedACPAdapterFiles(t *testing.T, root string, pkg builtinACPAdapterPackage) {
	t.Helper()
	packageDir := filepath.Join(root, "node_modules", filepath.FromSlash(pkg.Package))
	if err := os.MkdirAll(packageDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(package) error = %v", err)
	}
	manifest := `{"name":"` + pkg.Package + `","version":"` + pkg.Version + `"}`
	if err := os.WriteFile(filepath.Join(packageDir, "package.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("WriteFile(package.json) error = %v", err)
	}
	bin := managedACPAgentBinPath(root, pkg.Bin)
	if err := os.MkdirAll(filepath.Dir(bin), 0o700); err != nil {
		t.Fatalf("MkdirAll(bin) error = %v", err)
	}
	if err := os.WriteFile(bin, []byte("test executable fixture\n"), 0o700); err != nil {
		t.Fatalf("WriteFile(bin) error = %v", err)
	}
}

func assertManagedACPStagingEmpty(t *testing.T, base string, pkg builtinACPAdapterPackage) {
	t.Helper()
	entries, err := os.ReadDir(managedACPAdapterStagingRoot(base, pkg))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadDir(staging) error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging contains incomplete installs: %#v", entries)
	}
}

func containsManagedACPSetupPhase(phases []controlagents.SetupPhase, want controlagents.SetupPhase) bool {
	for _, phase := range phases {
		if phase == want {
			return true
		}
	}
	return false
}

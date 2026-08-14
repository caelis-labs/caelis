package servicelifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/internal/version"
)

func TestReleaseSelectionIsMonotonicAndDevelopmentUsesExactBuildID(t *testing.T) {
	tests := []struct {
		name      string
		running   Identity
		candidate Identity
		want      selectionDecision
		wantErr   error
	}{
		{name: "release upgrade", running: releaseIdentity("v1.0.0", "a"), candidate: releaseIdentity("v1.1.0", "b"), want: replaceRunning},
		{name: "release downgrade", running: releaseIdentity("v1.1.0", "b"), candidate: releaseIdentity("v1.0.0", "a"), want: keepRunning},
		{name: "release same", running: releaseIdentity("v1.1.0", "b"), candidate: releaseIdentity("v1.1.0", "b"), want: keepRunning},
		{name: "release conflicting build", running: releaseIdentity("v1.1.0", "a"), candidate: releaseIdentity("v1.1.0", "b"), wantErr: errors.New("conflicting BuildIDs")},
		{name: "development changed", running: devIdentity("a"), candidate: devIdentity("b"), want: replaceRunning},
		{name: "development same", running: devIdentity("a"), candidate: devIdentity("a"), want: keepRunning},
		{name: "isolated kinds", running: releaseIdentity("v1.0.0", "a"), candidate: devIdentity("b"), wantErr: ErrBuildKindConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectCandidate(test.running, test.candidate)
			if test.wantErr != nil {
				if errors.Is(test.wantErr, ErrBuildKindConflict) {
					if !errors.Is(err, ErrBuildKindConflict) {
						t.Fatalf("selectCandidate() error = %v", err)
					}
				} else if err == nil || !strings.Contains(err.Error(), test.wantErr.Error()) {
					t.Fatalf("selectCandidate() error = %v", err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("selectCandidate() = %v, %v", got, err)
			}
		})
	}
}

func TestManagerRetainsOnlyCurrentAndPreviousStagedBuilds(t *testing.T) {
	sourceDir := t.TempDir()
	installDir := t.TempDir()
	var running *Status
	manager := Manager{
		StoreDir: t.TempDir(), InstallDir: installDir, PollInterval: time.Millisecond,
		Probe: func(context.Context) ProbeResult {
			if running == nil {
				return ProbeResult{State: ProbeMissing}
			}
			return ProbeResult{State: ProbeReady, Status: *running}
		},
		Launch: func(candidate Candidate) (LaunchedProcess, error) {
			running = &Status{Identity: candidate.Identity, InstanceID: candidate.BuildID, PID: 1}
			return testLaunchedProcess(1), nil
		},
		Shutdown: func(context.Context, Status) error {
			running = nil
			return nil
		},
	}
	for _, buildID := range []string{"a", "b", "c"} {
		executable := filepath.Join(sourceDir, buildID)
		if err := os.WriteFile(executable, []byte("executable-"+buildID), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Start(context.Background(), Candidate{Identity: devIdentity(buildID), Executable: executable}); err != nil {
			t.Fatal(err)
		}
	}
	var staged []string
	err := filepath.WalkDir(filepath.Join(installDir, "versions"), func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			staged = append(staged, path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(staged) != 2 {
		t.Fatalf("staged executables = %v, want current and previous", staged)
	}
}

func TestStageRepairsSameSizeCorruptExecutable(t *testing.T) {
	source := filepath.Join(t.TempDir(), "caelis")
	want := []byte("expected-binary")
	if err := os.WriteFile(source, want, 0o700); err != nil {
		t.Fatal(err)
	}
	manager := Manager{StoreDir: t.TempDir(), InstallDir: t.TempDir()}
	candidate := Candidate{Identity: devIdentity("build-a"), Executable: source}
	staged, err := manager.stage(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged.Executable, []byte("damaged-binary!"), 0o700); err != nil {
		t.Fatal(err)
	}
	staged, err = manager.stage(candidate)
	if err != nil {
		t.Fatal(err)
	}
	gotDigest, err := regularFileDigest(staged.Executable)
	if err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256(want)
	if gotDigest != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("staged digest = %q, want %q", gotDigest, hex.EncodeToString(wantHash[:]))
	}
}

func TestFailedReplacementPreservesSelectedCandidate(t *testing.T) {
	sourceDir := t.TempDir()
	var running *Status
	failBuild := ""
	manager := Manager{
		StoreDir: t.TempDir(), InstallDir: t.TempDir(), PollInterval: time.Millisecond,
		Probe: func(context.Context) ProbeResult {
			if running == nil {
				return ProbeResult{State: ProbeMissing}
			}
			return ProbeResult{State: ProbeReady, Status: *running}
		},
		Launch: func(candidate Candidate) (LaunchedProcess, error) {
			if candidate.BuildID == failBuild {
				return LaunchedProcess{}, errors.New("launch failed")
			}
			running = &Status{Identity: candidate.Identity, InstanceID: candidate.BuildID, PID: 1}
			return testLaunchedProcess(1), nil
		},
		Shutdown: func(context.Context, Status) error {
			running = nil
			return nil
		},
	}
	makeCandidate := func(buildID string) Candidate {
		executable := filepath.Join(sourceDir, buildID)
		if err := os.WriteFile(executable, []byte("executable-"+buildID), 0o700); err != nil {
			t.Fatal(err)
		}
		return Candidate{Identity: devIdentity(buildID), Executable: executable}
	}
	first := makeCandidate("first")
	if _, err := manager.Start(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	selectedBefore, err := manager.loadSelected()
	if err != nil {
		t.Fatal(err)
	}
	failBuild = "second"
	if _, err := manager.Start(context.Background(), makeCandidate(failBuild)); err == nil || !strings.Contains(err.Error(), "launch failed") {
		t.Fatalf("replacement error = %v", err)
	}
	selectedAfter, err := manager.loadSelected()
	if err != nil {
		t.Fatal(err)
	}
	if selectedAfter != selectedBefore {
		t.Fatalf("selected candidate changed after failed replacement: before=%#v after=%#v", selectedBefore, selectedAfter)
	}
	if running == nil || running.BuildID != first.BuildID {
		t.Fatalf("running service = %#v, want restored build %q", running, first.BuildID)
	}
}

func TestFailedStartupAbortsExactLaunchedProcess(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "caelis")
	if err := os.WriteFile(executable, []byte("test executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	aborted := false
	released := false
	manager := Manager{
		StoreDir: t.TempDir(), InstallDir: t.TempDir(), StartupTimeout: 5 * time.Millisecond, PollInterval: time.Millisecond,
		Probe: func(context.Context) ProbeResult { return ProbeResult{State: ProbeMissing} },
		Launch: func(Candidate) (LaunchedProcess, error) {
			return LaunchedProcess{
				PID:     4242,
				Abort:   func() error { aborted = true; return nil },
				Release: func() error { released = true; return nil },
			}, nil
		},
		Shutdown: func(context.Context, Status) error { return nil },
	}
	if _, err := manager.Start(context.Background(), Candidate{Identity: devIdentity("build-a"), Executable: executable}); err == nil {
		t.Fatal("Start() succeeded without readiness")
	}
	if !aborted {
		t.Fatal("exact launched process was not aborted")
	}
	if released {
		t.Fatal("failed process was released before readiness")
	}
}

func TestUnreachableServiceNeverTriggersColdStart(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "caelis")
	if err := os.WriteFile(executable, []byte("test executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	launches := 0
	manager := Manager{
		StoreDir: t.TempDir(), InstallDir: t.TempDir(),
		Probe: func(context.Context) ProbeResult {
			return ProbeResult{State: ProbeUnreachable, Err: errors.New("discovery endpoint rejected the handshake")}
		},
		Launch: func(Candidate) (LaunchedProcess, error) {
			launches++
			return testLaunchedProcess(1), nil
		},
	}
	if _, err := manager.Start(context.Background(), Candidate{Identity: devIdentity("build-a"), Executable: executable}); err == nil || !strings.Contains(err.Error(), "rejected the handshake") {
		t.Fatalf("Start() error = %v", err)
	}
	if launches != 0 {
		t.Fatalf("unreachable service triggered %d launches", launches)
	}
}

func TestSuccessfulStartupReleasesProcessOnlyAfterExactReadiness(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "caelis")
	if err := os.WriteFile(executable, []byte("test executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	candidate := Candidate{Identity: devIdentity("build-a"), Executable: executable}
	probes := 0
	released := false
	aborted := false
	manager := Manager{
		StoreDir: t.TempDir(), InstallDir: t.TempDir(), PollInterval: time.Millisecond,
		Probe: func(context.Context) ProbeResult {
			probes++
			if probes < 3 {
				return ProbeResult{State: ProbeMissing}
			}
			return ProbeResult{State: ProbeReady, Status: Status{Identity: candidate.Identity, InstanceID: "instance", PID: 1}}
		},
		Launch: func(Candidate) (LaunchedProcess, error) {
			if released {
				t.Fatal("process was released by Launch")
			}
			return LaunchedProcess{
				PID:     1,
				Abort:   func() error { aborted = true; return nil },
				Release: func() error { released = true; return nil },
			}, nil
		},
	}
	if _, err := manager.Start(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	if !released || aborted {
		t.Fatalf("startup ownership released=%v aborted=%v", released, aborted)
	}
}

func TestManagerSerializesConcurrentStartAndLaunchesOnce(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "caelis")
	if err := os.WriteFile(executable, []byte("test executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var running *Status
	launches := 0
	manager := Manager{
		StoreDir: t.TempDir(), InstallDir: t.TempDir(), PollInterval: time.Millisecond,
		Probe: func(context.Context) ProbeResult {
			mu.Lock()
			defer mu.Unlock()
			if running == nil {
				return ProbeResult{State: ProbeMissing}
			}
			return ProbeResult{State: ProbeReady, Status: *running}
		},
		Launch: func(candidate Candidate) (LaunchedProcess, error) {
			mu.Lock()
			defer mu.Unlock()
			launches++
			running = &Status{Identity: candidate.Identity, InstanceID: "instance", PID: 1}
			return testLaunchedProcess(1), nil
		},
		Shutdown: func(context.Context, Status) error { return nil },
	}
	candidate := Candidate{Identity: devIdentity("build-a"), Executable: executable}
	var wait sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := manager.Start(context.Background(), candidate)
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if launches != 1 {
		t.Fatalf("launches = %d, want 1", launches)
	}
}

func releaseIdentity(distribution string, buildID string) Identity {
	return Identity{DistributionVersion: distribution, BuildID: buildID, BuildKind: version.BuildKindRelease}
}

func devIdentity(buildID string) Identity {
	return Identity{DistributionVersion: "dev", BuildID: buildID, BuildKind: version.BuildKindDev}
}

func testLaunchedProcess(pid int) LaunchedProcess {
	return LaunchedProcess{
		PID:     pid,
		Abort:   func() error { return nil },
		Release: func() error { return nil },
	}
}

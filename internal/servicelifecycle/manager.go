// Package servicelifecycle owns local Caelis service process selection and
// serialized replacement. It does not own Control protocol or Runtime state.
package servicelifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/internal/filelock"
	"github.com/caelis-labs/caelis/internal/productpaths"
	"github.com/caelis-labs/caelis/internal/version"
	"golang.org/x/mod/semver"
)

const (
	defaultStartupTimeout  = 15 * time.Second
	defaultShutdownTimeout = 10 * time.Second
	defaultPollInterval    = 50 * time.Millisecond
)

var ErrBuildKindConflict = errors.New("servicelifecycle: development and release services require isolated Stores")

// Identity is the process-selection identity reported by one full caelis
// binary. Protocol compatibility is intentionally not part of this type.
type Identity struct {
	DistributionVersion string `json:"distribution_version"`
	BuildID             string `json:"build_id"`
	BuildKind           string `json:"build_kind"`
}

// Candidate is one full caelis executable offered to the local service.
type Candidate struct {
	Identity
	Executable string `json:"executable"`
}

// Status is the observed ready state of one local service instance.
type Status struct {
	Identity
	InstanceID string `json:"instance_id"`
	PID        int    `json:"pid"`
	Endpoint   string `json:"endpoint"`
}

// ProbeState distinguishes absence from a service that exists but cannot be
// safely attached. Only an absent service may be cold-started.
type ProbeState uint8

const (
	ProbeMissing ProbeState = iota + 1
	ProbeReady
	ProbeUnreachable
)

// ProbeResult is one explicit lifecycle observation. Err describes an
// unreachable service and is not collapsed into absence.
type ProbeResult struct {
	State  ProbeState
	Status Status
	Err    error
}

// LaunchedProcess retains ownership of the exact child process until startup
// either succeeds or fails. Abort must kill and reap that child; Release hands
// it off only after the expected service identity is ready. Exited is optional;
// when present, it reports an early child exit so readiness does not wait for
// the full startup timeout.
type LaunchedProcess struct {
	PID     int
	Abort   func() error
	Release func() error
	Exited  <-chan error
}

// PhaseEvent reports one lifecycle phase without exposing service output.
type PhaseEvent struct {
	Name     string
	Duration time.Duration
	Err      error
}

type ProbeFunc func(context.Context) ProbeResult
type LaunchFunc func(Candidate) (LaunchedProcess, error)
type ShutdownFunc func(context.Context, Status) error

// Manager serializes lifecycle decisions for one Store.
type Manager struct {
	StoreDir string
	// InstallDir overrides the platform application-data staging root. It is
	// primarily used by hermetic tests; production leaves it empty.
	InstallDir      string
	Probe           ProbeFunc
	Launch          LaunchFunc
	Shutdown        ShutdownFunc
	StartupTimeout  time.Duration
	ShutdownTimeout time.Duration
	PollInterval    time.Duration
	ObservePhase    func(PhaseEvent)
}

// Start converges the Store on the candidate according to its build kind.
// It is idempotent and is the single implicit ensure operation used by product
// Surfaces and the explicit `caelis service start` command.
func (m Manager) Start(ctx context.Context, candidate Candidate) (status Status, err error) {
	started := time.Now()
	defer func() { m.observePhase("start_total", started, err) }()
	return m.withLock(ctx, func() (Status, error) {
		return m.startLocked(ctx, candidate)
	})
}

// Restart restarts the selected service. A newer release already selected by
// another Surface is never downgraded by an older caller.
func (m Manager) Restart(ctx context.Context, candidate Candidate) (status Status, err error) {
	started := time.Now()
	defer func() { m.observePhase("restart_total", started, err) }()
	return m.withLock(ctx, func() (Status, error) {
		if err := validateCandidate(candidate); err != nil {
			return Status{}, err
		}
		probe := m.probe(ctx)
		switch probe.State {
		case ProbeMissing:
			return m.startLocked(ctx, candidate)
		case ProbeUnreachable:
			return Status{}, probeError(probe)
		}
		running := probe.Status
		previous, previousErr := m.loadSelected()
		rollback := previousCandidate(running, previous, previousErr)
		selected := candidate
		decision, err := selectCandidate(running.Identity, candidate.Identity)
		if err != nil {
			return Status{}, err
		}
		if decision == keepRunning && running.Identity != candidate.Identity {
			selected = previous
			if previousErr != nil || selected.Identity != running.Identity {
				return Status{}, errors.New("servicelifecycle: the newer selected service cannot be restarted by this older binary")
			}
		}
		selected, err = m.stage(selected)
		if err != nil {
			return Status{}, err
		}
		if err := m.shutdownAndWait(ctx, running); err != nil {
			return Status{}, err
		}
		return m.launchWithRollback(ctx, selected, rollback)
	})
}

// Stop stops the current service without selecting another binary.
func (m Manager) Stop(ctx context.Context) (status Status, err error) {
	started := time.Now()
	defer func() { m.observePhase("stop_total", started, err) }()
	return m.withLock(ctx, func() (Status, error) {
		probe := m.probe(ctx)
		if probe.State == ProbeMissing {
			return Status{}, nil
		}
		if probe.State == ProbeUnreachable {
			return Status{}, probeError(probe)
		}
		running := probe.Status
		if err := m.shutdownAndWait(ctx, running); err != nil {
			return Status{}, err
		}
		return Status{}, nil
	})
}

// Status reads the current ready service without changing selection.
func (m Manager) Status(ctx context.Context) (Status, error) {
	probe := m.probe(ctx)
	switch probe.State {
	case ProbeReady:
		return probe.Status, nil
	case ProbeMissing:
		return Status{}, os.ErrNotExist
	default:
		return Status{}, probeError(probe)
	}
}

func (m Manager) startLocked(ctx context.Context, candidate Candidate) (Status, error) {
	if err := validateCandidate(candidate); err != nil {
		return Status{}, err
	}
	probe := m.probe(ctx)
	switch probe.State {
	case ProbeReady:
		running := probe.Status
		selected, selectedErr := m.loadSelected()
		previous := previousCandidate(running, selected, selectedErr)
		decision, selectErr := selectCandidate(running.Identity, candidate.Identity)
		if selectErr != nil {
			return Status{}, selectErr
		}
		if decision == keepRunning {
			return running, nil
		}
		staged, err := m.stage(candidate)
		if err != nil {
			return Status{}, err
		}
		if err := m.shutdownAndWait(ctx, running); err != nil {
			return Status{}, err
		}
		return m.launchWithRollback(ctx, staged, previous)
	case ProbeMissing:
		staged, err := m.stage(candidate)
		if err != nil {
			return Status{}, err
		}
		return m.launchAndWait(ctx, staged)
	case ProbeUnreachable:
		return Status{}, probeError(probe)
	default:
		return Status{}, fmt.Errorf("servicelifecycle: unsupported probe state %d", probe.State)
	}
}

func (m Manager) launchAndWait(ctx context.Context, candidate Candidate) (status Status, err error) {
	started := time.Now()
	defer func() { m.observePhase("launch_ready", started, err) }()
	previous, _ := m.loadSelected()
	if m.Launch == nil {
		return Status{}, errors.New("servicelifecycle: launch function is required")
	}
	process, err := m.Launch(candidate)
	if err != nil {
		return Status{}, err
	}
	if process.PID <= 0 || process.Abort == nil || process.Release == nil {
		if process.Abort != nil {
			_ = process.Abort()
		}
		return Status{}, errors.New("servicelifecycle: launch returned an incomplete process handle")
	}
	released := false
	fail := func(startErr error) (Status, error) {
		if released {
			return Status{}, startErr
		}
		if abortErr := process.Abort(); abortErr != nil {
			return Status{}, errors.Join(startErr, fmt.Errorf("servicelifecycle: abort process %d: %w", process.PID, abortErr))
		}
		return Status{}, startErr
	}
	deadline := m.StartupTimeout
	if deadline <= 0 {
		deadline = defaultStartupTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	for {
		if exitErr, exited := launchedProcessExit(process.Exited); exited {
			return fail(processExitBeforeReadinessError(exitErr))
		}
		probe := m.probe(waitCtx)
		if probe.State == ProbeReady {
			if exitErr, exited := launchedProcessExit(process.Exited); exited {
				return fail(processExitBeforeReadinessError(exitErr))
			}
			running := probe.Status
			if running.Identity != candidate.Identity {
				return fail(fmt.Errorf("servicelifecycle: started service identity %#v does not match candidate %#v", running.Identity, candidate.Identity))
			}
			if err := process.Release(); err != nil {
				return fail(fmt.Errorf("servicelifecycle: release process %d: %w", process.PID, err))
			}
			released = true
			if err := m.writeSelected(candidate); err != nil {
				stopErr := m.shutdownAndWait(ctx, running)
				return Status{}, errors.Join(err, stopErr)
			}
			m.cleanupStaged(candidate, previous)
			return running, nil
		}
		timer := time.NewTimer(m.pollInterval())
		select {
		case <-waitCtx.Done():
			timer.Stop()
			if exitErr, exited := launchedProcessExit(process.Exited); exited {
				return fail(processExitBeforeReadinessError(exitErr))
			}
			return fail(fmt.Errorf("servicelifecycle: service did not become ready: %w", waitCtx.Err()))
		case exitErr, ok := <-process.Exited:
			timer.Stop()
			if !ok {
				exitErr = nil
			}
			return fail(processExitBeforeReadinessError(exitErr))
		case <-timer.C:
		}
	}
}

func launchedProcessExit(exited <-chan error) (error, bool) {
	if exited == nil {
		return nil, false
	}
	select {
	case err, ok := <-exited:
		if !ok {
			return nil, true
		}
		return err, true
	default:
		return nil, false
	}
}

func processExitBeforeReadinessError(err error) error {
	if err == nil {
		return errors.New("servicelifecycle: service process exited before readiness")
	}
	return fmt.Errorf("servicelifecycle: service process exited before readiness: %w", err)
}

func (m Manager) launchWithRollback(ctx context.Context, candidate Candidate, previous Candidate) (Status, error) {
	running, err := m.launchAndWait(ctx, candidate)
	if err == nil || previous.Executable == "" {
		return running, err
	}
	if _, restoreErr := m.launchAndWait(ctx, previous); restoreErr != nil {
		return Status{}, errors.Join(err, fmt.Errorf("servicelifecycle: restore previous service: %w", restoreErr))
	}
	return Status{}, fmt.Errorf("servicelifecycle: replacement failed and the previous service was restored: %w", err)
}

func previousCandidate(running Status, candidate Candidate, err error) Candidate {
	if err != nil || candidate.Identity != running.Identity {
		return Candidate{}
	}
	return candidate
}

func (m Manager) shutdownAndWait(ctx context.Context, running Status) (err error) {
	started := time.Now()
	defer func() { m.observePhase("shutdown", started, err) }()
	if m.Shutdown == nil {
		return errors.New("servicelifecycle: shutdown function is required")
	}
	if err := m.Shutdown(ctx, running); err != nil {
		return err
	}
	deadline := m.ShutdownTimeout
	if deadline <= 0 {
		deadline = defaultShutdownTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	for {
		probe := m.probe(waitCtx)
		if probe.State == ProbeMissing {
			return nil
		}
		if probe.Status.InstanceID != "" && probe.Status.InstanceID != running.InstanceID {
			return fmt.Errorf("servicelifecycle: service %q was replaced by %q during shutdown", running.InstanceID, probe.Status.InstanceID)
		}
		if err := waitPoll(waitCtx, m.pollInterval()); err != nil {
			return fmt.Errorf("servicelifecycle: service %q did not stop: %w", running.InstanceID, err)
		}
	}
}

func (m Manager) withLock(ctx context.Context, action func() (Status, error)) (Status, error) {
	if strings.TrimSpace(m.StoreDir) == "" {
		return Status{}, errors.New("servicelifecycle: Store directory is required")
	}
	lock, err := filelock.Acquire(ctx, filepath.Join(productpaths.ServiceRuntimeDir(m.StoreDir), "lifecycle.lock"))
	if err != nil {
		return Status{}, fmt.Errorf("servicelifecycle: acquire lifecycle lock: %w", err)
	}
	defer lock.Close()
	return action()
}

func (m Manager) probe(ctx context.Context) ProbeResult {
	if m.Probe == nil {
		return ProbeResult{State: ProbeUnreachable, Err: errors.New("servicelifecycle: probe function is required")}
	}
	result := m.Probe(ctx)
	switch result.State {
	case ProbeMissing:
		result.Status = Status{}
		result.Err = nil
	case ProbeReady:
		if strings.TrimSpace(result.Status.InstanceID) == "" || result.Status.Identity == (Identity{}) {
			return ProbeResult{State: ProbeUnreachable, Status: result.Status, Err: errors.New("servicelifecycle: ready probe omitted service identity")}
		}
		result.Err = nil
	case ProbeUnreachable:
		if result.Err == nil {
			result.Err = errors.New("servicelifecycle: service is unreachable")
		}
	default:
		return ProbeResult{State: ProbeUnreachable, Status: result.Status, Err: fmt.Errorf("servicelifecycle: unsupported probe state %d", result.State)}
	}
	return result
}

func probeError(result ProbeResult) error {
	if result.Err == nil {
		return errors.New("servicelifecycle: service is unreachable")
	}
	return result.Err
}

func selectCandidate(running Identity, candidate Identity) (selectionDecision, error) {
	if running.BuildKind != candidate.BuildKind {
		return 0, ErrBuildKindConflict
	}
	if candidate.BuildKind == version.BuildKindDev {
		if running.BuildID == candidate.BuildID {
			return keepRunning, nil
		}
		return replaceRunning, nil
	}
	if candidate.BuildKind != version.BuildKindRelease {
		return 0, fmt.Errorf("servicelifecycle: unsupported build kind %q", candidate.BuildKind)
	}
	runningVersion, err := canonicalReleaseVersion(running.DistributionVersion)
	if err != nil {
		return 0, err
	}
	candidateVersion, err := canonicalReleaseVersion(candidate.DistributionVersion)
	if err != nil {
		return 0, err
	}
	switch semver.Compare(candidateVersion, runningVersion) {
	case -1:
		return keepRunning, nil
	case 1:
		return replaceRunning, nil
	default:
		if running.BuildID != candidate.BuildID {
			return 0, fmt.Errorf("servicelifecycle: release %s has conflicting BuildIDs %q and %q", candidateVersion, running.BuildID, candidate.BuildID)
		}
		return keepRunning, nil
	}
}

func validateCandidate(candidate Candidate) error {
	if strings.TrimSpace(candidate.Executable) == "" {
		return errors.New("servicelifecycle: candidate executable is required")
	}
	if strings.TrimSpace(candidate.BuildID) == "" {
		return errors.New("servicelifecycle: candidate BuildID is required")
	}
	switch candidate.BuildKind {
	case version.BuildKindDev:
		return nil
	case version.BuildKindRelease:
		_, err := canonicalReleaseVersion(candidate.DistributionVersion)
		return err
	default:
		return fmt.Errorf("servicelifecycle: unsupported candidate build kind %q", candidate.BuildKind)
	}
}

func canonicalReleaseVersion(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "v") {
		value = "v" + value
	}
	if !semver.IsValid(value) {
		return "", fmt.Errorf("servicelifecycle: invalid release version %q", value)
	}
	return value, nil
}

type selectionDecision uint8

const (
	keepRunning selectionDecision = iota + 1
	replaceRunning
)

func (m Manager) stage(candidate Candidate) (staged Candidate, err error) {
	started := time.Now()
	defer func() { m.observePhase("stage", started, err) }()
	source := filepath.Clean(candidate.Executable)
	before, err := os.Lstat(source)
	if err != nil {
		return Candidate{}, fmt.Errorf("servicelifecycle: inspect candidate executable: %w", err)
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return Candidate{}, errors.New("servicelifecycle: candidate executable must be a regular non-symlink file")
	}
	input, err := os.Open(source)
	if err != nil {
		return Candidate{}, fmt.Errorf("servicelifecycle: open candidate executable: %w", err)
	}
	defer input.Close()
	after, err := input.Stat()
	if err != nil || !os.SameFile(before, after) {
		return Candidate{}, errors.New("servicelifecycle: candidate executable changed while opening")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, input); err != nil {
		return Candidate{}, fmt.Errorf("servicelifecycle: hash candidate executable: %w", err)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	installRoot, err := m.installRoot()
	if err != nil {
		return Candidate{}, err
	}
	versionDir := safePathSegment(candidate.DistributionVersion)
	if candidate.BuildKind == version.BuildKindDev {
		versionDir = version.BuildKindDev
	}
	name := "caelis"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	destination := filepath.Join(installRoot, "versions", versionDir, digest[:16], name)
	if existingDigest, err := regularFileDigest(destination); err == nil && existingDigest == digest {
		candidate.Executable = destination
		return candidate, nil
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return Candidate{}, fmt.Errorf("servicelifecycle: create candidate directory: %w", err)
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return Candidate{}, fmt.Errorf("servicelifecycle: rewind candidate executable: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".caelis-stage-*")
	if err != nil {
		return Candidate{}, fmt.Errorf("servicelifecycle: create staged executable: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o700); err != nil {
		_ = temporary.Close()
		return Candidate{}, err
	}
	if _, err := io.Copy(temporary, input); err != nil {
		_ = temporary.Close()
		return Candidate{}, fmt.Errorf("servicelifecycle: copy candidate executable: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return Candidate{}, fmt.Errorf("servicelifecycle: sync candidate executable: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return Candidate{}, fmt.Errorf("servicelifecycle: close candidate executable: %w", err)
	}
	if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Candidate{}, fmt.Errorf("servicelifecycle: replace invalid staged executable: %w", err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return Candidate{}, fmt.Errorf("servicelifecycle: publish candidate executable: %w", err)
	}
	candidate.Executable = destination
	return candidate, nil
}

func (m Manager) observePhase(name string, started time.Time, err error) {
	if m.ObservePhase == nil {
		return
	}
	m.ObservePhase(PhaseEvent{Name: name, Duration: time.Since(started), Err: err})
}

func regularFileDigest(path string) (string, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("servicelifecycle: staged executable is not a regular file")
	}
	input, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return "", errors.New("servicelifecycle: staged executable changed while opening")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, input); err != nil {
		return "", err
	}
	after, err := input.Stat()
	if err != nil || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		return "", errors.New("servicelifecycle: staged executable changed while hashing")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (m Manager) installRoot() (string, error) {
	if root := strings.TrimSpace(m.InstallDir); root != "" {
		return filepath.Clean(root), nil
	}
	return productpaths.ServiceInstallDir(m.StoreDir)
}

func (m Manager) cleanupStaged(current Candidate, previous Candidate) {
	root, err := m.installRoot()
	if err != nil {
		return
	}
	versionsRoot := filepath.Join(root, "versions")
	keep := map[string]struct{}{}
	for _, executable := range []string{current.Executable, previous.Executable} {
		executable = filepath.Clean(strings.TrimSpace(executable))
		if executable == "." || executable == "" {
			continue
		}
		if rel, relErr := filepath.Rel(versionsRoot, executable); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			keep[filepath.Dir(executable)] = struct{}{}
		}
	}
	versionDirs, err := os.ReadDir(versionsRoot)
	if err != nil {
		return
	}
	for _, versionDir := range versionDirs {
		if !versionDir.IsDir() {
			continue
		}
		versionPath := filepath.Join(versionsRoot, versionDir.Name())
		buildDirs, readErr := os.ReadDir(versionPath)
		if readErr != nil {
			continue
		}
		for _, buildDir := range buildDirs {
			if !buildDir.IsDir() {
				continue
			}
			buildPath := filepath.Join(versionPath, buildDir.Name())
			if _, ok := keep[buildPath]; !ok {
				_ = os.RemoveAll(buildPath)
			}
		}
		_ = os.Remove(versionPath)
	}
}

func (m Manager) selectedPath() string {
	return filepath.Join(productpaths.ServiceRuntimeDir(m.StoreDir), "selected.json")
}

func (m Manager) writeSelected(candidate Candidate) error {
	path := m.selectedPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(candidate)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".selected-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func (m Manager) loadSelected() (Candidate, error) {
	file, err := os.Open(m.selectedPath())
	if err != nil {
		return Candidate{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
	decoder.DisallowUnknownFields()
	var candidate Candidate
	if err := decoder.Decode(&candidate); err != nil {
		return Candidate{}, err
	}
	if err := validateCandidate(candidate); err != nil {
		return Candidate{}, err
	}
	return candidate, nil
}

func safePathSegment(value string) string {
	value = strings.TrimSpace(strings.TrimPrefix(value, "v"))
	var out strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			out.WriteRune(r)
		} else {
			out.WriteByte('_')
		}
	}
	if out.Len() == 0 {
		return "unknown"
	}
	return out.String()
}

func (m Manager) pollInterval() time.Duration {
	if m.PollInterval > 0 {
		return m.PollInterval
	}
	return defaultPollInterval
}

func waitPoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

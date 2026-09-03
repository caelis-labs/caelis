package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/internal/productpaths"
	"github.com/caelis-labs/caelis/internal/servicelifecycle"
	"github.com/caelis-labs/caelis/internal/version"
)

const (
	defaultLocalHostListen = "127.0.0.1:0"
	localHostLogFilename   = "service.log"
)

type localHostStartRequest struct {
	Executable string
	StoreDir   string
	Listen     string
	TokenFile  string
}

func openManagedProductClients(ctx context.Context, cfg gatewayapp.Config, options productClientOptions) (*productClients, error) {
	defaultTokenFile := controlserver.DefaultTokenFile(options.StoreDir)
	if options.Token != "" {
		return nil, errors.New("CAELIS_CONTROL_TOKEN requires an explicit --control-url")
	}
	if options.TokenFile != "" && filepath.Clean(options.TokenFile) != filepath.Clean(defaultTokenFile) {
		return nil, errors.New("--control-token-file requires an explicit --control-url or `caelis serve`")
	}
	options.TokenFile = defaultTokenFile
	manager, candidate, err := newLocalServiceManager(options)
	if err != nil {
		return nil, err
	}
	missingBeforeStart := inspectManagedHost(ctx, options).Probe.State == servicelifecycle.ProbeMissing
	if _, err := manager.Start(ctx, candidate); err != nil {
		managedErr := managedProductFailure(options.StoreDir, "start", err, options.SurfaceHostCause)
		if !missingBeforeStart || ctx.Err() != nil {
			return nil, managedErr
		}
		embedded, embeddedErr := openEmbeddedProductClients(cfg, options)
		if embeddedErr != nil {
			return nil, errors.Join(managedErr, fmt.Errorf("cli: embedded fallback failed: %w", embeddedErr))
		}
		embedded.ManagedFallback = true
		return embedded, nil
	}
	product, err := openManagedProductClientsFromDiscovery(ctx, options)
	if err != nil {
		return nil, managedProductFailure(options.StoreDir, "connect", err, options.SurfaceHostCause)
	}
	return product, nil
}

func openManagedProductClientsFromDiscovery(ctx context.Context, options productClientOptions) (*productClients, error) {
	remote, record, err := attachManagedHostClient(ctx, options)
	if err != nil {
		return nil, err
	}
	clients, err := httpclient.AppServerClients(remote)
	if err != nil {
		return nil, err
	}
	return &productClients{
		Clients: clients, Mode: productClientModeManaged, BaseURL: record.Endpoint,
		Workspace: gatewayapp.Config{
			AppName: record.AppName, UserID: record.PrincipalID, StoreDir: options.StoreDir,
			WorkspaceKey: options.WorkspaceKey, WorkspaceCWD: options.WorkspaceCWD,
		},
	}, nil
}

type managedHostInspection struct {
	Probe  servicelifecycle.ProbeResult
	Client *httpclient.Client
	Record controlserver.DiscoveryRecord
	Info   appserver.ServerInfo
	Token  string
}

func inspectManagedHost(ctx context.Context, options productClientOptions) managedHostInspection {
	record, tokenFile, err := loadManagedDiscovery(options.StoreDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return classifyMissingManagedHost(options.StoreDir)
		}
		return unreachableManagedHost(record, err)
	}
	if record.AppName != options.AppName || record.PrincipalID != options.UserID {
		return unreachableManagedHost(record, fmt.Errorf(
			"cli: local Control Host scope is app %q and principal %q, not app %q and principal %q; use a different --store-dir or restart the Host",
			record.AppName, record.PrincipalID, options.AppName, options.UserID,
		))
	}
	token, err := controlserver.LoadBearerToken(tokenFile)
	if err != nil {
		return unreachableManagedHost(record, err)
	}
	inspectionPolicy := appserver.CompatibilityPolicy{
		ProtocolVersions: []int{record.ProtocolVersion},
		EnvelopeVersions: []string{record.EnvelopeVersion},
		APIVersions:      []string{record.APIVersion},
	}
	remote, err := newManagedHTTPClient(record, token, options, inspectionPolicy)
	if err != nil {
		return unreachableManagedHost(record, err)
	}
	attemptCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ready, err := remote.Readiness(attemptCtx)
	if err != nil {
		if managedTransportRetryable(err) {
			return classifyStaleManagedHost(options.StoreDir, record, err)
		}
		return unreachableManagedHost(record, err)
	}
	if ready.ServerID != record.ServerID || ready.InstanceID != record.InstanceID {
		return unreachableManagedHost(record, errors.New("cli: local Control Host discovery instance does not match the ready endpoint"))
	}
	if !ready.Ready {
		return classifyStaleManagedHost(options.StoreDir, record, errors.New("cli: local Control Host is not ready"))
	}
	info, err := remote.Initialize(attemptCtx)
	if err != nil {
		return unreachableManagedHost(record, err)
	}
	if err := validateManagedServerInfo(record, info); err != nil {
		return unreachableManagedHost(record, err)
	}
	return managedHostInspection{
		Probe:  servicelifecycle.ProbeResult{State: servicelifecycle.ProbeReady, Status: managedServiceStatus(record)},
		Client: remote,
		Record: record,
		Info:   info,
		Token:  token,
	}
}

func classifyMissingManagedHost(storeDir string) managedHostInspection {
	ownership, err := acquireProductHostOwnership(storeDir)
	if err != nil {
		return unreachableManagedHost(controlserver.DiscoveryRecord{}, fmt.Errorf("cli: local Control Host owns the Store but has not published readiness: %w", err))
	}
	_ = ownership.Close()
	return managedHostInspection{Probe: servicelifecycle.ProbeResult{State: servicelifecycle.ProbeMissing}}
}

func attachManagedHostClient(ctx context.Context, options productClientOptions) (*httpclient.Client, controlserver.DiscoveryRecord, error) {
	inspection := inspectManagedHost(ctx, options)
	switch inspection.Probe.State {
	case servicelifecycle.ProbeMissing:
		return nil, controlserver.DiscoveryRecord{}, os.ErrNotExist
	case servicelifecycle.ProbeUnreachable:
		return nil, controlserver.DiscoveryRecord{}, inspection.Probe.Err
	}
	for _, capability := range appserver.RequiredManagedHostCapabilities() {
		if !slices.Contains(inspection.Record.Capabilities, capability) {
			return nil, controlserver.DiscoveryRecord{}, &managedCompatibilityError{
				cause: fmt.Errorf("discovery is missing capability %q", capability),
			}
		}
	}
	policy := appserver.CurrentCompatibility(appserver.RequiredManagedHostCapabilities()...)
	if err := policy.Accept(inspection.Info); err != nil {
		return nil, controlserver.DiscoveryRecord{}, &managedCompatibilityError{cause: err}
	}
	token := inspection.Token
	if options.ACPIngress {
		var err error
		token, err = controlserver.LoadBearerToken(controlserver.DefaultACPIngressTokenFile(options.StoreDir))
		if err != nil {
			return nil, controlserver.DiscoveryRecord{}, fmt.Errorf("cli: load ACP ingress credential: %w", err)
		}
	}
	remote, err := newManagedHTTPClient(inspection.Record, token, options, policy)
	if err != nil {
		return nil, controlserver.DiscoveryRecord{}, err
	}
	attemptCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	info, err := remote.Initialize(attemptCtx)
	if err != nil {
		return nil, controlserver.DiscoveryRecord{}, err
	}
	if err := validateManagedServerInfo(inspection.Record, info); err != nil {
		return nil, controlserver.DiscoveryRecord{}, err
	}
	return remote, inspection.Record, nil
}

func newManagedHTTPClient(
	record controlserver.DiscoveryRecord,
	token string,
	options productClientOptions,
	policy appserver.CompatibilityPolicy,
) (*httpclient.Client, error) {
	return httpclient.New(httpclient.Config{
		BaseURL: record.Endpoint, BearerToken: token, HTTPClient: options.HTTPClient, EventBuffer: 256,
		Compatibility: policy,
	})
}

func classifyStaleManagedHost(storeDir string, record controlserver.DiscoveryRecord, cause error) managedHostInspection {
	ownership, err := acquireProductHostOwnership(storeDir)
	if err != nil {
		return unreachableManagedHost(record, cause)
	}
	_ = ownership.Close()
	if err := controlserver.RemoveDiscoveryRecord(controlserver.DefaultDiscoveryFile(storeDir), record.InstanceID); err != nil && !errors.Is(err, os.ErrNotExist) {
		return unreachableManagedHost(record, errors.Join(cause, err))
	}
	return managedHostInspection{Probe: servicelifecycle.ProbeResult{State: servicelifecycle.ProbeMissing}}
}

func unreachableManagedHost(record controlserver.DiscoveryRecord, err error) managedHostInspection {
	return managedHostInspection{
		Probe: servicelifecycle.ProbeResult{
			State:  servicelifecycle.ProbeUnreachable,
			Status: managedServiceStatus(record),
			Err:    err,
		},
		Record: record,
	}
}

func managedServiceStatus(record controlserver.DiscoveryRecord) servicelifecycle.Status {
	return servicelifecycle.Status{
		Identity: servicelifecycle.Identity{
			DistributionVersion: record.DistributionVersion,
			BuildID:             record.BuildID,
			BuildKind:           record.BuildKind,
		},
		InstanceID: record.InstanceID,
		PID:        record.PID,
		Endpoint:   record.Endpoint,
	}
}

type managedCompatibilityError struct {
	cause error
}

func (e *managedCompatibilityError) Error() string {
	return "this version of Caelis needs an update before it can continue"
}

func (e *managedCompatibilityError) Unwrap() error {
	return e.cause
}

func managedProductFailure(storeDir string, phase string, err error, surfaceCause bool) error {
	if err == nil {
		return nil
	}
	recordManagedProductDiagnostic(storeDir, phase, err)
	var compatibility *managedCompatibilityError
	if errors.As(err, &compatibility) {
		return compatibility
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if cause := userFacingHostBlocker(err); cause != "" {
		return fmt.Errorf("caelis could not %s: %s", phase, cause)
	}
	if surfaceCause {
		if detail := compactUserVisibleCause(err); detail != "" {
			return fmt.Errorf("caelis could not %s: %s", phase, detail)
		}
	}
	return fmt.Errorf("caelis could not %s; try again or run `caelis doctor`", phase)
}

func userFacingHostBlocker(err error) string {
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "unsupported unreleased schema"):
		return "Memory data uses an unsupported unreleased schema; remove the prerelease Memory database and restart Caelis"
	case strings.Contains(text, "memory data directory is already owned"):
		return "Memory data directory is already owned by another process"
	case strings.Contains(text, "permission denied"), strings.Contains(text, "operation not permitted"),
		strings.Contains(text, "read-only file system"):
		return "permission denied"
	case strings.Contains(text, "no space left"):
		return "no space left on device"
	case strings.Contains(text, "address already in use"):
		return "listen address already in use"
	case strings.Contains(text, "bwrap"), strings.Contains(text, "bubblewrap"),
		strings.Contains(text, "landlock"), strings.Contains(text, "seccomp"):
		return "sandbox isolation is unavailable"
	default:
		return ""
	}
}

func managedStartupDoctorCause(err error) string {
	if err == nil {
		return "local Control Host is unavailable"
	}
	if cause := userFacingHostBlocker(err); cause != "" {
		return cause
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "context deadline exceeded"), strings.Contains(text, "did not become ready"):
		return "local Control Host did not become ready before the startup deadline"
	case strings.Contains(text, "process exited before readiness"), strings.Contains(text, "exit status"):
		return "local Control Host exited before readiness"
	default:
		return "local Control Host could not start"
	}
}

func compactUserVisibleCause(err error) string {
	return strings.Join(strings.Fields(strings.TrimSpace(err.Error())), " ")
}

func recordManagedProductDiagnostic(storeDir string, phase string, err error) {
	if err == nil {
		return
	}
	var compatibility *managedCompatibilityError
	if errors.As(err, &compatibility) && compatibility.cause != nil {
		err = compatibility.cause
	}
	logDir := productpaths.ServiceLogDir(storeDir)
	if mkdirErr := os.MkdirAll(logDir, 0o700); mkdirErr != nil {
		return
	}
	file, openErr := os.OpenFile(filepath.Join(logDir, localHostLogFilename), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if openErr != nil {
		return
	}
	defer file.Close()
	if chmodErr := file.Chmod(0o600); chmodErr != nil {
		return
	}
	_, _ = fmt.Fprintf(file, "%s phase=%s error=%q\n", time.Now().UTC().Format(time.RFC3339Nano), phase, err.Error())
}

func recordManagedProductTiming(storeDir string, event servicelifecycle.PhaseEvent) {
	if event.Name == "start_total" && event.Err == nil && event.Duration < 100*time.Millisecond {
		return
	}
	logDir := productpaths.ServiceLogDir(storeDir)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return
	}
	file, err := os.OpenFile(filepath.Join(logDir, localHostLogFilename), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return
	}
	outcome := "ok"
	if event.Err != nil {
		outcome = "failed"
	}
	_, _ = fmt.Fprintf(file, "%s phase=lifecycle.%s duration_ms=%.3f outcome=%s\n",
		time.Now().UTC().Format(time.RFC3339Nano), event.Name, float64(event.Duration)/float64(time.Millisecond), outcome)
}

func validateManagedServerInfo(record controlserver.DiscoveryRecord, info appserver.ServerInfo) error {
	if info.ServerID != appserver.ServerIdentity || info.ServerID != record.ServerID || info.InstanceID != record.InstanceID {
		return errors.New("cli: local Control Host initialization identity does not match discovery")
	}
	if info.ProtocolVersion != record.ProtocolVersion || info.EnvelopeVersion != record.EnvelopeVersion || info.APIVersion != record.APIVersion {
		return errors.New("cli: local Control Host discovery protocol metadata is stale")
	}
	if info.DistributionVersion != record.DistributionVersion || info.BuildID != record.BuildID || info.BuildKind != record.BuildKind {
		return errors.New("cli: local Control Host discovery build metadata is stale")
	}
	if !sameManagedMetadata(record.Capabilities, info.Capabilities) || !sameManagedMetadata(record.Transports, info.Transports) {
		return errors.New("cli: local Control Host discovery capability metadata is stale")
	}
	endpoint, err := url.Parse(record.Endpoint)
	if err != nil || !slices.Contains(record.Transports, endpoint.Scheme) || !slices.Contains(info.Transports, endpoint.Scheme) {
		return errors.New("cli: local Control Host transport metadata does not match discovery")
	}
	return nil
}

func sameManagedMetadata(left []string, right []string) bool {
	left = slices.Clone(left)
	right = slices.Clone(right)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

func managedTransportRetryable(err error) bool {
	var remote *httpclient.RemoteError
	if errors.As(err, &remote) {
		return false
	}
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func launchDetachedLocalHost(request localHostStartRequest) (servicelifecycle.LaunchedProcess, error) {
	executable := strings.TrimSpace(request.Executable)
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return servicelifecycle.LaunchedProcess{}, fmt.Errorf("cli: resolve executable for local Control Host: %w", err)
		}
	}
	if strings.TrimSpace(request.StoreDir) == "" {
		return servicelifecycle.LaunchedProcess{}, errors.New("cli: local Control Host store directory is required")
	}
	if err := os.MkdirAll(request.StoreDir, 0o700); err != nil {
		return servicelifecycle.LaunchedProcess{}, fmt.Errorf("cli: create local Control Host store directory: %w", err)
	}
	logDir := productpaths.ServiceLogDir(request.StoreDir)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return servicelifecycle.LaunchedProcess{}, fmt.Errorf("cli: create local service log directory: %w", err)
	}
	logPath := filepath.Join(logDir, localHostLogFilename)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return servicelifecycle.LaunchedProcess{}, fmt.Errorf("cli: open local Control Host log: %w", err)
	}
	if err := logFile.Chmod(0o600); err != nil {
		_ = logFile.Close()
		return servicelifecycle.LaunchedProcess{}, fmt.Errorf("cli: secure local Control Host log: %w", err)
	}
	logInfo, err := logFile.Stat()
	if err != nil {
		_ = logFile.Close()
		return servicelifecycle.LaunchedProcess{}, fmt.Errorf("cli: inspect local Control Host log: %w", err)
	}
	args := []string{
		"serve",
		"--store-dir", request.StoreDir,
		"--listen", firstNonEmpty(request.Listen, defaultLocalHostListen),
		"--control-token-file", request.TokenFile,
	}
	command := exec.Command(executable, args...)
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = localHostEnvironment(os.Environ())
	configureDetachedLocalHostCommand(command)
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return servicelifecycle.LaunchedProcess{}, fmt.Errorf("cli: start local Control Host: %w", err)
	}
	_ = logFile.Close()
	handle := newDetachedLocalHostProcess(command, logPath, logInfo.Size())
	return servicelifecycle.LaunchedProcess{
		PID:     command.Process.Pid,
		Abort:   handle.abort,
		Release: handle.release,
		Exited:  handle.exited,
	}, nil
}

type detachedLocalHostProcess struct {
	mu       sync.Mutex
	command  *exec.Cmd
	exited   chan error
	done     chan struct{}
	released bool
}

func newDetachedLocalHostProcess(command *exec.Cmd, logPath string, logOffset int64) *detachedLocalHostProcess {
	process := &detachedLocalHostProcess{
		command: command,
		exited:  make(chan error, 1),
		done:    make(chan struct{}),
	}
	go func() {
		waitErr := command.Wait()
		if cause := managedStartupCauseFromLog(logPath, logOffset); cause != "" {
			waitErr = errors.New(cause)
		}
		process.exited <- waitErr
		close(process.exited)
		close(process.done)
	}()
	return process
}

func (p *detachedLocalHostProcess) abort() error {
	p.mu.Lock()
	if p.released {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()
	select {
	case <-p.done:
		return nil
	default:
	}
	var killErr error
	if err := p.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		killErr = err
	}
	<-p.done
	return killErr
}

func (p *detachedLocalHostProcess) release() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.released = true
	return nil
}

func managedStartupCauseFromLog(path string, offset int64) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() <= offset {
		return ""
	}
	const maxStartupLogBytes = int64(64 << 10)
	start := offset
	if info.Size()-start > maxStartupLogBytes {
		start = info.Size() - maxStartupLogBytes
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxStartupLogBytes))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(raw), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if cause := userFacingHostBlocker(errors.New(lines[i])); cause != "" {
			return cause
		}
	}
	return ""
}

func loadManagedDiscovery(storeDir string) (controlserver.DiscoveryRecord, string, error) {
	record, err := controlserver.LoadDiscoveryRecord(controlserver.DefaultDiscoveryFile(storeDir))
	if err != nil {
		return controlserver.DiscoveryRecord{}, "", err
	}
	return record, controlserver.DefaultTokenFile(storeDir), nil
}

func newLocalServiceManager(options productClientOptions) (servicelifecycle.Manager, servicelifecycle.Candidate, error) {
	executable, err := os.Executable()
	if err != nil {
		return servicelifecycle.Manager{}, servicelifecycle.Candidate{}, fmt.Errorf("cli: resolve Caelis executable: %w", err)
	}
	build := version.BuildInfo()
	candidate := servicelifecycle.Candidate{
		Identity: servicelifecycle.Identity{
			DistributionVersion: build.Version,
			BuildID:             build.BuildID,
			BuildKind:           build.BuildKind,
		},
		Executable: executable,
	}
	manager := servicelifecycle.Manager{
		StoreDir:        options.StoreDir,
		InstallDir:      options.ServiceInstallDir,
		StartupTimeout:  options.StartupTimeout,
		ShutdownTimeout: localHostShutdownTimeout,
		PollInterval:    options.PollInterval,
		ObservePhase: func(event servicelifecycle.PhaseEvent) {
			recordManagedProductTiming(options.StoreDir, event)
		},
	}
	manager.Probe = func(ctx context.Context) servicelifecycle.ProbeResult {
		return inspectManagedHost(ctx, options).Probe
	}
	manager.Launch = func(staged servicelifecycle.Candidate) (servicelifecycle.LaunchedProcess, error) {
		request := localHostStartRequest{
			Executable: staged.Executable,
			StoreDir:   options.StoreDir,
			Listen:     firstNonEmpty(options.ListenAddress, defaultLocalHostListen),
			TokenFile:  controlserver.DefaultTokenFile(options.StoreDir),
		}
		if options.LaunchLocalService != nil {
			return options.LaunchLocalService(request)
		}
		return launchDetachedLocalHost(request)
	}
	manager.Shutdown = func(ctx context.Context, running servicelifecycle.Status) error {
		return requestManagedServiceShutdown(ctx, options, running)
	}
	return manager, candidate, nil
}

func requestManagedServiceShutdown(ctx context.Context, options productClientOptions, running servicelifecycle.Status) error {
	inspection := inspectManagedHost(ctx, options)
	if inspection.Probe.State != servicelifecycle.ProbeReady {
		if inspection.Probe.State == servicelifecycle.ProbeMissing {
			return os.ErrNotExist
		}
		return inspection.Probe.Err
	}
	remote, record := inspection.Client, inspection.Record
	if record.InstanceID != running.InstanceID {
		return fmt.Errorf("cli: Caelis service changed from %q to %q before shutdown", running.InstanceID, record.InstanceID)
	}
	acknowledged, err := remote.ShutdownHost(ctx)
	if err != nil {
		return fmt.Errorf("cli: stop Caelis service: %w", err)
	}
	if acknowledged.ServerID != record.ServerID || acknowledged.InstanceID != record.InstanceID {
		return errors.New("cli: Caelis service shutdown acknowledgement does not match discovery")
	}
	return nil
}

func localHostEnvironment(environment []string) []string {
	blocked := []string{
		"CAELIS_CONTROL_URL", "CAELIS_CONTROL_EMBEDDED", "CAELIS_CONTROL_TOKEN", "CAELIS_CONTROL_TOKEN_FILE",
		"CAELIS_CONTROL_LISTEN", "CAELIS_CONTROL_ALLOWED_HOSTS", "CAELIS_CONTROL_TLS_CERT", "CAELIS_CONTROL_TLS_KEY",
		"CAELIS_MEMORY_BINDING_REF", "CAELIS_MEMORY_SIDECAR_MANIFEST", "CAELIS_MEMORY_DATA_DIR", "CAELIS_MEMORY_DISABLED",
	}
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if !slices.Contains(blocked, name) {
			result = append(result, entry)
		}
	}
	return result
}

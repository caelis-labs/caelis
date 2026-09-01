package memoryhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/control/memorybinding"
	v1alpha1 "github.com/caelis-labs/memory/api/memory/v1alpha1"
	memorysdk "github.com/caelis-labs/memory/sdk/go/memory"
	localclient "github.com/caelis-labs/memory/sdk/go/memory/local"
	"github.com/caelis-labs/memory/sdk/go/memory/sidecar"
)

const (
	defaultStartTimeout  = 10 * time.Second
	defaultCapabilityTTL = 30 * time.Minute
	capabilityRenewalAge = time.Minute
	failureIncompatible  = "incompatible"
	failureUnavailable   = "unavailable"
)

// FailureClass returns a fixed, secret-free activation diagnostic. Detailed
// errors remain available to the caller but must not be copied into product
// diagnostic fields because they may contain host paths.
func FailureClass(err error) string {
	if err == nil {
		return ""
	}
	if permanentCompatibilityError(err) {
		return failureIncompatible
	}
	message := err.Error()
	for _, marker := range []string{
		"sidecar manifest", "native sidecar", "pinned Memory compatibility",
		"managed Memory endpoint is invalid", "attached sidecar compatibility",
	} {
		if strings.Contains(message, marker) {
			return failureIncompatible
		}
	}
	return failureUnavailable
}

// CredentialLookup resolves an opaque Control reference to issuer credential
// bytes. Implementations must not log or persist the returned value elsewhere.
type CredentialLookup func(context.Context, string) (string, error)

type capabilityIssuer func(context.Context, v1alpha1.LocalEndpoint, string, v1alpha1.CapabilityIssueRequest) (v1alpha1.RuntimeCapability, error)

// BoundClient is the narrow public-SDK data plane consumed by Caelis tools.
type BoundClient interface {
	Remember(context.Context, string, string, *time.Time) (v1alpha1.RememberResponse, error)
	Recall(context.Context, string, v1alpha1.ConsistencyToken) (v1alpha1.RecallResponse, error)
}

// Config is process-owned composition input for one managed local sidecar.
type Config struct {
	ManifestPath string
	DataDir      string
	Endpoint     memorybinding.EndpointConfig
	Credentials  CredentialLookup
	StartTimeout time.Duration
}

// Host owns one verified memoryd process and borrows no appliance storage
// authority. The only runtime path is the public local API and Go SDK.
type Host struct {
	client        *localclient.Client
	localEndpoint v1alpha1.LocalEndpoint
	endpoint      memorybinding.EndpointConfig
	credentials   CredentialLookup
	issue         capabilityIssuer
	command       *exec.Cmd
	done          chan struct{}
	ownsProcess   bool

	mu       sync.Mutex
	waitErr  error
	close    sync.Once
	closeErr error
}

// Start verifies the exact native artifact before launch, waits for durable
// readiness, and checks the running build identity against the manifest.
func Start(ctx context.Context, config Config) (*Host, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config.ManifestPath = strings.TrimSpace(config.ManifestPath)
	config.DataDir = strings.TrimSpace(config.DataDir)
	if config.ManifestPath == "" || config.DataDir == "" || config.Credentials == nil {
		return nil, fmt.Errorf("gatewayapp/memoryhost: manifest, data directory, and credential lookup are required")
	}
	manifest, err := sidecar.Load(config.ManifestPath)
	if err != nil {
		return nil, fmt.Errorf("gatewayapp/memoryhost: verify sidecar manifest: %w", err)
	}
	if err := validateManagedEndpoint(config.Endpoint); err != nil {
		return nil, err
	}
	if err := verifyPinnedManifest(manifest, config.Endpoint.Compatibility); err != nil {
		return nil, err
	}
	executable, err := manifest.VerifyNative(filepath.Dir(config.ManifestPath))
	if err != nil {
		return nil, fmt.Errorf("gatewayapp/memoryhost: verify native sidecar: %w", err)
	}
	if info, statErr := os.Lstat(config.DataDir); statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("gatewayapp/memoryhost: appliance data path must be a directory")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("gatewayapp/memoryhost: inspect appliance data path: %w", statErr)
	}

	if config.StartTimeout <= 0 {
		config.StartTimeout = defaultStartTimeout
	}
	localEndpoint := v1alpha1.DefaultLocalEndpoint(config.DataDir)
	host := &Host{
		client:        localclient.NewClientForEndpoint(localEndpoint),
		localEndpoint: localEndpoint,
		endpoint:      config.Endpoint,
		credentials:   config.Credentials,
		issue:         issueCapability,
	}
	probeCtx, cancelProbe := context.WithTimeout(ctx, min(config.StartTimeout, 250*time.Millisecond))
	err = host.probeCompatible(probeCtx, manifest)
	cancelProbe()
	if err == nil {
		return host, nil
	}
	if permanentCompatibilityError(err) {
		host.client.CloseIdleConnections()
		return nil, fmt.Errorf("gatewayapp/memoryhost: attached sidecar compatibility: %w", err)
	}

	command := exec.Command(executable, "-data-dir", config.DataDir)
	// The sidecar has an explicit executable and data path and needs no ambient
	// Host environment. In particular, provider and issuer credentials must not
	// cross this process boundary.
	command.Env = []string{}
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("gatewayapp/memoryhost: start verified sidecar: %w", err)
	}
	host.command = command
	host.done = make(chan struct{})
	host.ownsProcess = true
	go host.wait()
	readyCtx, cancel := context.WithTimeout(ctx, config.StartTimeout)
	defer cancel()
	if err := host.waitReady(readyCtx, manifest); err != nil {
		return nil, errors.Join(err, host.Close())
	}
	return host, nil
}

// Bind returns one SDK client whose source context, budget, actor, audience,
// View/Grant, and capability renewal are fixed for one Runtime activation.
func (h *Host) Bind(
	binding memorybinding.RuntimeMemoryBindingSnapshot,
	source v1alpha1.SourceContext,
	budget v1alpha1.RecallBudget,
) (BoundClient, error) {
	if h == nil || h.client == nil || h.credentials == nil {
		return nil, fmt.Errorf("gatewayapp/memoryhost: Memory host is unavailable")
	}
	if err := h.ValidateBinding(binding); err != nil {
		return nil, err
	}
	if binding.RuntimeActorRef == "" || binding.PrincipalRef == "" || binding.IssuerCredentialRef == "" ||
		binding.ViewRef == "" || binding.GrantRef == "" || binding.BindingVersion == 0 {
		return nil, fmt.Errorf("gatewayapp/memoryhost: Runtime binding is incomplete")
	}
	if err := budget.Validate(); err != nil {
		return nil, fmt.Errorf("gatewayapp/memoryhost: Recall budget is invalid: %w", err)
	}
	capabilities := &capabilitySource{
		host:        h,
		binding:     binding,
		now:         time.Now,
		byOperation: make(map[v1alpha1.Operation]v1alpha1.RuntimeCapability, 2),
	}
	return memorysdk.NewClient(h.client, capabilities, source, budget), nil
}

// ValidateBinding proves that a Runtime snapshot still names the exact
// endpoint and artifact this process verified at startup. AppConfig changes
// require a Host restart before a different endpoint can receive tools.
func (h *Host) ValidateBinding(binding memorybinding.RuntimeMemoryBindingSnapshot) error {
	if h == nil {
		return fmt.Errorf("gatewayapp/memoryhost: Memory host is unavailable")
	}
	if binding.Endpoint != h.endpoint {
		return fmt.Errorf("gatewayapp/memoryhost: Runtime binding does not match the managed Memory endpoint")
	}
	return nil
}

// LocalEndpoint returns the Host-derived transport endpoint for diagnostics
// and tests. It is never persisted in AppConfig and contains no authority.
func (h *Host) LocalEndpoint() v1alpha1.LocalEndpoint {
	if h == nil {
		return v1alpha1.LocalEndpoint{}
	}
	return h.localEndpoint
}

// Close stops the managed process after callers have drained every Runtime.
func (h *Host) Close() error {
	if h == nil {
		return nil
	}
	h.close.Do(func() {
		if h.client != nil {
			h.client.CloseIdleConnections()
		}
		if !h.ownsProcess {
			return
		}
		select {
		case <-h.done:
			h.closeErr = h.processWaitError(false)
			return
		default:
		}
		if h.command == nil || h.command.Process == nil {
			return
		}
		if err := h.command.Process.Signal(os.Interrupt); err != nil {
			_ = h.command.Process.Kill()
		}
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		select {
		case <-h.done:
			h.closeErr = h.processWaitError(true)
		case <-timer.C:
			killErr := h.command.Process.Kill()
			<-h.done
			h.closeErr = errors.Join(fmt.Errorf("gatewayapp/memoryhost: sidecar shutdown timed out"), killErr)
		}
	})
	return h.closeErr
}

func (h *Host) wait() {
	if h.command == nil {
		return
	}
	err := h.command.Wait()
	h.mu.Lock()
	h.waitErr = err
	h.mu.Unlock()
	close(h.done)
}

func (h *Host) processWaitError(stopping bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.waitErr == nil || stopping {
		return nil
	}
	return fmt.Errorf("gatewayapp/memoryhost: managed sidecar exited")
}

func (h *Host) waitReady(ctx context.Context, manifest sidecar.Manifest) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			select {
			case <-h.done:
				return fmt.Errorf("gatewayapp/memoryhost: managed sidecar exited before readiness: %w", ctx.Err())
			default:
			}
			return fmt.Errorf("gatewayapp/memoryhost: sidecar readiness: %w", ctx.Err())
		case <-h.done:
			err := waitForCompatibleOwner(ctx, func(probeCtx context.Context) error {
				return h.probeCompatible(probeCtx, manifest)
			})
			if err == nil {
				h.ownsProcess = false
				return nil
			}
			if permanentCompatibilityError(err) {
				return fmt.Errorf("gatewayapp/memoryhost: sidecar compatibility after owner race: %w", err)
			}
			return fmt.Errorf("gatewayapp/memoryhost: managed sidecar exited before readiness: %w", err)
		case <-ticker.C:
			probeCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
			err := h.probeCompatible(probeCtx, manifest)
			cancel()
			if err == nil {
				return nil
			}
			if permanentCompatibilityError(err) {
				return fmt.Errorf("gatewayapp/memoryhost: sidecar compatibility: %w", err)
			}
		}
	}
}

func waitForCompatibleOwner(ctx context.Context, probe func(context.Context) error) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		probeCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		err := probe(probeCtx)
		cancel()
		if err == nil || permanentCompatibilityError(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (h *Host) probeCompatible(ctx context.Context, manifest sidecar.Manifest) error {
	if err := h.client.Ready(ctx); err != nil {
		return err
	}
	_, err := h.client.CheckCompatibility(ctx, localclient.CompatibilityExpectation{
		ServiceVersion: manifest.ServiceVersion,
		BuildRevision:  manifest.BuildRevision,
	})
	return err
}

func permanentCompatibilityError(err error) bool {
	code, ok := v1alpha1.ErrorCodeOf(err)
	return ok && code == v1alpha1.ErrorCodeIncompatible
}

func verifyPinnedManifest(manifest sidecar.Manifest, expected memorybinding.APICompatibility) error {
	if manifest.Protocol != strings.TrimSpace(expected.Protocol) ||
		manifest.APIVersion != strings.TrimSpace(expected.APIVersion) ||
		manifest.CoreProfile != strings.TrimSpace(expected.CoreProfile) ||
		manifest.ServiceVersion != strings.TrimSpace(expected.ServiceVersion) ||
		manifest.BuildRevision != strings.ToLower(strings.TrimSpace(expected.BuildRevision)) ||
		manifest.SHA256 != strings.ToLower(strings.TrimSpace(expected.ArtifactSHA256)) {
		return fmt.Errorf("gatewayapp/memoryhost: sidecar manifest does not match pinned Memory compatibility")
	}
	return nil
}

func validateManagedEndpoint(endpoint memorybinding.EndpointConfig) error {
	if strings.TrimSpace(endpoint.ID) == "" || endpoint.Deployment != memorybinding.DeploymentModeManagedLocal ||
		strings.TrimSpace(endpoint.Endpoint) != "" {
		return fmt.Errorf("gatewayapp/memoryhost: managed Memory endpoint is invalid")
	}
	return nil
}

type capabilitySource struct {
	host        *Host
	binding     memorybinding.RuntimeMemoryBindingSnapshot
	now         func() time.Time
	mu          sync.Mutex
	byOperation map[v1alpha1.Operation]v1alpha1.RuntimeCapability
}

func (s *capabilitySource) Authorization(ctx context.Context, operation v1alpha1.Operation) (v1alpha1.CallAuthorization, error) {
	if operation != v1alpha1.OperationRemember && operation != v1alpha1.OperationRecall {
		return v1alpha1.CallAuthorization{}, fmt.Errorf("gatewayapp/memoryhost: unsupported Runtime operation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	capability := s.byOperation[operation]
	if capability.Token == "" || !capability.ExpiresAt.After(now.Add(capabilityRenewalAge)) {
		credential, err := s.host.credentials(ctx, s.binding.IssuerCredentialRef)
		if err != nil {
			return v1alpha1.CallAuthorization{}, fmt.Errorf("gatewayapp/memoryhost: resolve issuer credential: %w", err)
		}
		if s.host.issue == nil {
			return v1alpha1.CallAuthorization{}, fmt.Errorf("gatewayapp/memoryhost: capability issuer is unavailable")
		}
		capability, err = s.host.issue(ctx, s.host.localEndpoint, credential, v1alpha1.CapabilityIssueRequest{
			PrincipalRef: s.binding.PrincipalRef,
			GrantRef:     v1alpha1.GrantID(s.binding.GrantRef),
			ActorRef:     string(s.binding.RuntimeActorRef),
			Audience:     v1alpha1.Audience(s.binding.Audience),
			Operations:   []v1alpha1.Operation{operation},
			TTLSeconds:   int64(defaultCapabilityTTL / time.Second),
		})
		if err != nil {
			return v1alpha1.CallAuthorization{}, err
		}
		s.byOperation[operation] = capability
	}
	return v1alpha1.CallAuthorization{
		Capability: capability.Token,
		ActorRef:   string(s.binding.RuntimeActorRef),
		Audience:   v1alpha1.Audience(s.binding.Audience),
	}, nil
}

func issueCapability(
	ctx context.Context,
	endpoint v1alpha1.LocalEndpoint,
	credential string,
	request v1alpha1.CapabilityIssueRequest,
) (v1alpha1.RuntimeCapability, error) {
	issuer := localclient.NewIssuerClientForEndpoint(endpoint, credential)
	defer issuer.CloseIdleConnections()
	return issuer.IssueCapability(ctx, request)
}

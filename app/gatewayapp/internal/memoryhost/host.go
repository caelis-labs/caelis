// Package memoryhost binds Caelis Host authority to the embedded Memory
// appliance without exposing Memory storage or domain internals.
package memoryhost

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caelis-labs/caelis/control/memorybinding"
	v1alpha1 "github.com/caelis-labs/memory/api/memory/v1alpha1"
	"github.com/caelis-labs/memory/appliance"
	memorysdk "github.com/caelis-labs/memory/sdk/go/memory"
	"github.com/caelis-labs/memory/sdk/go/memory/stewardworker"
)

const (
	defaultCapabilityTTL = 30 * time.Minute
	capabilityRenewalAge = time.Minute
)

// CredentialLookup resolves an opaque Control reference to issuer credential
// bytes. Implementations must not log or persist the returned value elsewhere.
type CredentialLookup func(context.Context, string) (string, error)

// BoundClient is the narrow public-SDK data plane consumed by Caelis tools.
type BoundClient interface {
	Remember(context.Context, string, string, *time.Time) (v1alpha1.RememberResponse, error)
	Recall(context.Context, string, v1alpha1.ConsistencyToken) (v1alpha1.RecallResponse, error)
	GetReceiptStatus(context.Context, v1alpha1.ReceiptID) (v1alpha1.ReceiptStatus, error)
}

// Config is the complete process-owned input for the embedded Memory runtime.
type Config struct {
	DataDir     string
	Credentials CredentialLookup
}

// Host owns one in-process Memory runtime for the Caelis Host lifetime.
type Host struct {
	runtime     *appliance.Runtime
	credentials CredentialLookup
	runtimeMu   sync.RWMutex
	close       sync.Once
	closed      atomic.Bool
	closeErr    error
}

// Open synchronously opens the embedded Memory database and applies its schema
// migrations. Failure is an ordinary Host construction error.
func Open(ctx context.Context, config Config) (*Host, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	config.DataDir = strings.TrimSpace(config.DataDir)
	if config.DataDir == "" || config.Credentials == nil {
		return nil, fmt.Errorf("gatewayapp/memoryhost: data directory and credential lookup are required")
	}
	runtime, err := appliance.Open(ctx, appliance.Options{DataDir: config.DataDir})
	if err != nil {
		return nil, fmt.Errorf("gatewayapp/memoryhost: open embedded Memory: %w", err)
	}
	return &Host{runtime: runtime, credentials: config.Credentials}, nil
}

// Management returns the direct appliance owner plane used for automatic
// topology and Steward-policy maintenance.
func (h *Host) Management() appliance.Management {
	if h == nil {
		return nil
	}
	h.runtimeMu.RLock()
	defer h.runtimeMu.RUnlock()
	if h.closed.Load() {
		return nil
	}
	return h.runtime.Management()
}

// StewardWorker returns the provider-neutral direct work plane.
func (h *Host) StewardWorker() stewardworker.Worker {
	if h == nil {
		return nil
	}
	h.runtimeMu.RLock()
	defer h.runtimeMu.RUnlock()
	if h.closed.Load() {
		return nil
	}
	return h.runtime.StewardWorker()
}

// Bind returns one SDK client whose source context, budget, actor, audience,
// View/Grant, and capability renewal are fixed for one Runtime activation.
func (h *Host) Bind(
	binding memorybinding.RuntimeMemoryBindingSnapshot,
	source v1alpha1.SourceContext,
	budget v1alpha1.RecallBudget,
) (BoundClient, error) {
	if h == nil {
		return nil, fmt.Errorf("gatewayapp/memoryhost: embedded Memory is unavailable")
	}
	h.runtimeMu.RLock()
	defer h.runtimeMu.RUnlock()
	if h.closed.Load() || h.credentials == nil {
		return nil, fmt.Errorf("gatewayapp/memoryhost: embedded Memory is unavailable")
	}
	if err := validateBinding(binding); err != nil {
		return nil, err
	}
	if err := budget.Validate(); err != nil {
		return nil, fmt.Errorf("gatewayapp/memoryhost: Recall budget is invalid: %w", err)
	}
	capabilities := &capabilitySource{
		host:        h,
		binding:     binding,
		now:         time.Now,
		byOperation: make(map[v1alpha1.Operation]v1alpha1.RuntimeCapability, 3),
	}
	return memorysdk.NewClient(h.runtime.DataPlane(), capabilities, source, budget), nil
}

// ValidateBinding checks the complete logical delegation. The embedded
// runtime has no separately versioned endpoint or artifact to validate.
func (h *Host) ValidateBinding(binding memorybinding.RuntimeMemoryBindingSnapshot) error {
	if h == nil {
		return fmt.Errorf("gatewayapp/memoryhost: embedded Memory is unavailable")
	}
	h.runtimeMu.RLock()
	defer h.runtimeMu.RUnlock()
	if h.closed.Load() {
		return fmt.Errorf("gatewayapp/memoryhost: embedded Memory is unavailable")
	}
	return validateBinding(binding)
}

func validateBinding(binding memorybinding.RuntimeMemoryBindingSnapshot) error {
	if binding.BindingRef == "" || binding.RuntimeActorRef == "" || binding.PrincipalRef == "" ||
		binding.IssuerCredentialRef == "" || binding.ViewRef == "" || binding.GrantRef == "" ||
		binding.BindingVersion == 0 {
		return fmt.Errorf("gatewayapp/memoryhost: Runtime binding is incomplete")
	}
	return nil
}

// Close releases the embedded database and owner lock.
func (h *Host) Close() error {
	if h == nil {
		return nil
	}
	h.close.Do(func() {
		h.closed.Store(true)
		h.runtimeMu.Lock()
		defer h.runtimeMu.Unlock()
		if h.runtime != nil {
			h.closeErr = h.runtime.Close()
		}
	})
	return h.closeErr
}

type capabilitySource struct {
	host        *Host
	binding     memorybinding.RuntimeMemoryBindingSnapshot
	now         func() time.Time
	mu          sync.Mutex
	byOperation map[v1alpha1.Operation]v1alpha1.RuntimeCapability
}

func (s *capabilitySource) Authorization(ctx context.Context, operation v1alpha1.Operation) (v1alpha1.CallAuthorization, error) {
	if operation != v1alpha1.OperationRemember && operation != v1alpha1.OperationRecall &&
		operation != v1alpha1.OperationReceiptStatus {
		return v1alpha1.CallAuthorization{}, fmt.Errorf("gatewayapp/memoryhost: unsupported Runtime operation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.host == nil || s.host.closed.Load() {
		return v1alpha1.CallAuthorization{}, fmt.Errorf("gatewayapp/memoryhost: embedded Memory is unavailable")
	}
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
		capability, err = s.host.issueCapability(ctx, credential, v1alpha1.CapabilityIssueRequest{
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

func (h *Host) issueCapability(
	ctx context.Context,
	credential string,
	request v1alpha1.CapabilityIssueRequest,
) (v1alpha1.RuntimeCapability, error) {
	if h == nil {
		return v1alpha1.RuntimeCapability{}, fmt.Errorf("gatewayapp/memoryhost: embedded Memory is unavailable")
	}
	h.runtimeMu.RLock()
	defer h.runtimeMu.RUnlock()
	if h.closed.Load() {
		return v1alpha1.RuntimeCapability{}, fmt.Errorf("gatewayapp/memoryhost: embedded Memory is unavailable")
	}
	return h.runtime.IssueCapability(ctx, credential, request)
}

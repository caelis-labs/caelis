package kernel

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/ports/plugin"
)

type Config struct {
	Sessions session.Service
	Runtime  agent.Runtime
	// TurnStartGate is a Control-owned readiness barrier checked before any
	// main or participant Turn can mutate runtime state.
	TurnStartGate TurnStartGate
	// Control is injected by the product host; Gateway does not infer
	// orchestration authority from the execution Runtime.
	Control       agent.SessionControlPlane
	Resolver      TurnResolver
	RequestPolicy RequestPolicy
	// ExecutionValidator is injected by Control to validate the final assembled
	// request after surface defaults are applied and before Runtime starts.
	ExecutionValidator  ExecutionRequirementsValidator
	DefaultApprovalMode ApprovalMode
	ApprovalApprover    approval.Approver
	ApprovalReviewer    ApprovalReviewer
	// SubmissionReferences projects surface shorthand such as $skill or #file
	// before a turn reaches the model/runtime boundary.
	SubmissionReferences SubmissionReferenceProjector
	Clock                func() time.Time
	SessionStartHooks    []plugin.HookSpec
}

type Gateway struct {
	sessions             session.Service
	runtime              agent.Runtime
	turnStartGate        TurnStartGate
	control              agent.SessionControlPlane
	resolver             TurnResolver
	request              RequestPolicy
	executionValidator   ExecutionRequirementsValidator
	defaultApprovalMode  ApprovalMode
	approvalApprover     approval.Approver
	approvalReviewer     ApprovalReviewer
	submissionReferences SubmissionReferenceProjector
	clock                func() time.Time
	sessionStartHooks    []plugin.HookSpec

	mu        sync.Mutex
	active    map[string]*turnHandle
	approvals map[string]*approvalCoordinator
	bindings  map[string]sessionBinding
	nextID    atomic.Uint64
}

// TurnStartGate blocks Turn creation until Control startup invariants hold.
type TurnStartGate interface {
	Wait(context.Context) error
}

// ExecutionRequirementsValidator checks a fully assembled local invocation
// after surface request defaults have been applied.
type ExecutionRequirementsValidator interface {
	ValidateExecutionRequest(agent.RunRequest) error
}

type sessionBinding struct {
	current         session.SessionRef
	surface         string
	actorKind       string
	actorID         string
	owner           string
	boundAt         time.Time
	updatedAt       time.Time
	expiresAt       time.Time
	lastHandleID    string
	lastRunID       string
	lastTurnID      string
	lastEventCursor string
}

func New(cfg Config) (*Gateway, error) {
	if cfg.Sessions == nil {
		return nil, fmt.Errorf("gateway: sessions service is required")
	}
	if cfg.Runtime == nil {
		return nil, fmt.Errorf("gateway: runtime is required")
	}
	if cfg.Resolver == nil {
		return nil, fmt.Errorf("gateway: turn resolver is required")
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.RequestPolicy == nil {
		cfg.RequestPolicy = defaultRequestPolicy{}
	}
	if cfg.ApprovalApprover == nil {
		if cfg.ApprovalReviewer != nil {
			cfg.ApprovalApprover = approval.ReviewerAdapter{Reviewer: cfg.ApprovalReviewer}
		} else {
			cfg.ApprovalApprover = denyingApprovalApprover{}
		}
	}
	if cfg.ApprovalReviewer == nil {
		cfg.ApprovalReviewer = approval.ApproverAdapter{Approver: cfg.ApprovalApprover}
	}
	return &Gateway{
		sessions:             cfg.Sessions,
		runtime:              cfg.Runtime,
		turnStartGate:        cfg.TurnStartGate,
		control:              cfg.Control,
		resolver:             cfg.Resolver,
		request:              cfg.RequestPolicy,
		executionValidator:   cfg.ExecutionValidator,
		defaultApprovalMode:  NormalizeApprovalMode(string(cfg.DefaultApprovalMode)),
		approvalApprover:     cfg.ApprovalApprover,
		approvalReviewer:     cfg.ApprovalReviewer,
		submissionReferences: cfg.SubmissionReferences,
		clock:                cfg.Clock,
		sessionStartHooks:    cfg.SessionStartHooks,
		active:               map[string]*turnHandle{},
		approvals:            map[string]*approvalCoordinator{},
		bindings:             map[string]sessionBinding{},
	}, nil
}

func (g *Gateway) waitForTurnStart(ctx context.Context) error {
	if g == nil || g.turnStartGate == nil {
		return nil
	}
	return g.turnStartGate.Wait(ctx)
}

func (g *Gateway) sessionApprovals(ref session.SessionRef) *approvalCoordinator {
	if g == nil {
		return nil
	}
	ref = session.NormalizeSessionRef(ref)
	g.mu.Lock()
	defer g.mu.Unlock()
	coordinator := g.approvals[ref.SessionID]
	if coordinator == nil {
		coordinator = newApprovalCoordinator(ref)
		g.approvals[ref.SessionID] = coordinator
	}
	return coordinator
}

func (g *Gateway) Streams() stream.Service {
	if g == nil || g.runtime == nil {
		return nil
	}
	provider, ok := g.runtime.(agent.StreamProvider)
	if !ok {
		return nil
	}
	return provider.Streams()
}

// Resolver returns the underlying *AssemblyResolver if the gateway's
// TurnResolver is one. Returns nil otherwise.
func (g *Gateway) Resolver() *AssemblyResolver {
	if g == nil {
		return nil
	}
	r, _ := g.resolver.(*AssemblyResolver)
	return r
}

// ApprovalReviewer returns the reviewer configured for automatic approval
// decisions so non-gateway surfaces can reuse the same policy bridge.
func (g *Gateway) ApprovalReviewer() ApprovalReviewer {
	if g == nil {
		return nil
	}
	return g.approvalReviewer
}

func (g *Gateway) Interrupt(ctx context.Context, req InterruptRequest) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ref, err := g.interruptTarget(req)
	if err != nil {
		return err
	}
	g.mu.Lock()
	handle, ok := g.active[ref.SessionID]
	if ok && handle != nil && !interruptMatchesHandle(req, handle) {
		g.mu.Unlock()
		return &Error{
			Kind: KindConflict, Code: CodeActiveRunConflict, UserVisible: true,
			Message: "gateway: active run does not match interrupt target",
		}
	}
	g.mu.Unlock()
	if !ok || handle == nil {
		return &Error{
			Kind:        KindConflict,
			Code:        CodeNoActiveRun,
			UserVisible: true,
			Message:     "gateway: session has no active run",
		}
	}
	if !handle.Cancel().Cancelled() {
		return &Error{
			Kind:        KindConflict,
			Code:        CodeNoActiveRun,
			UserVisible: true,
			Message:     "gateway: session has no active run",
		}
	}
	return nil
}

func interruptMatchesHandle(req InterruptRequest, handle *turnHandle) bool {
	if handle == nil {
		return false
	}
	if value := strings.TrimSpace(req.HandleID); value != "" && value != handle.HandleID() {
		return false
	}
	if value := strings.TrimSpace(req.RunID); value != "" && value != handle.RunID() {
		return false
	}
	if value := strings.TrimSpace(req.TurnID); value != "" && value != handle.TurnID() {
		return false
	}
	if req.Kind != "" && req.Kind != handle.ActiveKind() {
		return false
	}
	if value := strings.TrimSpace(req.ParticipantID); value != "" && value != handle.ParticipantID() {
		return false
	}
	return true
}

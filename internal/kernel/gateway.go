package kernel

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/approval"
	"github.com/OnslaughtSnail/caelis/ports/plugin"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
	"github.com/OnslaughtSnail/caelis/ports/task"
)

type Config struct {
	Sessions session.Service
	// Tasks is optional for basic replay. When present, resume can restore
	// completed asynchronous RUN_COMMAND/SPAWN output into the original tool
	// panel when the durable session stream only contains the running update.
	Tasks               task.Store
	Runtime             agent.Runtime
	Resolver            TurnResolver
	RequestPolicy       RequestPolicy
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
	tasks                task.Store
	runtime              agent.Runtime
	control              agent.SessionControlPlane
	resolver             TurnResolver
	request              RequestPolicy
	defaultApprovalMode  ApprovalMode
	approvalApprover     approval.Approver
	approvalReviewer     ApprovalReviewer
	submissionReferences SubmissionReferenceProjector
	clock                func() time.Time
	sessionStartHooks    []plugin.HookSpec

	mu       sync.Mutex
	active   map[string]*turnHandle
	bindings map[string]sessionBinding
	nextID   atomic.Uint64
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
		tasks:                cfg.Tasks,
		runtime:              cfg.Runtime,
		control:              resolveControlPlane(cfg.Runtime),
		resolver:             cfg.Resolver,
		request:              cfg.RequestPolicy,
		defaultApprovalMode:  NormalizeApprovalMode(string(cfg.DefaultApprovalMode)),
		approvalApprover:     cfg.ApprovalApprover,
		approvalReviewer:     cfg.ApprovalReviewer,
		submissionReferences: cfg.SubmissionReferences,
		clock:                cfg.Clock,
		sessionStartHooks:    cfg.SessionStartHooks,
		active:               map[string]*turnHandle{},
		bindings:             map[string]sessionBinding{},
	}, nil
}

func resolveControlPlane(runtime agent.Runtime) agent.SessionControlPlane {
	if control, ok := runtime.(agent.SessionControlPlane); ok {
		return control
	}
	return nil
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

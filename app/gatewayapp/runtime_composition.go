package gatewayapp

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig/codexauth"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
	"github.com/caelis-labs/caelis/control/modelconfig/grokauth"
	"github.com/caelis-labs/caelis/control/modelconfig/providerusage"
	acpassembly "github.com/caelis-labs/caelis/internal/acpagentbridge/assembly"
	"github.com/caelis-labs/caelis/internal/controlplane"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
)

// runtimeComposition groups the mutable execution snapshot and the resources
// built from it. Stack embeds one for the process root, while each detached
// Session Runtime instance receives a distinct value. Keeping the composition
// private prevents it from becoming a second product API.
type runtimeComposition struct {
	// These references are borrowed from the Host. The composition uses them to
	// execute one fixed Runtime snapshot but does not own their process
	// lifecycle or construct replacement authorities.
	Sessions                  session.Service
	AppName                   string
	UserID                    string
	Workspace                 session.WorkspaceRef
	store                     *appConfigStore
	storeDir                  string
	leaseOwnerID              string
	taskStore                 task.Store
	controlFeeds              appserver.FeedRegistry
	approvalRecovery          *appserver.ApprovalRecoveryGate
	codexAuth                 *codexauth.Manager
	grokAuth                  *grokauth.Manager
	apiKeyCredentials         *credentialstore.Store
	providerUsage             *providerusage.Registry
	sessionModelPins          *sessionModelPinRegistry
	spawnedSessionPinsMu      sync.Mutex
	spawnedSessionPinReleases map[string]func()

	lookup *modelLookup
	// modelCatalog is set only on a detached Session Runtime. It points at the
	// live Host catalog used to validate explicit Session model selections;
	// lookup remains the Runtime-owned pinned resolver used by Turns.
	modelCatalog             *modelLookup
	mu                       sync.RWMutex
	workspaceCloseMu         sync.Mutex
	placementCacheMu         sync.RWMutex
	placementCache           *placementSnapshot
	placementCacheGeneration uint64
	// appConfigSnapshot is set only on a detached Session Runtime. It keeps
	// plugin and Agent assembly on the same immutable AppConfig document used to
	// resolve that activation's placement snapshot.
	appConfigSnapshot *AppConfig
	runtime           stackRuntimeConfig
	sandbox           SandboxConfig
	// sandboxActivationPinned marks a detached Session Runtime whose sandbox
	// field is already the authoritative configuration for this activation.
	// Host roots instead compare their startup Runtime with the latest canonical
	// policy before projecting live execution status.
	sandboxActivationPinned bool
	// sandboxPersisted and sandboxRevision are populated only for the Host root
	// so status can report the canonical policy. Detached Runtime compositions
	// remain bound to their fixed sandbox snapshot in sandbox.
	sandboxOverride       SandboxConfig
	sandboxPersisted      SandboxConfig
	sandboxRevision       uint64
	exec                  sandbox.Runtime
	engine                *runtime.Runtime
	placement             controlplane.PlacementExecutor
	acpControlPlane       *acpassembly.ControlPlane
	lifecycleCtx          context.Context
	closing               atomic.Bool
	gateway               *kernelimpl.Gateway
	mcpMgr                *mcp.Manager
	pluginCacheRelease    func() error
	childControlURL       string
	childControlTokenFile string
	retainRuntimeWork     func(session.SessionRef) func()
	runtimeTaskChanged    func(session.SessionRef)
	// hostedChildMailbox is the Host-owned parent/sibling route borrowed by
	// spawned child Session Runtimes. A composition never owns the Registry.
	hostedChildMailbox hostedChildMailboxFunc
}

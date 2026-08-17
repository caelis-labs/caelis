package gatewayapp

import (
	"sync"
	"sync/atomic"

	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	acpassembly "github.com/caelis-labs/caelis/internal/acpagentbridge/assembly"
	"github.com/caelis-labs/caelis/internal/controlplane"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
)

// runtimeComposition groups the mutable execution snapshot and the resources
// built from it. Stack owns one as a named field for the process root, while
// each detached Session Runtime instance receives a distinct value. Keeping
// the composition private prevents it from becoming a second product API.
type runtimeComposition struct {
	// authorities are borrowed process services. The value contains references
	// only; one Runtime composition never owns or replaces their lifecycle.
	authorities runtimeHostAuthorities
	sessions    session.Service
	workspace   session.WorkspaceRef

	// processConfig is present only on the Host root. Detached Session Runtime
	// instances receive a pinned snapshot and never retain this mutable source.
	processConfig *runtimeProcessConfigSource

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
	// activeRuntime is the execution artifact installed with gateway/engine.
	// Host process mutations publish only to processConfig; detached values keep
	// this activation-pinned snapshot for their complete lifetime.
	activeRuntime stackRuntimeConfig
	sandbox       SandboxConfig
	// sandboxActivationPinned marks a detached Session Runtime whose sandbox
	// field is already the authoritative configuration for this activation.
	// Host roots instead compare their startup Runtime with the latest canonical
	// policy before projecting live execution status.
	sandboxActivationPinned bool
	// sandboxPersisted and sandboxRevision are populated only for the Host root
	// so status can report the canonical policy. Detached Runtime compositions
	// remain bound to their fixed sandbox snapshot in sandbox.
	sandboxPersisted   SandboxConfig
	sandboxRevision    uint64
	exec               sandbox.Runtime
	engine             *runtime.Runtime
	placement          controlplane.PlacementExecutor
	acpControlPlane    *acpassembly.ControlPlane
	closing            atomic.Bool
	gateway            *kernelimpl.Gateway
	mcpMgr             *mcp.Manager
	pluginCacheRelease func() error
	// Pinned child-control values are populated only on detached Runtimes. The
	// Host root reads the current endpoint from processConfig.
	pinnedChildControlURL       string
	pinnedChildControlTokenFile string
	retainRuntimeWork           func(session.SessionRef) func()
	runtimeTaskChanged          func(session.SessionRef)
}

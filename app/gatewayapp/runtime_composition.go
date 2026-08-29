package gatewayapp

import (
	"sync"
	"sync/atomic"

	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
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

	// process is present only on the Host root. Detached Session Runtime
	// instances receive activation instead and never retain mutable process
	// configuration.
	process *runtimeProcessState
	// activation is present only on a detached Session Runtime. It owns the
	// immutable App/model/child-control selections sampled for that activation.
	activation *sessionRuntimeActivation

	spawnedSessionPinsMu      sync.Mutex
	spawnedSessionPinReleases map[string]func()

	lookup                   *modelLookup
	mu                       sync.RWMutex
	workspaceCloseMu         sync.Mutex
	placementCacheMu         sync.RWMutex
	placementCache           *placementSnapshot
	placementCacheGeneration uint64
	// activeRuntime is the execution artifact installed with gateway/engine.
	// Host process mutations publish only to process.config; detached values
	// keep this activation-pinned snapshot for their complete lifetime except
	// that explicit provider removal revokes the deleted model for later work.
	activeRuntime      stackRuntimeConfig
	sandbox            SandboxConfig
	exec               sandbox.Runtime
	engine             *runtime.Runtime
	placement          controlplane.PlacementExecutor
	acpControlPlane    *acpassembly.ControlPlane
	closing            atomic.Bool
	gateway            *kernelimpl.Gateway
	mcpMgr             *mcp.Manager
	pluginCacheRelease func() error
	retainRuntimeWork  func(session.SessionRef) func()
	runtimeTaskChanged func(session.SessionRef)
	taskCommitted      func(*task.Entry)
}

// runtimeProcessState owns the Host root's mutable process selections and the
// latest canonical sandbox observation. It never appears on detached Session
// Runtime compositions.
type runtimeProcessState struct {
	config           *runtimeProcessConfigSource
	sandboxPersisted SandboxConfig
	sandboxRevision  uint64
}

// sessionRuntimeActivation is the selection snapshot for one detached Session
// Runtime. Ordinary Host additions and default changes do not mutate it;
// explicit provider removal revokes the deleted model from its live lookup and
// placement before later Turns or Spawn resolve work.
type sessionRuntimeActivation struct {
	modelCatalog          *modelLookup
	appConfig             *AppConfig
	childControlURL       string
	childControlTokenFile string
}

package gatewayapp

import (
	"context"
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
// built from it. Stack embeds one for the process root today, while detached
// Session Runtimes receive a distinct value. Keeping the composition private
// prevents it from becoming a second product API while the Registry migrates
// away from child Stack values.
type runtimeComposition struct {
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
	appConfigSnapshot     *AppConfig
	runtime               stackRuntimeConfig
	sandbox               SandboxConfig
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

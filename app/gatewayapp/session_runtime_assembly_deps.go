package gatewayapp

import (
	"context"
	"errors"

	"github.com/caelis-labs/caelis/agent-sdk/task"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig/codexauth"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
	"github.com/caelis-labs/caelis/control/modelconfig/grokauth"
	"github.com/caelis-labs/caelis/control/modelconfig/providerusage"
)

// sessionRuntimeAssemblyDeps are the process-scoped authorities shared with
// one detached Session Runtime plus the narrow loader for mutable process
// configuration sampled at activation. They deliberately exclude Host-only
// clients, operation stores, Runtime registries, and resource ownership.
type sessionRuntimeAssemblyDeps struct {
	appName             string
	userID              string
	store               *appConfigStore
	storeDir            string
	leaseOwnerID        string
	tasks               task.Store
	feeds               appserver.FeedRegistry
	approvalRecovery    *appserver.ApprovalRecoveryGate
	lifecycleCtx        context.Context
	codexAuth           *codexauth.Manager
	grokAuth            *grokauth.Manager
	apiKeyCredentials   *credentialstore.Store
	providerUsage       *providerusage.Registry
	modelCatalog        *modelLookup
	modelPins           *sessionModelPinRegistry
	hostedChildMailbox  hostedChildMailboxFunc
	loadProcessSnapshot func() sessionRuntimeProcessSnapshot
}

// sessionRuntimeProcessSnapshot is the process configuration sampled once for
// an activation. The assembler clones its mutable members before building the
// fixed Session Runtime.
type sessionRuntimeProcessSnapshot struct {
	runtime               stackRuntimeConfig
	sandboxOverride       SandboxConfig
	childControlURL       string
	childControlTokenFile string
}

func newSessionRuntimeAssemblyDeps(host *Stack) (sessionRuntimeAssemblyDeps, error) {
	if host == nil {
		return sessionRuntimeAssemblyDeps{}, errors.New("gatewayapp: Session Runtime assembly Host is required")
	}
	runtimeRoot := &host.composition
	mailbox := runtimeRoot.hostedChildMailbox
	if mailbox == nil {
		return sessionRuntimeAssemblyDeps{}, errors.New("gatewayapp: hosted child mailbox is required")
	}
	return sessionRuntimeAssemblyDeps{
		appName:             runtimeRoot.appName,
		userID:              runtimeRoot.userID,
		store:               runtimeRoot.store,
		storeDir:            runtimeRoot.storeDir,
		leaseOwnerID:        runtimeRoot.leaseOwnerID,
		tasks:               runtimeRoot.taskStore,
		feeds:               runtimeRoot.controlFeeds,
		approvalRecovery:    runtimeRoot.approvalRecovery,
		lifecycleCtx:        runtimeRoot.lifecycleCtx,
		codexAuth:           runtimeRoot.codexAuth,
		grokAuth:            runtimeRoot.grokAuth,
		apiKeyCredentials:   runtimeRoot.apiKeyCredentials,
		providerUsage:       runtimeRoot.providerUsage,
		modelCatalog:        runtimeRoot.lookup,
		modelPins:           runtimeRoot.sessionModelPins,
		hostedChildMailbox:  mailbox,
		loadProcessSnapshot: runtimeRoot.loadSessionRuntimeProcessSnapshot,
	}, nil
}

func (s *runtimeComposition) loadSessionRuntimeProcessSnapshot() sessionRuntimeProcessSnapshot {
	if s == nil {
		return sessionRuntimeProcessSnapshot{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sessionRuntimeProcessSnapshot{
		runtime:               cloneSessionRuntimeConfig(s.runtime),
		sandboxOverride:       cloneSandboxConfig(s.sandboxOverride),
		childControlURL:       s.childControlURL,
		childControlTokenFile: s.childControlTokenFile,
	}
}

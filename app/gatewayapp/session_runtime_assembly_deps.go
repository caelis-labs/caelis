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
	mailbox := host.hostedChildMailbox
	if mailbox == nil {
		mailbox = host.routeHostedChildMessage
	}
	return sessionRuntimeAssemblyDeps{
		appName:             host.AppName,
		userID:              host.UserID,
		store:               host.store,
		storeDir:            host.storeDir,
		leaseOwnerID:        host.leaseOwnerID,
		tasks:               host.taskStore,
		feeds:               host.controlFeeds,
		approvalRecovery:    host.approvalRecovery,
		lifecycleCtx:        host.lifecycleCtx,
		codexAuth:           host.codexAuth,
		grokAuth:            host.grokAuth,
		apiKeyCredentials:   host.apiKeyCredentials,
		providerUsage:       host.providerUsage,
		modelCatalog:        host.lookup,
		modelPins:           host.sessionModelPins,
		hostedChildMailbox:  mailbox,
		loadProcessSnapshot: host.loadSessionRuntimeProcessSnapshot,
	}, nil
}

func (s *Stack) loadSessionRuntimeProcessSnapshot() sessionRuntimeProcessSnapshot {
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

package gatewayapp

import (
	"errors"
)

// sessionRuntimeAssemblyDeps are the process-scoped authorities shared with
// one detached Session Runtime plus the independent process configuration
// source sampled at activation. They deliberately exclude Host-only clients,
// operation stores, Runtime registries, and resource ownership.
type sessionRuntimeAssemblyDeps struct {
	authorities   runtimeHostAuthorities
	modelCatalog  *modelLookup
	processConfig *runtimeProcessConfigSource
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
	if runtimeRoot.process == nil || runtimeRoot.process.config == nil {
		return sessionRuntimeAssemblyDeps{}, errors.New("gatewayapp: Session Runtime process configuration source is required")
	}
	mailbox := runtimeRoot.authorities.hostedChildMailbox
	if mailbox == nil {
		return sessionRuntimeAssemblyDeps{}, errors.New("gatewayapp: hosted child mailbox is required")
	}
	runtimeStore := newAppConfigStore(runtimeRoot.authorities.storeDir)
	if runtimeStore == nil {
		return sessionRuntimeAssemblyDeps{}, errors.New("gatewayapp: Session Runtime configuration source is required")
	}
	authorities := runtimeRoot.authorities
	// Session Runtime assembly receives an independent reader over the canonical
	// configuration path. The Host store carries write hooks, so sharing that
	// object would retain its composition through a bound callback.
	authorities.store = runtimeStore
	return sessionRuntimeAssemblyDeps{
		authorities:   authorities,
		modelCatalog:  runtimeRoot.lookup,
		processConfig: runtimeRoot.process.config,
	}, nil
}

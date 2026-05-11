package gateway

import (
	gatewaycore "github.com/OnslaughtSnail/caelis/gateway/core"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

type Config = gatewaycore.Config
type Gateway = gatewaycore.Gateway
type AssemblyResolverConfig = gatewaycore.AssemblyResolverConfig
type AssemblyResolver = gatewaycore.AssemblyResolver

func New(cfg Config) (*Gateway, error) { return gatewaycore.New(cfg) }

func NewAssemblyResolver(cfg AssemblyResolverConfig) (*AssemblyResolver, error) {
	return gatewaycore.NewAssemblyResolver(cfg)
}

func UsageSnapshotFromSessionEvent(event *sdksession.Event) *UsageSnapshot {
	return gatewaycore.UsageSnapshotFromSessionEvent(event)
}

func UsageSnapshotFromMap(payload map[string]any) *UsageSnapshot {
	return gatewaycore.UsageSnapshotFromMap(payload)
}

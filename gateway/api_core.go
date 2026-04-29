package gateway

import gatewaycore "github.com/OnslaughtSnail/caelis/gateway/core"

type Config = gatewaycore.Config
type Gateway = gatewaycore.Gateway
type AssemblyResolverConfig = gatewaycore.AssemblyResolverConfig
type AssemblyResolver = gatewaycore.AssemblyResolver

func New(cfg Config) (*Gateway, error) { return gatewaycore.New(cfg) }

func NewAssemblyResolver(cfg AssemblyResolverConfig) (*AssemblyResolver, error) {
	return gatewaycore.NewAssemblyResolver(cfg)
}

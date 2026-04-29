package gateway

import gatewayhost "github.com/OnslaughtSnail/caelis/gateway/host"

type HostMode = gatewayhost.HostMode
type HostConfig = gatewayhost.HostConfig
type Host = gatewayhost.Host
type HostStatus = gatewayhost.HostStatus
type RemoteAddress = gatewayhost.RemoteAddress
type RemoteActor = gatewayhost.RemoteActor
type RemoteSessionRequest = gatewayhost.RemoteSessionRequest
type RemoteTurnRequest = gatewayhost.RemoteTurnRequest

const (
	HostModeForeground = gatewayhost.HostModeForeground
	HostModeDaemon     = gatewayhost.HostModeDaemon
)

func NewHost(cfg HostConfig) (*Host, error) { return gatewayhost.NewHost(cfg) }

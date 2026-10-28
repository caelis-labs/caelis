package gatewayapp

import (
	"context"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver"
)

// executeMCPRefresh retries one activation's directory read. Live refresh is
// built in, so availability depends only on the active composition.
func (s *runtimeComposition) executeMCPRefresh(ctx context.Context, req appserver.RefreshMCPToolsRequest) (appserver.CommandResult, error) {
	active, err := s.checkControlCommandCAS(ctx, req.WriteBase)
	if err != nil {
		return sessionCommandResult(active), classifyControlBackendError(err)
	}
	s.mu.RLock()
	manager, refresh := s.mcpMgr, s.capabilityRefresh
	s.mu.RUnlock()
	if manager == nil && refresh == nil {
		return sessionCommandResult(active), appserver.NewOutcomeError(appserver.OutcomeRejected, errorcode.New(errorcode.Unsupported, "MCP catalog refresh is unavailable in this activation"))
	}
	if refresh != nil {
		err = refresh(ctx, req.PluginID, req.ServerName)
	} else {
		err = manager.RefreshTools(ctx, req.PluginID, req.ServerName)
	}
	return sessionCommandResult(active), classifyControlBackendError(err)
}

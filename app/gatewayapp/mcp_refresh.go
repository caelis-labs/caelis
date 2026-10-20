package gatewayapp

import (
	"context"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/experimental"
)

func (s *runtimeComposition) executeMCPRefresh(ctx context.Context, req appserver.RefreshMCPToolsRequest) (appserver.CommandResult, error) {
	active, err := s.checkControlCommandCAS(ctx, req.WriteBase)
	if err != nil {
		return sessionCommandResult(active), classifyControlBackendError(err)
	}
	s.mu.RLock()
	enabled, manager, refresh := s.activeRuntime.Experimental[experimental.MCPRefresh], s.mcpMgr, s.capabilityRefresh
	s.mu.RUnlock()
	if !enabled || (manager == nil && refresh == nil) {
		return sessionCommandResult(active), appserver.NewOutcomeError(appserver.OutcomeRejected, errorcode.New(errorcode.Unsupported, "MCP catalog refresh is unavailable in this activation"))
	}
	if refresh != nil {
		err = refresh(ctx, req.PluginID, req.ServerName)
	} else {
		err = manager.RefreshTools(ctx, req.PluginID, req.ServerName)
	}
	return sessionCommandResult(active), classifyControlBackendError(err)
}

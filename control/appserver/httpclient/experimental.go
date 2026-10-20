package httpclient

import (
	"context"
	"net/http"
	"net/url"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/experimental"
)

func (c *Client) ExperimentalFeatures(ctx context.Context, id string) (experimental.Snapshot, error) {
	return doFocusedQueryJSON[experimental.Snapshot](ctx, c, http.MethodGet, "/configuration/experimental", url.Values{"session_id": {id}}, nil)
}
func (c *Client) SetExperimentalFeatures(ctx context.Context, req appserver.ExperimentalRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/configuration/experimental", req.WriteBase, req)
}

func (c *Client) RefreshMCPTools(ctx context.Context, req appserver.RefreshMCPToolsRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/sessions/"+url.PathEscape(req.SessionID)+"/mcp/refresh", req.WriteBase, req)
}

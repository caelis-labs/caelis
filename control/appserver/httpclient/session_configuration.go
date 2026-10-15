package httpclient

import (
	"context"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	"net/http"
	"net/url"
)

func (c *Client) SessionConfiguration(ctx context.Context, sessionID string) (sessionprofile.Configuration, error) {
	return doFocusedJSON[sessionprofile.Configuration](ctx, c, http.MethodGet, "/sessions/"+url.PathEscape(sessionID)+"/configuration", nil)
}

func (c *Client) UpdateSessionConfiguration(ctx context.Context, req appserver.UpdateSessionConfigurationRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/sessions/"+url.PathEscape(req.SessionID)+"/configuration", req.WriteBase, req)
}

// Operation is receipt-only recovery. NotFound never proves an effect did not
// run; unknown outcomes must retain their original operation identity.
func (c *Client) Operation(ctx context.Context, id string) (appserver.CommandResult, error) {
	return doFocusedJSON[appserver.CommandResult](ctx, c, http.MethodGet, "/operations/"+url.PathEscape(id), nil)
}

// SessionProfile returns the original creation profile and identity evidence.
func (c *Client) SessionProfile(ctx context.Context, sessionID string) (sessionprofile.Binding, error) {
	return doFocusedJSON[sessionprofile.Binding](ctx, c, http.MethodGet, "/sessions/"+url.PathEscape(sessionID)+"/profile", nil)
}

func (c *Client) SessionReviewerState(ctx context.Context, id string) (sessionprofile.ReviewerState, error) {
	return doFocusedJSON[sessionprofile.ReviewerState](ctx, c, http.MethodGet, "/sessions/"+url.PathEscape(id)+"/reviewer-state", nil)
}
func (c *Client) SessionModelCapabilities(ctx context.Context, id string) (sessionprofile.ModelCapabilities, error) {
	return doFocusedJSON[sessionprofile.ModelCapabilities](ctx, c, http.MethodGet, "/sessions/"+url.PathEscape(id)+"/model-capabilities", nil)
}
func (c *Client) SessionMCPStatus(ctx context.Context, id string) (sessionprofile.MCPStatus, error) {
	return doFocusedJSON[sessionprofile.MCPStatus](ctx, c, http.MethodGet, "/sessions/"+url.PathEscape(id)+"/mcp-status", nil)
}

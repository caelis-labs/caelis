package httpclient

import (
	"context"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	"net/http"
	"net/url"
)

func (c *Client) CreateSessionBackgroundGrant(ctx context.Context, req appserver.CreateSessionBackgroundGrantRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/sessions/"+url.PathEscape(req.SessionID)+"/background-grants", req.WriteBase, req)
}
func (c *Client) RevokeSessionBackgroundGrant(ctx context.Context, req appserver.RevokeSessionBackgroundGrantRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/sessions/"+url.PathEscape(req.SessionID)+"/background-grants/"+url.PathEscape(req.GrantID)+"/revoke", req.WriteBase, req)
}
func (c *Client) SessionBackgroundGrant(ctx context.Context, sessionID, id string) (sessionprofile.BackgroundGrant, error) {
	return doFocusedJSON[sessionprofile.BackgroundGrant](ctx, c, http.MethodGet, "/sessions/"+url.PathEscape(sessionID)+"/background-grants/"+url.PathEscape(id), nil)
}

func (c *Client) SessionBackgroundGrants(ctx context.Context, id string) ([]sessionprofile.BackgroundGrant, error) {
	return doFocusedJSON[[]sessionprofile.BackgroundGrant](ctx, c, http.MethodGet, "/sessions/"+url.PathEscape(id)+"/background-grants", nil)
}

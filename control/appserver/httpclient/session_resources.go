package httpclient

import (
	"context"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	"net/http"
	"net/url"
)

func (c *Client) CreateSessionResource(ctx context.Context, req appserver.SessionResourceRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/sessions/"+url.PathEscape(req.SessionID)+"/resources", req.WriteBase, req)
}
func (c *Client) SessionResource(ctx context.Context, sessionID, resourceID string) (sessionprofile.Resource, error) {
	return doFocusedJSON[sessionprofile.Resource](ctx, c, http.MethodGet, "/sessions/"+url.PathEscape(sessionID)+"/resources/"+url.PathEscape(resourceID), nil)
}
func (c *Client) SessionResourceContent(ctx context.Context, sessionID, resourceID string) (appserver.SessionResourceContent, error) {
	out, err := sessionJSON[appserver.SessionResourceContent](ctx, c, http.MethodGet, "/sessions/"+url.PathEscape(sessionID)+"/resources/"+url.PathEscape(resourceID)+"/content", nil, nil, "", true)
	if err != nil {
		return out, err
	}
	return verifyResourceContent(sessionID, resourceID, out)
}

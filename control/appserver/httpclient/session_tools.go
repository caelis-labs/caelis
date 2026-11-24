package httpclient

import (
	"context"
	"net/http"
	"net/url"

	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func sessionToolCallsPath(sessionID string) string {
	return "/sessions/" + url.PathEscape(sessionID) + "/tool-calls"
}

// SessionToolCall reads a historical receipt at its original address.
func (c *Client) SessionToolCall(ctx context.Context, sessionID, id string) (sessionprofile.Call, error) {
	return doFocusedJSON[sessionprofile.Call](ctx, c, http.MethodGet, sessionToolCallsPath(sessionID)+"/"+url.PathEscape(id), nil)
}

// SessionToolCalls lists historical callback receipts for an owned Session.
func (c *Client) SessionToolCalls(ctx context.Context, sessionID string) ([]sessionprofile.Call, error) {
	return doFocusedJSON[[]sessionprofile.Call](ctx, c, http.MethodGet, sessionToolCallsPath(sessionID), nil)
}

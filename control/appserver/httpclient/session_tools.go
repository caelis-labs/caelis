package httpclient

import (
	"context"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	"net/http"
	"net/url"
)

func sessionToolCallsPath(sessionID string) string {
	return "/sessions/" + url.PathEscape(sessionID) + "/tool-calls"
}
func (c *Client) WaitSessionToolCalls(ctx context.Context, sessionID string) ([]sessionprofile.Call, error) {
	return sessionJSON[[]sessionprofile.Call](ctx, c, http.MethodGet, sessionToolCallsPath(sessionID), url.Values{"wait": {"true"}}, nil, "", false)
}
func (c *Client) SessionToolCall(ctx context.Context, sessionID, id string) (sessionprofile.Call, error) {
	return doFocusedJSON[sessionprofile.Call](ctx, c, http.MethodGet, sessionToolCallsPath(sessionID)+"/"+url.PathEscape(id), nil)
}

// ClaimSessionToolCall grants one execution only. A lost response is not permission
// to claim again or perform the effect; reconcile the original call instead.
func (c *Client) ClaimSessionToolCall(ctx context.Context, sessionID, id string) (sessionprofile.Call, error) {
	return doFocusedJSON[sessionprofile.Call](ctx, c, http.MethodPost, sessionToolCallsPath(sessionID)+"/"+url.PathEscape(id)+"/claim", struct{}{})
}
func (c *Client) CompleteSessionToolCall(ctx context.Context, sessionID, id string, result sessionprofile.CallResult) error {
	_, err := doFocusedJSON[struct{}](ctx, c, http.MethodPost, sessionToolCallsPath(sessionID)+"/"+url.PathEscape(id)+"/result", result)
	return err
}

func (c *Client) SessionToolCalls(ctx context.Context, sessionID string) ([]sessionprofile.Call, error) {
	return doFocusedJSON[[]sessionprofile.Call](ctx, c, http.MethodGet, sessionToolCallsPath(sessionID), nil)
}

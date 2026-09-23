package httpclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
)

// RegisterApplication requires a Host credential. Persist a cryptographically
// random application credential and operation ID before enrollment, then use a
// separate Client with that credential for all application operations.
func (c *Client) RegisterApplication(ctx context.Context, req application.Registration) (application.Connection, error) {
	return applicationJSON[application.Connection](ctx, c, http.MethodPost, "/applications/register", nil, req, req.OperationID, false)
}
func (c *Client) ApplicationConnection(ctx context.Context) (application.Connection, error) {
	return doFocusedJSON[application.Connection](ctx, c, http.MethodGet, "/application/connection", nil)
}
func (c *Client) RenewApplicationConnection(ctx context.Context) (application.Connection, error) {
	return doFocusedJSON[application.Connection](ctx, c, http.MethodPost, "/application/connection/renew", struct{}{})
}
func (c *Client) RevokeApplicationConnection(ctx context.Context) error {
	_, err := doFocusedJSON[struct{}](ctx, c, http.MethodPost, "/application/connection/revoke", struct{}{})
	return err
}
func (c *Client) ListApplicationSessions(ctx context.Context) ([]application.Binding, error) {
	return doFocusedJSON[[]application.Binding](ctx, c, http.MethodGet, "/application/sessions", nil)
}
func (c *Client) CreateApplicationSession(ctx context.Context, req appserver.CreateApplicationSessionRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, "/application/sessions", req.WriteBase, req)
}
func (c *Client) ApplicationSession(ctx context.Context, id string) (application.Binding, error) {
	return doFocusedJSON[application.Binding](ctx, c, http.MethodGet, applicationSessionPath(id), nil)
}
func (c *Client) PromptApplication(ctx context.Context, req appserver.ApplicationPromptRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, applicationSessionPath(req.SessionID)+"/prompt", req.WriteBase, req)
}
func (c *Client) ArchiveApplicationSession(ctx context.Context, req appserver.CloseSessionRequest) (appserver.CommandResult, error) {
	return c.doCommand(ctx, http.MethodPost, applicationSessionPath(req.SessionID)+"/archive", req.WriteBase, req)
}
func (c *Client) ApplicationOperation(ctx context.Context, id string) (appserver.ApplicationOperation, error) {
	return doFocusedJSON[appserver.ApplicationOperation](ctx, c, http.MethodGet, "/application/operations/"+url.PathEscape(id), nil)
}
func (c *Client) ApplicationCalls(ctx context.Context, sessionID string) ([]application.Call, error) {
	return doFocusedJSON[[]application.Call](ctx, c, http.MethodGet, applicationSessionPath(sessionID)+"/calls", nil)
}

// WaitApplicationCalls waits for unclaimed intents. A returned intent is not an
// effect grant: persist and claim its opaque ID before performing business work.
func (c *Client) WaitApplicationCalls(ctx context.Context, sessionID string) ([]application.Call, error) {
	return applicationJSON[[]application.Call](ctx, c, http.MethodGet, applicationSessionPath(sessionID)+"/calls", url.Values{"wait": {"true"}}, nil, "", false)
}
func (c *Client) ApplicationCall(ctx context.Context, sessionID, id string) (application.Call, error) {
	return doFocusedJSON[application.Call](ctx, c, http.MethodGet, applicationCallPath(sessionID, id), nil)
}

// ClaimApplicationCall is intentionally not retryable. A lost claim response
// leaves the effect unknown; querying a claimed call never grants another effect.
func (c *Client) ClaimApplicationCall(ctx context.Context, sessionID, id string) (application.Call, error) {
	return doFocusedJSON[application.Call](ctx, c, http.MethodPost, applicationCallPath(sessionID, id)+"/claim", struct{}{})
}
func (c *Client) CompleteApplicationCall(ctx context.Context, sessionID, id string, result application.CallResult) error {
	_, err := doFocusedJSON[struct{}](ctx, c, http.MethodPost, applicationCallPath(sessionID, id)+"/result", result)
	return err
}
func (c *Client) UploadApplicationResource(ctx context.Context, req appserver.ApplicationResourceRequest) (application.Resource, error) {
	return applicationJSON[application.Resource](ctx, c, http.MethodPost, applicationSessionPath(req.SessionID)+"/resources", nil, req, req.OperationID, false)
}
func (c *Client) ApplicationResource(ctx context.Context, sessionID, id string) (application.Resource, error) {
	return doFocusedJSON[application.Resource](ctx, c, http.MethodGet, applicationResourcePath(sessionID, id), nil)
}

// ReadApplicationResource verifies the immutable descriptor before returning bytes.
func (c *Client) ReadApplicationResource(ctx context.Context, sessionID, id string) (appserver.ApplicationResourceContent, error) {
	out, err := applicationJSON[appserver.ApplicationResourceContent](ctx, c, http.MethodGet, applicationResourcePath(sessionID, id)+"/content", nil, nil, "", true)
	if err != nil {
		return out, err
	}
	digest := sha256.Sum256(out.Data)
	if out.Resource.ID != id || out.Resource.SessionID != sessionID || out.Resource.Size != int64(len(out.Data)) || len(out.Data) > application.MaxResourceBytes || out.Resource.SHA256 != hex.EncodeToString(digest[:]) {
		return appserver.ApplicationResourceContent{}, errors.New("application resource integrity mismatch")
	}
	return out, nil
}
func applicationSessionPath(id string) string { return "/application/sessions/" + url.PathEscape(id) }
func applicationCallPath(sessionID, id string) string {
	return applicationSessionPath(sessionID) + "/calls/" + url.PathEscape(id)
}
func applicationResourcePath(sessionID, id string) string {
	return applicationSessionPath(sessionID) + "/resources/" + url.PathEscape(id)
}

func applicationJSON[T any](ctx context.Context, c *Client, method, path string, query url.Values, body any, operation string, resource bool) (T, error) {
	var out T
	headers := http.Header{}
	if operation != "" {
		headers.Set("Idempotency-Key", operation)
	}
	response, err := c.do(ctx, method, path, query, body, headers)
	if err != nil {
		return out, err
	}
	defer response.Body.Close()
	limit := int64(maxRemoteResponseBody)
	if resource {
		limit = 4*((application.MaxResourceBytes+2)/3) + 64*1024
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return out, err
	}
	if int64(len(raw)) > limit {
		return out, errors.New("application response exceeds size limit")
	}
	err = wirev1.Unmarshal(raw, &out)
	return out, err
}

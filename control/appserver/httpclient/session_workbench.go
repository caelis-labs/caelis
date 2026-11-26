package httpclient

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
)

// ListSessionWorkbench reads one cross-workspace overview page.
func (c *Client) ListSessionWorkbench(ctx context.Context, request appserver.SessionWorkbenchRequest) (appserver.SessionWorkbenchPage, error) {
	query := make(url.Values)
	if request.Cursor != "" {
		query.Set("cursor", request.Cursor)
	}
	if request.Limit > 0 {
		query.Set("limit", strconv.Itoa(request.Limit))
	}
	for _, id := range request.RefreshIDs {
		query.Add("refresh_id", id)
	}
	response, err := c.do(ctx, http.MethodGet, "/session-workbench", query, nil, nil)
	if err != nil {
		return appserver.SessionWorkbenchPage{}, err
	}
	defer response.Body.Close()
	raw, err := readRemoteResponse(response)
	if err != nil {
		return appserver.SessionWorkbenchPage{}, err
	}
	var page appserver.SessionWorkbenchPage
	if err := wirev1.Unmarshal(raw, &page); err != nil {
		return appserver.SessionWorkbenchPage{}, err
	}
	return page, nil
}

// AcknowledgeSessionRead advances the authenticated user's read position.
func (c *Client) AcknowledgeSessionRead(ctx context.Context, sessionID string, receipt appserver.SessionReadReceipt) error {
	id, err := remotePathID("session", sessionID)
	if err != nil {
		return err
	}
	response, err := c.do(ctx, http.MethodPut, "/sessions/"+id+"/read-position", nil, receipt, nil)
	if err != nil {
		return err
	}
	return response.Body.Close()
}

var _ appserver.SessionWorkbenchClient = (*Client)(nil)

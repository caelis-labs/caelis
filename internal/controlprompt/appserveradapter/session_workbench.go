package appserveradapter

import (
	"context"
	"slices"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver"
)

// ListSessionWorkbench reads the paged overview. Hosts predating the workbench
// use the existing cross-workspace directory until that peer is upgraded.
func (a *SessionClientAdapter) ListSessionWorkbench(ctx context.Context, request appserver.SessionWorkbenchRequest) (appserver.SessionWorkbenchPage, error) {
	if client, ok := a.sessionClient.(appserver.SessionWorkbenchClient); ok {
		page, err := client.ListSessionWorkbench(ctx, request)
		if code := errorcode.CodeOf(err); code != errorcode.NotFound && code != errorcode.Unsupported {
			return page, err
		}
	}
	list, err := a.sessionClient.ListSessions(ctx, appserver.ListSessionsRequest{Cursor: request.Cursor, Limit: request.Limit})
	if err != nil {
		return appserver.SessionWorkbenchPage{}, err
	}
	rows := make([]appserver.SessionWorkbenchRow, 0, len(list.Sessions))
	for _, item := range list.Sessions {
		rows = append(rows, appserver.SessionWorkbenchRow{
			SessionID: item.SessionID, WorkspaceKey: item.WorkspaceKey, CWD: item.CWD,
			Title: item.Title, UpdatedAt: item.UpdatedAt, Active: slices.Contains(list.RunningSessionIDs, item.SessionID),
		})
	}
	return appserver.SessionWorkbenchPage{Sessions: rows, NextCursor: list.NextCursor}, nil
}

// AcknowledgeSessionRead advances the user's read position if supported by the Host.
func (a *SessionClientAdapter) AcknowledgeSessionRead(ctx context.Context, id string, receipt appserver.SessionReadReceipt) error {
	if client, ok := a.sessionClient.(appserver.SessionWorkbenchClient); ok {
		err := client.AcknowledgeSessionRead(ctx, id, receipt)
		if code := errorcode.CodeOf(err); code != errorcode.NotFound && code != errorcode.Unsupported {
			return err
		}
	}
	return nil
}

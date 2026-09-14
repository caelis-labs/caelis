package appserveradapter

import (
	"context"
	"github.com/caelis-labs/caelis/control/appserver"
)

// LoadSessionHistory reads a finite older window without switching the active
// Session, replacing its observer, or changing its command target.
func (a *SessionClientAdapter) LoadSessionHistory(ctx context.Context, sessionID, before string) (appserver.FeedSubscription, error) {
	result, err := a.sessionClient.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sessionID, HistoryTurns: 16, HistoryBefore: before})
	return result.Subscription, err
}

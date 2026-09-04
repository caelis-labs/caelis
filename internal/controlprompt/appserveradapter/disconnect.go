package appserveradapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig"
)

// DeleteModels disconnects selected provider models through individual
// revision-aware Host commands. It returns confirmed deletions, including a
// committed deletion with a warning, and stops at the first error without retry.
func (a *SessionClientAdapter) DeleteModels(ctx context.Context, models []string) ([]string, error) {
	return disconnectTargets(ctx, models, a.DeleteModel)
}

// DisconnectACPAgents disconnects selected Agents through individual revision-aware
// Host commands. It returns confirmed removals and stops at the first error
// without retry, including when the last removal committed with a warning.
func (a *SessionClientAdapter) DisconnectACPAgents(ctx context.Context, agentIDs []string) ([]string, error) {
	return disconnectTargets(ctx, agentIDs, func(ctx context.Context, agentID string) error {
		_, err := a.DisconnectACP(ctx, agentID)
		return err
	})
}

func disconnectTargets(ctx context.Context, targets []string, disconnect func(context.Context, string) error) ([]string, error) {
	var completed []string
	for _, target := range modelconfig.DedupeNonEmptyStrings(targets) {
		if err := ctx.Err(); err != nil {
			return completed, fmt.Errorf("disconnect stopped before %q: %w", target, err)
		}
		err := disconnect(ctx, target)
		var receipt *appserver.CommandReceiptError
		if err == nil || (errors.As(err, &receipt) && receipt.Receipt.Outcome == appserver.OutcomeCommitted) {
			completed = append(completed, target)
		}
		if err != nil {
			return completed, fmt.Errorf("disconnect stopped at %q: %w", target, err)
		}
	}
	return completed, nil
}

package controladapter

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/internal/controlclient/turningress"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func newGatewayTurn(handle kernel.TurnHandle) *gatewayTurn {
	return &gatewayTurn{
		handle: handle,
		feed:   turningress.New(handle),
	}
}

func (d *Adapter) newGatewayTurn(handle kernel.TurnHandle) *gatewayTurn {
	return d.newGatewayTurnWithSubscription(handle, nil, false)
}

func (d *Adapter) subscribeGatewayTurn(ref session.SessionRef) (controlclient.FeedSubscription, error) {
	if d == nil || d.stack == nil || d.stack.ControlFeeds == nil {
		return nil, nil
	}
	feed, err := d.stack.ControlFeeds.Session(ref)
	if err != nil {
		return nil, err
	}
	// The gatewayTurn owns caller cancellation ordering. Binding the feed
	// subscription directly to the caller context would let its watcher close
	// the channel before the Turn records cancellation and crosses its producer
	// barrier.
	return feed.SubscribeFromNow(context.Background())
}

func (d *Adapter) newGatewayTurnWithSubscription(
	handle kernel.TurnHandle,
	prepared controlclient.FeedSubscription,
	preparedBeforeTurn bool,
	ownerContexts ...context.Context,
) *gatewayTurn {
	ingress := turningress.New(handle)
	turn := &gatewayTurn{handle: handle, feed: ingress}
	if d != nil && d.stack != nil && d.stack.ControlFeeds != nil && handle != nil {
		if feed, err := d.stack.ControlFeeds.Session(handle.SessionRef()); err == nil {
			turn.sessionFeed = feed
			subscription := prepared
			if subscription == nil && !preparedBeforeTurn {
				subscription, err = feed.SubscribeFromNow(context.Background())
			}
			if err == nil && subscription != nil {
				turn.subscription = subscription
				turn.attach = func() <-chan error {
					return feed.AttachTo(subscription, ingress.Events())
				}
			}
		}
	}
	if len(ownerContexts) > 0 {
		turn.watchOwnerContext(ownerContexts[0])
	}
	return turn
}

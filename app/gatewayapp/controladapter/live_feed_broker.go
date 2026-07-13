package controladapter

import (
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/internal/controlclient"
	"github.com/caelis-labs/caelis/internal/controlclient/turningress"
	"github.com/caelis-labs/caelis/ports/gateway"
)

type liveFeedBroker = turningress.Broker

func newGatewayTurn(handle gateway.TurnHandle, streams func() stream.Service, recorders ...*controlclient.ChildRecorder) *gatewayTurn {
	return &gatewayTurn{
		handle: handle,
		feed:   newLiveFeedBroker(handle, streams, recorders...),
	}
}

func (d *Adapter) newGatewayTurn(handle gateway.TurnHandle) *gatewayTurn {
	var recorder *controlclient.ChildRecorder
	if d != nil && d.stack != nil && d.stack.Session.Store != nil {
		recorder = controlclient.NewChildRecorder(d.stack.Session.Store)
	}
	ingress := newLiveFeedBroker(handle, func() stream.Service {
		provider, err := d.gatewayStreams()
		if err != nil || provider == nil {
			return nil
		}
		return provider.Streams()
	}, recorder)
	turn := &gatewayTurn{handle: handle, feed: ingress}
	if d != nil && d.stack != nil && d.stack.ControlFeeds != nil && handle != nil {
		if feed, err := d.stack.ControlFeeds.Session(handle.SessionRef()); err == nil {
			if subscription, err := feed.SubscribeFromNow(); err == nil {
				turn.subscription = subscription
				feed.Attach(ingress.Events())
			}
		}
	}
	return turn
}

func newLiveFeedBroker(handle gateway.TurnHandle, streams func() stream.Service, recorders ...*controlclient.ChildRecorder) *liveFeedBroker {
	return turningress.New(handle, streams, recorders...)
}

package gatewayapp

import (
	"context"
	"errors"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/controlclient/turningress"
	"github.com/caelis-labs/caelis/internal/kernel"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func (s *runtimeComposition) attachControlClientHandle(handle kernel.TurnHandle) {
	if handle == nil {
		return
	}
	releaseWork := func() {}
	if s.retainRuntimeWork != nil {
		releaseWork = s.retainRuntimeWork(handle.SessionRef())
	}
	ingress := turningress.New(handle)
	events := ingress.Events()
	if s.authorities.controlFeeds == nil {
		go func() {
			defer releaseWork()
			finishFailedControlClientAttachment(ingress, nil, events, errors.New("gatewayapp: control session feed is unavailable"))
		}()
		return
	}
	feed, err := s.authorities.controlFeeds.Session(handle.SessionRef())
	if err != nil {
		go func() {
			defer releaseWork()
			finishFailedControlClientAttachment(ingress, nil, events, err)
		}()
		return
	}
	go func() {
		defer releaseWork()
		superviseControlClientAttachment(ingress, feed, events, feed.Attach(events))
	}()
}

func superviseControlClientAttachment(
	ingress *turningress.Broker,
	feed appserver.SessionFeed,
	events <-chan eventstream.Envelope,
	attachment <-chan error,
) {
	if err := controlClientAttachmentError(attachment); err != nil {
		finishFailedControlClientAttachment(ingress, feed, events, err)
	}
}

func finishFailedControlClientAttachment(
	ingress *turningress.Broker,
	feed appserver.SessionFeed,
	events <-chan eventstream.Envelope,
	err error,
) {
	if ingress == nil {
		return
	}
	if err == nil {
		err = errors.New("gatewayapp: control session feed attachment failed")
	}
	ingress.RequestStop(eventstream.LifecycleStateFailed, err.Error())
	ingress.CancelProducer()
	if feed != nil {
		primeCtx, cancel := context.WithTimeout(context.Background(), controlFeedPublishTimeout)
		_ = feed.Prime(primeCtx)
		cancel()
		if controlClientAttachmentError(feed.Attach(events)) == nil {
			return
		}
	}
	// The Session feed is unavailable, but the Runtime producer barrier still
	// owns the execution lease. Drain the shared ingress until ACPEvents closes.
	for range events {
	}
}

func controlClientAttachmentError(result <-chan error) error {
	if result == nil {
		return errors.New("gatewayapp: control session feed returned no attachment result")
	}
	var first error
	for err := range result {
		if first == nil && err != nil {
			first = err
		}
	}
	return first
}

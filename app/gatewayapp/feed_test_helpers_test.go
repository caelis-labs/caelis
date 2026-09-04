package gatewayapp

import (
	"context"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/streamspool"
	streamspoolfile "github.com/caelis-labs/caelis/control/streamspool/file"
)

func newGatewayTestStreamSpool(t *testing.T) streamspool.Store {
	t.Helper()
	store, err := streamspoolfile.New(context.Background(), streamspoolfile.Config{RootDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func nextGatewayFeedEvents(
	ctx context.Context,
	subscription appserver.FeedSubscription,
	assembler *appserver.FeedDeliveryAssembler,
) ([]eventstream.Envelope, bool, error) {
	if subscription == nil {
		return nil, false, errors.New("test Session feed subscription is unavailable")
	}
	if assembler == nil {
		assembler = &appserver.FeedDeliveryAssembler{}
	}
	for {
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case delivery, ok := <-subscription.Deliveries():
			if !ok {
				if err := subscription.Err(); err != nil {
					return nil, false, err
				}
				return nil, false, errors.New("test Session feed closed")
			}
			events, replaced, err := assembler.Accept(delivery)
			if err != nil {
				return nil, false, err
			}
			if replaced || len(events) > 0 {
				return events, replaced, nil
			}
		}
	}
}

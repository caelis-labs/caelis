package controlclient

import (
	"context"
	"fmt"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func BenchmarkFeedBrokerReconnectFirstEnvelope(b *testing.B) {
	for _, historySize := range []int{1_000, 10_000, 100_000} {
		b.Run(fmt.Sprintf("history_%d", historySize), func(b *testing.B) {
			events := make([]*session.Event, 0, historySize)
			for seq := 1; seq <= historySize; seq++ {
				event := durableProtocolEvent(uint64(seq), "history")
				event.ID = fmt.Sprintf("event-%d", seq)
				events = append(events, event)
			}
			reader := &checkpointPageReader{events: events, active: session.Session{
				SessionRef: session.SessionRef{SessionID: "session-1"}, Revision: uint64(historySize),
			}}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				broker, _ := newBenchmarkFeedBroker(b, reader)
				result, err := broker.Subscribe(context.Background(), SubscribeRequest{SessionID: "session-1"})
				if err != nil {
					b.Fatal(err)
				}
				if _, open := <-result.Subscription.Backfill(); !open {
					b.Fatal("backfill closed before first envelope")
				}
				_ = result.Subscription.Close()
				_ = broker.Close()
			}
		})
	}
}

func newBenchmarkFeedBroker(b *testing.B, reader session.PagedReader) (*FeedBroker, error) {
	b.Helper()
	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{
		Secret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		return nil, err
	}
	return NewFeedBroker(FeedBrokerConfig{
		SessionRef: session.SessionRef{SessionID: "session-1"}, Reader: reader, CursorCodec: codec,
	})
}

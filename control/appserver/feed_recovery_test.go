package appserver

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/streamspool"
	streamspoolfile "github.com/caelis-labs/caelis/control/streamspool/file"
)

func TestFeedBrokerRecoveryReplacesTransientPrefixBeforeTerminal(t *testing.T) {
	for _, failure := range []string{"read", "sealed_tail", "append"} {
		t.Run(failure, func(t *testing.T) {
			physical, err := streamspoolfile.New(t.Context(), streamspoolfile.Config{RootDir: t.TempDir(), GCInterval: -1})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = physical.Close() })
			var store streamspool.Store = physical
			appendFailure := &failFeedAppendStore{Store: physical}
			if failure == "read" {
				store = &failReaderAfterStore{Store: physical, records: 1}
			}
			if failure == "append" {
				store = appendFailure
			}
			reader := &checkpointPageReader{active: session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}}}
			codec, err := NewCursorCodec(CursorCodecConfig{Secret: make([]byte, 32)})
			if err != nil {
				t.Fatal(err)
			}
			broker, err := NewFeedBroker(FeedBrokerConfig{SessionRef: reader.active.SessionRef, Reader: reader, Spool: store, CursorCodec: codec})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = broker.Close() })
			result, err := broker.Subscribe(t.Context(), SubscribeRequest{SessionID: "session-1"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = result.Subscription.Close() })
			assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliverySync)

			final := durableProtocolEvent(1, "hello")
			final.MessageID, final.Protocol.Update.MessageID = "message-1", "message-1"
			reader.setEvents(final)
			live := eventstream.Envelope{
				Kind: eventstream.KindSessionUpdate, SessionID: "session-1", HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1",
				Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
				Update:   eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "message-1", Content: eventstream.TextContent{Type: "text", Text: "hel"}},
			}
			if err := broker.Publish(live); err != nil {
				t.Fatal(err)
			}
			prefix := assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryAppendPage)
			if got := prefix.Events[0].Update.(eventstream.ContentChunk).Content.(eventstream.TextContent).Text; got != "hel" {
				t.Fatalf("prefix = %q", got)
			}
			if failure == "sealed_tail" {
				if err := broker.Seal(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "append" {
				appendFailure.fail.Store(true)
				terminal := eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Now())
				terminal.SessionID = "session-1"
				if err := broker.Publish(terminal); err != nil {
					t.Fatal(err)
				}
			}
			events := receiveFeedReplacement(t, result.Subscription)
			if len(events) != 1 {
				t.Fatalf("replacement = %#v", events)
			}
			if got := events[0].Update.(eventstream.ContentChunk).Content.(eventstream.TextContent).Text; got != "hello" {
				t.Fatalf("replacement text = %q", got)
			}
			if failure == "append" {
				terminal := assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryAppendPage)
				if terminal.Source != FeedSourceResult || len(terminal.Events) != 1 || !eventstream.IsTurnTerminalLifecycle(terminal.Events[0]) {
					t.Fatalf("terminal after replacement = %#v", terminal)
				}
			}
		})
	}
}

func receiveFeedReplacement(t *testing.T, sub FeedSubscription) []eventstream.Envelope {
	t.Helper()
	assembler := &FeedDeliveryAssembler{}
	begin := assertFeedDelivery(t, sub.Deliveries(), FeedDeliveryReplaceBegin)
	if _, _, err := assembler.Accept(begin); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case delivery, open := <-sub.Deliveries():
			if !open {
				t.Fatalf("replacement closed before commit: %v", sub.Err())
			}
			events, replaced, err := assembler.Accept(delivery)
			if err != nil {
				t.Fatal(err)
			}
			if replaced {
				assertFeedDelivery(t, sub.Deliveries(), FeedDeliverySync)
				return events
			}
			if len(events) != 0 {
				t.Fatalf("uncommitted replacement became visible: %#v", events)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("replacement did not commit")
		}
	}
}

type failFeedAppendStore struct {
	streamspool.Store
	fail atomic.Bool
}

func (s *failFeedAppendStore) Register(ctx context.Context, key streamspool.LogicalKey, options streamspool.WriterOptions) (streamspool.Writer, error) {
	writer, err := s.Store.Register(ctx, key, options)
	return &failFeedAppendWriter{Writer: writer, fail: &s.fail}, err
}

type failFeedAppendWriter struct {
	streamspool.Writer
	fail *atomic.Bool
}

func (w *failFeedAppendWriter) Append(ctx context.Context, kind uint16, at time.Time, payload []byte) (streamspool.Offset, error) {
	if w.fail.Load() {
		return 0, errors.New("test append failure")
	}
	return w.Writer.Append(ctx, kind, at, payload)
}

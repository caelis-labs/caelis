package httpclient

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
)

func TestRemoteTaskSubscriptionCarriesCursorInDelivery(t *testing.T) {
	reader, writer := io.Pipe()
	response := &http.Response{Body: reader}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), defaultRemoteMaxEvent)
	subscription := newRemoteTaskSubscription(response, scanner)

	raw := taskDeliveryJSON(t, taskstream.Delivery{
		Kind: taskstream.DeliveryAppendPage, Source: taskstream.SourceExact, NextCursor: "cursor-1",
		Events: []eventstream.Envelope{{Kind: eventstream.KindNotice, Notice: "ready"}},
	})
	go func() {
		_, _ = fmt.Fprintf(writer, "event: %s\nid: cursor-1\ndata: %s\n\n", wirev1.TaskStreamDeliveryEventName, raw)
		_, _ = fmt.Fprintf(writer, "event: %s\ndata: {}\n\n", wirev1.TaskStreamDoneEventName)
		_ = writer.Close()
	}()

	select {
	case delivery := <-subscription.Deliveries():
		if len(delivery.Events) != 1 || delivery.Events[0].Notice != "ready" {
			t.Fatalf("delivery = %#v", delivery)
		}
		if got := delivery.NextCursor; got != "cursor-1" {
			t.Fatalf("NextCursor = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for delivery")
	}
	_ = subscription.Close()
	if err := subscription.Err(); err != nil {
		t.Fatalf("Err() = %v after explicit done", err)
	}
}

func TestRemoteTaskSubscriptionBareEOFIsUnavailable(t *testing.T) {
	reader, writer := io.Pipe()
	response := &http.Response{Body: reader}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), defaultRemoteMaxEvent)
	subscription := newRemoteTaskSubscription(response, scanner)
	_ = writer.Close()
	select {
	case <-subscription.done:
	case <-time.After(time.Second):
		t.Fatal("read loop did not exit")
	}
	if !errorcode.Is(subscription.Err(), errorcode.Unavailable) {
		t.Fatalf("Err() = %v", subscription.Err())
	}
	_ = subscription.Close()
}

func TestClassifyTaskStreamReadError(t *testing.T) {
	if !errorcode.Is(classifyTaskStreamReadError(io.EOF), errorcode.Unavailable) {
		t.Fatal("EOF should be Unavailable")
	}
	if !errorcode.Is(classifyTaskStreamReadError(io.ErrUnexpectedEOF), errorcode.Unavailable) {
		t.Fatal("UnexpectedEOF should be Unavailable")
	}
	if !errorcode.Is(classifyTaskStreamReadError(bufio.ErrTooLong), errorcode.InvalidArgument) {
		t.Fatal("ErrTooLong should be InvalidArgument")
	}
	if !errorcode.Is(classifyTaskStreamReadError(&net.OpError{Op: "read", Err: errors.New("connection reset by peer")}), errorcode.Unavailable) {
		t.Fatal("connection reset should be Unavailable")
	}
}

func taskDeliveryJSON(t *testing.T, delivery taskstream.Delivery) []byte {
	t.Helper()
	wire := wirev1.TaskStreamDelivery{
		Kind: string(delivery.Kind), Source: string(delivery.Source), SnapshotID: delivery.SnapshotID,
		Page: delivery.Page, NextCursor: delivery.NextCursor, ActivityID: delivery.ActivityID,
	}
	for _, envelope := range delivery.Events {
		raw, err := wirev1.MarshalEnvelope(envelope)
		if err != nil {
			t.Fatal(err)
		}
		wire.Events = append(wire.Events, raw)
	}
	raw, err := wirev1.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

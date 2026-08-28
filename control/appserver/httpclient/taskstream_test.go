package httpclient

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
)

func TestRemoteTaskSubscriptionCloseUnblocksFullBuffer(t *testing.T) {
	reader, writer := io.Pipe()
	response := &http.Response{Body: reader}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), defaultRemoteMaxEvent)
	subscription := newRemoteTaskSubscription(response, scanner, 1)

	subscription.events <- eventstream.Envelope{Cursor: "buffered-1"}

	var writerDone sync.WaitGroup
	writerDone.Add(1)
	go func() {
		defer writerDone.Done()
		_, _ = io.WriteString(writer, "id: blocked-2\ndata: {\"kind\":\"notice\",\"cursor\":\"blocked-2\",\"session_id\":\"s1\",\"notice\":\"x\"}\n\n")
	}()

	done := make(chan struct{})
	go func() {
		_ = subscription.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close deadlocked behind a full Task subscription buffer")
	}
	_ = writer.Close()
	writerDone.Wait()
}

func TestRemoteTaskSubscriptionLastCursorLinearWithDelivery(t *testing.T) {
	// Under concurrency, receiving an event must not observe a stale LastCursor.
	for i := 0; i < 200; i++ {
		reader, writer := io.Pipe()
		response := &http.Response{Body: reader}
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64<<10), defaultRemoteMaxEvent)
		subscription := newRemoteTaskSubscription(response, scanner, 8)

		_, _ = io.WriteString(writer, "id: delivered-1\ndata: {\"kind\":\"notice\",\"cursor\":\"delivered-1\",\"session_id\":\"s1\",\"notice\":\"a\"}\n\n")
		select {
		case envelope := <-subscription.Events():
			if envelope.Cursor != "delivered-1" {
				t.Fatalf("delivered envelope = %#v", envelope)
			}
			if got := subscription.LastCursor(); got != "delivered-1" {
				t.Fatalf("LastCursor() = %q immediately after receive, want delivered-1", got)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for delivered event")
		}
		_ = writer.Close()
		_ = subscription.Close()
	}
}

func TestRemoteTaskSubscriptionSlowConsumerDoesNotAdvancePastUndeliveredEvent(t *testing.T) {
	reader, writer := io.Pipe()
	response := &http.Response{Body: reader}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), defaultRemoteMaxEvent)
	subscription := newRemoteTaskSubscription(response, scanner, 1)

	_, _ = io.WriteString(writer, "id: delivered-1\ndata: {\"kind\":\"notice\",\"cursor\":\"delivered-1\",\"session_id\":\"s1\",\"notice\":\"a\"}\n\n")
	select {
	case envelope := <-subscription.Events():
		if envelope.Cursor != "delivered-1" {
			t.Fatalf("delivered envelope = %#v", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for delivered event")
	}
	if got := subscription.LastCursor(); got != "delivered-1" {
		t.Fatalf("LastCursor() = %q after delivery, want delivered-1", got)
	}

	subscription.events <- eventstream.Envelope{Cursor: "local-hold"}
	go func() {
		_, _ = io.WriteString(writer, "id: undelivered\ndata: {\"kind\":\"notice\",\"cursor\":\"undelivered\",\"session_id\":\"s1\",\"notice\":\"z\"}\n\n")
		_ = writer.Close()
	}()

	select {
	case <-subscription.done:
	case <-time.After(time.Second):
		t.Fatal("readLoop did not exit after slow consumer")
	}
	if err := subscription.Err(); !errors.Is(err, taskstream.ErrSlowConsumer) {
		t.Fatalf("Err() = %v, want slow consumer", err)
	}
	if got := subscription.LastCursor(); got != "delivered-1" {
		t.Fatalf("LastCursor() = %q, want last accepted delivered-1 (not undelivered)", got)
	}
	_ = subscription.Close()
}

func TestRemoteTaskSubscriptionBareEOFIsRetryableUnavailable(t *testing.T) {
	reader, writer := io.Pipe()
	response := &http.Response{Body: reader}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), defaultRemoteMaxEvent)
	subscription := newRemoteTaskSubscription(response, scanner, 4)

	_, _ = io.WriteString(writer, "id: delivered-1\ndata: {\"kind\":\"notice\",\"cursor\":\"delivered-1\",\"session_id\":\"s1\",\"notice\":\"a\"}\n\n")
	select {
	case <-subscription.Events():
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for delivered event")
	}
	_ = writer.Close()
	select {
	case <-subscription.done:
	case <-time.After(time.Second):
		t.Fatal("readLoop did not exit after transport EOF")
	}
	err := subscription.Err()
	if err == nil {
		t.Fatal("Err() = nil after bare EOF, want retryable Unavailable")
	}
	if !errorcode.Is(err, errorcode.Unavailable) {
		t.Fatalf("Err() = %v, want Unavailable", err)
	}
	if got := subscription.LastCursor(); got != "delivered-1" {
		t.Fatalf("LastCursor() = %q after bare EOF, want last accepted cursor", got)
	}
	_ = subscription.Close()
}

func TestRemoteTaskSubscriptionUnexpectedEOFIsRetryableUnavailable(t *testing.T) {
	body := &sequenceReader{reads: []readResult{
		{data: []byte("id: delivered-1\ndata: {\"kind\":\"notice\",\"cursor\":\"delivered-1\",\"session_id\":\"s1\",\"notice\":\"a\"}\n\n")},
		{err: io.ErrUnexpectedEOF},
	}}
	response := &http.Response{Body: io.NopCloser(body)}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), defaultRemoteMaxEvent)
	subscription := newRemoteTaskSubscription(response, scanner, 4)

	select {
	case <-subscription.Events():
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for delivered event")
	}
	select {
	case <-subscription.done:
	case <-time.After(time.Second):
		t.Fatal("readLoop did not exit after UnexpectedEOF")
	}
	err := subscription.Err()
	if !errorcode.Is(err, errorcode.Unavailable) {
		t.Fatalf("Err() = %v, want Unavailable for abrupt truncation", err)
	}
	if got := subscription.LastCursor(); got != "delivered-1" {
		t.Fatalf("LastCursor() = %q, want last accepted cursor", got)
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
		t.Fatal("ErrTooLong should be InvalidArgument, not retryable")
	}
	if !errorcode.Is(classifyTaskStreamReadError(&net.OpError{Op: "read", Err: errors.New("connection reset by peer")}), errorcode.Unavailable) {
		t.Fatal("connection reset should be Unavailable")
	}
	malformed := errors.New("json: syntax error")
	if errorcode.Is(classifyTaskStreamReadError(malformed), errorcode.Unavailable) {
		t.Fatal("malformed payload errors must not become Unavailable")
	}
}

func TestRemoteTaskSubscriptionExplicitDoneIsCleanEnd(t *testing.T) {
	reader, writer := io.Pipe()
	response := &http.Response{Body: reader}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), defaultRemoteMaxEvent)
	subscription := newRemoteTaskSubscription(response, scanner, 4)

	_, _ = io.WriteString(writer, "id: delivered-1\ndata: {\"kind\":\"notice\",\"cursor\":\"delivered-1\",\"session_id\":\"s1\",\"notice\":\"a\"}\n\n")
	_, _ = io.WriteString(writer, "event: "+wirev1.TaskStreamDoneEventName+"\ndata: {}\n\n")
	_ = writer.Close()

	select {
	case <-subscription.Events():
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for delivered event")
	}
	select {
	case <-subscription.done:
	case <-time.After(time.Second):
		t.Fatal("readLoop did not exit after explicit done")
	}
	if err := subscription.Err(); err != nil {
		t.Fatalf("Err() = %v after explicit done, want nil", err)
	}
	if got := subscription.LastCursor(); got != "delivered-1" {
		t.Fatalf("LastCursor() = %q, want delivered-1", got)
	}
	_ = subscription.Close()
}

type readResult struct {
	data []byte
	err  error
}

type sequenceReader struct {
	reads []readResult
	index int
	off   int
}

func (r *sequenceReader) Read(p []byte) (int, error) {
	if r.index >= len(r.reads) {
		return 0, io.EOF
	}
	item := r.reads[r.index]
	if item.err != nil && r.off >= len(item.data) {
		r.index++
		r.off = 0
		return 0, item.err
	}
	n := copy(p, item.data[r.off:])
	r.off += n
	if r.off >= len(item.data) {
		r.index++
		r.off = 0
		if item.err != nil {
			return n, item.err
		}
	}
	return n, nil
}

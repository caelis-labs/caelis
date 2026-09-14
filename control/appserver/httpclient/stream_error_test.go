package httpclient

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

func TestClassifyStreamReadError(t *testing.T) {
	for _, source := range []string{"Task stream", "Session feed"} {
		t.Run(source, func(t *testing.T) {
			for _, err := range []error{io.EOF, io.ErrUnexpectedEOF, &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}} {
				if got := classifyStreamReadError(source, err); !errorcode.Is(got, errorcode.Unavailable) {
					t.Fatalf("classify(%v) = %v, want Unavailable", err, got)
				}
			}
			if got := classifyStreamReadError(source, bufio.ErrTooLong); !errorcode.Is(got, errorcode.InvalidArgument) {
				t.Fatalf("classify(ErrTooLong) = %v", got)
			}
			for _, err := range []error{nil, context.Canceled, errors.New("invalid wire data")} {
				if got := classifyStreamReadError(source, err); !errors.Is(got, err) {
					t.Fatalf("classify(%v) = %v, want original error preserved", err, got)
				}
			}
		})
	}
}

func TestRemoteSessionFeedTransportLossIsReconnectable(t *testing.T) {
	reader, writer := io.Pipe()
	subscription := newRemoteSubscription(&http.Response{Body: reader}, bufio.NewScanner(reader))
	t.Cleanup(func() { _ = subscription.Close() })
	_ = writer.CloseWithError(&net.OpError{Op: "read", Err: errors.New("connection reset by peer")})
	for range subscription.Deliveries() {
		t.Fatal("broken feed produced a delivery")
	}
	if err := subscription.Err(); !errorcode.Is(err, errorcode.Unavailable) {
		t.Fatalf("Err() = %v, want reconnectable observation loss", err)
	}
}

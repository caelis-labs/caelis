package controlserver

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	internalcontrolclient "github.com/caelis-labs/caelis/internal/controlclient"
	controlport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/surfaces/appserver"
)

func TestInProcessAndHTTPSSEReceiveSameBrokerEnvelope(t *testing.T) {
	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := internalcontrolclient.NewFeedRegistry(internalcontrolclient.FeedRegistryConfig{CursorCodec: codec})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := registry.Session(session.SessionRef{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	wantSource := eventstream.Envelope{
		Kind: eventstream.KindNotice, SessionID: "session-1", Notice: "\x1b[32m你好\x1b[0m",
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Meta:     map[string]any{"terminal_output": "\x1b[32m你好\x1b[0m", "exit_code": float64(0)},
	}
	if err := feed.Publish(wantSource); err != nil {
		t.Fatal(err)
	}

	inProcess, err := feed.Subscribe(context.Background(), controlport.SubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	want := receiveParityEnvelope(t, inProcess.Subscription.Backfill())
	_ = inProcess.Subscription.Close()

	server, err := appserver.New(appserver.Config{
		Service: parityService{feed: feed}, LocalPrincipal: controlport.Principal{ID: "owner"}, Heartbeat: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	request, err := http.NewRequest(http.MethodGet, httpServer.URL+"/api/control/v1/sessions/session-1/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	for range 3 {
		if _, err := reader.ReadString('\n'); err != nil {
			t.Fatal(err)
		}
	}
	idLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	dataLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(strings.TrimPrefix(idLine, "id: ")) != want.Cursor {
		t.Fatalf("SSE id = %q, want %q", idLine, want.Cursor)
	}
	var got eventstream.Envelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(dataLine, "data: "))), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("HTTP/SSE Envelope = %#v, want in-process %#v", got, want)
	}
}

type parityService struct {
	controlport.Service
	feed controlport.SessionFeed
}

func (s parityService) Subscribe(ctx context.Context, _ controlport.Principal, req controlport.SubscribeRequest) (controlport.SubscribeResult, error) {
	return s.feed.Subscribe(ctx, req)
}

func receiveParityEnvelope(t *testing.T, events <-chan eventstream.Envelope) eventstream.Envelope {
	t.Helper()
	select {
	case envelope := <-events:
		return envelope
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for broker Envelope")
		return eventstream.Envelope{}
	}
}

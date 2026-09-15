package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/mcpconfig"
)

func TestMCPBlockedHTTPHandshakeDoesNotBlockRuntimeActivation(t *testing.T) {
	entered := make(chan struct{}, 1)
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Block handshake calls, not the SDK's cancellation notification. Blocking
		// both makes cleanup exhaust its five-second notification timeout.
		if request.Method == "notifications/cancelled" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	defer func() { close(stop); server.Close() }()
	stack, active := newLocalStateTestStack(t)
	doc, err := stack.composition.authorities.store.LoadContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	doc.MCPServers = mcpconfig.Servers{"blocked": {Transport: mcp.TransportStreamableHTTP, URL: server.URL}}
	if err := stack.composition.authorities.store.Save(doc); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	runtime, release := activateHeldSessionRuntime(t, stack, active.SessionID)
	defer release()
	if elapsed := time.Since(started); elapsed >= 5*time.Second {
		t.Fatalf("activation waited for MCP: %s", elapsed)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("background handshake never started")
	}
	select {
	case <-runtime.instance.mcpMgr.Initialized():
		t.Fatal("blocked handshake unexpectedly completed")
	default:
	}
	if runtime.instance.currentGateway() == nil || len(runtime.instance.mcpMgr.Tools()) != 0 {
		t.Fatal("Runtime is unavailable or unready MCP tools were published")
	}
	if err := runtime.instance.mcpMgr.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMCPFailurePublishesOneNoticeWithoutChangingCanonicalSession(t *testing.T) {
	stack, active := newLocalStateTestStack(t)
	doc, err := stack.composition.authorities.store.LoadContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	doc.MCPServers = mcpconfig.Servers{"unreachable": {Command: filepath.Join(t.TempDir(), "missing-mcp")}}
	if err := stack.composition.authorities.store.Save(doc); err != nil {
		t.Fatal(err)
	}
	feed, err := stack.composition.authorities.controlFeeds.Session(active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	_, cursor := feed.Boundary()
	sub, err := feed.Subscribe(t.Context(), appserver.SubscribeRequest{SessionID: active.SessionID, Cursor: cursor})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Subscription.Close()
	before, err := stack.composition.sessions.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	runtime, release := activateHeldSessionRuntime(t, stack, active.SessionID)
	defer release()
	<-runtime.instance.mcpMgr.Initialized()
	if runtime.instance.currentGateway() == nil {
		t.Fatal("failed MCP prevented Runtime startup")
	}
	found := false
	deadline := time.After(3 * time.Second)
	for !found {
		select {
		case delivery := <-sub.Subscription.Deliveries():
			for _, event := range delivery.Events {
				if event.Kind != eventstream.KindNotice {
					continue
				}
				if event.Delivery == nil || event.Delivery.Mode != eventstream.DeliveryTransient || event.Position == nil || event.Position.Durable != nil {
					t.Fatalf("notice claimed persistence: %#v", event)
				}
				if event.Notice != mcpFailureNotice(mcp.ServerFailure{Name: "unreachable", Err: errors.New("failure")}) {
					t.Fatalf("notice=%q", event.Notice)
				}
				if found {
					t.Fatal("duplicate failure notice")
				}
				found = true
			}
		case <-deadline:
			t.Fatal("MCP failure notice was not delivered")
		}
	}
	// Reopen the physical Session store, independently of the disposable feed.
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(stack.composition.authorities.storeDir, "sessions")})
	after, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("MCP notice changed canonical events: before=%#v after=%#v", before, after)
	}
}

func TestMCPFailureNoticeIsBoundedAndDoesNotExposePrivateCause(t *testing.T) {
	for _, failure := range []mcp.ServerFailure{
		{Name: "context7", Err: context.DeadlineExceeded},
		{Name: "context7\x1b\n", Err: errors.New("token=private /Users/private/path")},
	} {
		text := mcpFailureNotice(failure)
		if strings.ContainsAny(text, "\x1b\n") || strings.Contains(text, "token=") || strings.Contains(text, "/Users/") {
			t.Fatalf("unsafe notice=%q", text)
		}
		if errors.Is(failure.Err, context.DeadlineExceeded) && !strings.Contains(text, "30 seconds") {
			t.Fatalf("missing timeout: %s", text)
		}
	}
}

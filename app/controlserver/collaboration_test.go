package controlserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/collaboration"
	surfacemcp "github.com/caelis-labs/caelis/surfaces/mcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mailboxTestBackend struct{}

func (mailboxTestBackend) List(context.Context, string) ([]collaboration.Thread, error) {
	return []collaboration.Thread{{ID: "a", SessionID: "a", Handle: "a"}, {ID: "b", SessionID: "b", Handle: "b"}}, nil
}

func (mailboxTestBackend) Deliver(context.Context, string, []collaboration.Message) error {
	return errors.New("automatic delivery is not used by the transport test")
}

func TestCollaborationMCPHTTPRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	service, err := collaboration.Open(filepath.Join(t.TempDir(), "control.sqlite"), mailboxTestBackend{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Close() }()
	services := testAppServerServices(&fakeService{}, staticStatusService{})
	services.Collaboration = service
	handler, err := New(HandlerConfig{Services: services, Authenticator: testAuthenticator(), AllowedHosts: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	grant := service.Prepare(collaboration.Identity{Session: "work", Member: "a"}, "a")
	if err := grant.Bind("a"); err != nil {
		t.Fatal(err)
	}
	token := grant.Token()
	clientSide, serverSide := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- surfacemcp.Run(ctx, httpclient.Collaboration(server.URL, token), serverSide) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 5 {
		t.Fatalf("tools %v %v", tools, err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "SendMessage", Arguments: map[string]any{"to": "b", "message": "hello"}})
	if err != nil || result.IsError {
		t.Fatalf("send %#v %v", result, err)
	}
	mail, err := service.Receive(ctx, collaboration.Identity{Session: "work", Member: "b"})
	if err != nil || len(mail) != 1 || mail[0].Text != "hello" {
		t.Fatalf("mail %v %v", mail, err)
	}
	if _, err = httpclient.Collaboration(server.URL, "wrong")(ctx, collaboration.Request{Tool: "ListThreads", Arguments: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("accepted invalid credential")
	}
	large := strings.Repeat("<", 65536)
	for range 32 {
		if _, err := service.Send(ctx, collaboration.Identity{Session: "work", Member: "b"}, "a", large, ""); err != nil {
			t.Fatal(err)
		}
	}
	received := map[string]bool{}
	for len(received) < 32 {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "ReceiveMessages", Arguments: map[string]any{}})
		if err != nil || result.IsError {
			t.Fatalf("large MCP receive: %#v %v", result, err)
		}
		var batch []collaboration.Message
		if len(result.Content) != 1 {
			t.Fatalf("content: %#v", result.Content)
		}
		content, ok := result.Content[0].(*mcp.TextContent)
		if !ok {
			t.Fatalf("content type %T", result.Content[0])
		}
		if err := json.Unmarshal([]byte(content.Text), &batch); err != nil {
			t.Fatal(err)
		}
		if len(batch) == 0 {
			t.Fatalf("lost large messages: %d", len(received))
		}
		for _, m := range batch {
			if received[m.ID] || m.Text != large {
				t.Fatal("duplicate or damaged MCP message")
			}
			received[m.ID] = true
		}
	}
	_ = session.Close()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

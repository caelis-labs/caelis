package client

import (
	"context"
	"encoding/json"
	"net"
	"reflect"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
)

func TestNoticeWirePreservesOrderAcrossThoughtAndToolBoundaries(t *testing.T) {
	local, remote := net.Pipe()
	updates := make(chan UpdateEnvelope, 4)
	c, err := NewStreamClient(local, local, Config{OnUpdate: func(update UpdateEnvelope) { updates <- update }})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	capability := make(chan bool, 1)
	peer := acpsdk.NewConnection(func(_ context.Context, method string, raw json.RawMessage) (any, *acpsdk.RequestError) {
		if method != MethodInitialize {
			return nil, acpsdk.NewMethodNotFound(method)
		}
		var request acpsdk.InitializeRequest
		_ = json.Unmarshal(raw, &request)
		capability <- string(request.ClientCapabilities.Meta[sessionNoticeCapability]) == "true"
		return acpsdk.InitializeResponse{ProtocolVersion: acpsdk.ProtocolVersionNumber}, nil
	}, remote, remote)
	defer peer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if !<-capability {
		t.Fatal("notice fallback capability was not advertised")
	}
	for i, update := range []string{
		`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"Thinking"}}`,
		`{"sessionUpdate":"notice","severity":"info","title":"Review approved"}`,
		`{"sessionUpdate":"notice","severity":"info","title":"Review approved"}`,
		`{"sessionUpdate":"tool_call","toolCallId":"send-1","title":"SendMessage","rawInput":{"to":"parent","message":"report"}}`,
	} {
		method := MethodSessionUpdate
		if i == 2 {
			method = sessionNoticeMethod
		}
		if err := peer.SendNotification(ctx, method, json.RawMessage(`{"sessionId":"child","update":`+update+`}`)); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 4 {
		select {
		case update := <-updates:
			if update.SessionID != "child" {
				t.Fatalf("wrong session: %#v", update)
			}
			switch i {
			case 0:
				if _, ok := update.Update.(ContentChunk); !ok {
					t.Fatalf("thought = %T", update.Update)
				}
			case 1, 2:
				if notice, ok := update.Update.(Notice); !ok || notice.Title != "Review approved" {
					t.Fatalf("notice = %#v", update.Update)
				}
			case 3:
				if _, ok := update.Update.(ToolCall); !ok {
					t.Fatalf("tool = %T", update.Update)
				}
			}
		case <-ctx.Done():
			t.Fatal("notice delivery lost or blocked a later tool")
		}
	}
}

func TestNoticeDraftAndNegotiatedFallbackShareDecoder(t *testing.T) {
	for _, method := range []string{MethodSessionUpdate, sessionNoticeMethod} {
		t.Run(method, func(t *testing.T) {
			var updates []UpdateEnvelope
			c := &Client{cfg: Config{OnUpdate: func(env UpdateEnvelope) { updates = append(updates, env) }}}
			for _, raw := range []string{
				`{"sessionId":"child","update":{"sessionUpdate":"notice","severity":"warning","title":"Review approved","description":"Read-only request","_meta":{"source":"guardian"}}}`,
				`{"sessionId":"child","update":{"sessionUpdate":"notice","severity":"_custom","title":"Another notice","description":42,"_meta":false}}`,
			} {
				if _, err := c.handleNotification(method, json.RawMessage(raw)); err != nil {
					t.Fatal(err)
				}
			}
			if len(updates) != 2 || updates[0].SessionID != "child" {
				t.Fatalf("updates = %#v", updates)
			}
			first, ok := updates[0].Update.(Notice)
			if !ok || first.Title != "Review approved" || first.Description != "Read-only request" || !reflect.DeepEqual(first.Meta, map[string]any{"source": "guardian"}) {
				t.Fatalf("notice = %#v", updates[0].Update)
			}
			second, ok := updates[1].Update.(Notice)
			if !ok || second.Severity != "_custom" || second.Description != "" || second.Meta != nil {
				t.Fatalf("future notice = %#v", updates[1].Update)
			}
		})
	}
}

func TestNoticeRejectsMissingRequiredFieldsAndFallbackNonNotice(t *testing.T) {
	for _, raw := range []string{
		`{"sessionUpdate":"notice","title":"title"}`,
		`{"sessionUpdate":"notice","severity":null,"title":"title"}`,
		`{"sessionUpdate":"notice","severity":"info","title":null}`,
		`{"sessionUpdate":"notice","severity":"info","title":" "}`,
	} {
		if _, err := decodeUpdate(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted malformed notice: %s", raw)
		}
	}
	c := &Client{cfg: Config{OnUpdate: func(UpdateEnvelope) { t.Fatal("extension accepted non-notice") }}}
	_, _ = c.handleNotification(sessionNoticeMethod, json.RawMessage(`{"sessionId":"child","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"forged"}}}`))
}

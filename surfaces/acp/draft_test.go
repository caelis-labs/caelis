package acp

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
)

func newDraftTestConnection(t *testing.T) (context.Context, *acpsdk.Connection) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	clientSide, serverSide := net.Pipe()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- ServeDraftStdio(ctx, DraftSchemaTag, acpsdk.Implementation{Name: "caelis-test", Version: "test"}, serverSide, serverSide)
	}()
	connection, err := acpsdk.NewConnectionWithOptions(nil, clientSide, clientSide, acpsdk.ConnectionOptions{})
	if err != nil {
		cancel()
		_ = clientSide.Close()
		_ = serverSide.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = connection.Close()
		_ = clientSide.Close()
		_ = serverSide.Close()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Error("draft ACP connection did not stop")
		}
	})
	return ctx, connection
}

func draftTestInitialize() v2.InitializeRequest {
	return v2.InitializeRequest{
		ProtocolVersion: v2.ProtocolVersionNumber,
		Info:            v2.Implementation{Name: "fixture", Version: "test"},
	}
}

func assertDraftRPCError(t *testing.T, err error, code int) {
	t.Helper()
	var requestError *acpsdk.RequestError
	if !errors.As(err, &requestError) || requestError.Code != code {
		t.Fatalf("RPC error = %v, want code %d", err, code)
	}
}

func TestDraftInitializeUsesPinnedV2SchemaAndAdvertisesNoWork(t *testing.T) {
	t.Parallel()
	ctx, connection := newDraftTestConnection(t)
	response, err := acpsdk.SendRequest[v2.InitializeResponse](connection, ctx, v2.AgentMethodInitialize, draftTestInitialize())
	if err != nil {
		t.Fatal(err)
	}
	if response.ProtocolVersion != 2 || response.Info.Name != "caelis-test" || response.Info.Version != "test" {
		t.Fatalf("v2 initialize response = %#v", response)
	}
	if response.Capabilities.Session != nil || response.Capabilities.Auth != nil || len(response.AuthMethods) != 0 {
		t.Fatalf("unimplemented capabilities were advertised: %#v", response)
	}
	if string(response.Meta[DraftSchemaMetaKey]) != `"`+DraftSchemaTag+`"` {
		t.Fatalf("schema constraint = %s", response.Meta[DraftSchemaMetaKey])
	}
	_, err = acpsdk.SendRequest[v2.InitializeResponse](connection, ctx, v2.AgentMethodInitialize, draftTestInitialize())
	assertDraftRPCError(t, err, -32600)

	for _, method := range []string{
		"session/new", "session/prompt", "session/cancel", "session/resume", "session/list", "session/close", "session/delete",
		"session/set_config_option", "session/load", "session/set_mode", "auth/login", "auth/logout", "authenticate",
		"session/request_permission", "session/update", "elicitation/create", "elicitation/complete", "_session/steering", "_vendor/work",
	} {
		_, err := acpsdk.SendRequest[json.RawMessage](connection, ctx, method, map[string]any{"sessionId": "must-not-exist"})
		assertDraftRPCError(t, err, -32601)
	}
}

func TestDraftInitializeRejectsInvalidOrIncompatibleHandshake(t *testing.T) {
	t.Parallel()
	for name, request := range map[string]json.RawMessage{
		"v1":            json.RawMessage(`{"protocolVersion":1,"clientInfo":{"name":"fixture","version":"test"}}`),
		"future":        json.RawMessage(`{"protocolVersion":3,"info":{"name":"fixture","version":"test"}}`),
		"no version":    json.RawMessage(`{"info":{"name":"fixture","version":"test"}}`),
		"null version":  json.RawMessage(`{"protocolVersion":null,"info":{"name":"fixture","version":"test"}}`),
		"no info":       json.RawMessage(`{"protocolVersion":2,"clientInfo":{"name":"fixture","version":"test"}}`),
		"null info":     json.RawMessage(`{"protocolVersion":2,"info":null}`),
		"bad info":      json.RawMessage(`{"protocolVersion":2,"info":{"name":"fixture"}}`),
		"other schema":  json.RawMessage(`{"protocolVersion":2,"info":{"name":"fixture","version":"test"},"_meta":{"caelis/acp_schema":"schema-v2.0.0-alpha.7"}}`),
		"null schema":   json.RawMessage(`{"protocolVersion":2,"info":{"name":"fixture","version":"test"},"_meta":{"caelis/acp_schema":null}}`),
		"object schema": json.RawMessage(`{"protocolVersion":2,"info":{"name":"fixture","version":"test"},"_meta":{"caelis/acp_schema":{}}}`),
	} {
		t.Run(name, func(t *testing.T) {
			ctx, connection := newDraftTestConnection(t)
			_, err := acpsdk.SendRequest[json.RawMessage](connection, ctx, v2.AgentMethodInitialize, request)
			assertDraftRPCError(t, err, -32602)
			_, err = acpsdk.SendRequest[json.RawMessage](connection, ctx, v2.AgentMethodSessionNew, map[string]any{})
			assertDraftRPCError(t, err, -32600)
		})
	}
}

func TestDraftHandshakeRequiresRequestAndConnectionLocalInitialization(t *testing.T) {
	t.Parallel()
	// Each newly connected peer negotiates independently. A connection cannot
	// import another peer's negotiated version or acquire a product Session.
	for range 2 {
		ctx, connection := newDraftTestConnection(t)
		_, err := acpsdk.SendRequest[json.RawMessage](connection, ctx, "session/new", map[string]any{})
		assertDraftRPCError(t, err, -32600)
		if err := connection.SendNotification(ctx, v2.AgentMethodInitialize, draftTestInitialize()); err != nil {
			t.Fatal(err)
		}
		request := draftTestInitialize()
		request.Meta = map[string]json.RawMessage{DraftSchemaMetaKey: json.RawMessage(`"` + DraftSchemaTag + `"`)}
		if _, err := acpsdk.SendRequest[v2.InitializeResponse](connection, ctx, v2.AgentMethodInitialize, request); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDraftConcurrentInitializePinsExactlyOneRequest(t *testing.T) {
	t.Parallel()
	ctx, connection := newDraftTestConnection(t)
	results := make(chan error, 2)
	var callers sync.WaitGroup
	for range 2 {
		callers.Go(func() {
			_, err := acpsdk.SendRequest[v2.InitializeResponse](connection, ctx, v2.AgentMethodInitialize, draftTestInitialize())
			results <- err
		})
	}
	callers.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else {
			assertDraftRPCError(t, err, -32600)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted initialize requests = %d, want one", accepted)
	}
}

func TestStableACPHandshakeNegotiatesV1ForNewerClient(t *testing.T) {
	t.Parallel()
	ctx, connection := newTestServerConnection(t, stableDraftBoundaryAgent{})
	_, err := acpsdk.SendRequest[acpsdk.InitializeResponse](connection, ctx, acpsdk.AgentMethodInitialize, acpsdk.InitializeRequest{ProtocolVersion: 0})
	assertDraftRPCError(t, err, -32602)
	for _, version := range []acpsdk.ProtocolVersion{1, 2, 3} {
		response, err := acpsdk.SendRequest[acpsdk.InitializeResponse](connection, ctx, acpsdk.AgentMethodInitialize, acpsdk.InitializeRequest{ProtocolVersion: version})
		if err != nil || response.ProtocolVersion != 1 {
			t.Fatalf("client version %d stable initialize = %#v, %v", version, response, err)
		}
	}
}

type stableDraftBoundaryAgent struct{ noAuthAgent }

func (stableDraftBoundaryAgent) Initialize(context.Context, acpsdk.InitializeRequest) (acpsdk.InitializeResponse, error) {
	return acpsdk.InitializeResponse{ProtocolVersion: 1}, nil
}

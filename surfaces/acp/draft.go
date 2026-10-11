package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
)

// DraftSchemaTag is the exact experimental schema accepted by the draft endpoints.
// Stable external ACP remains v1 unless the caller explicitly selects this draft.
const DraftSchemaTag = v2.SchemaTag

// DraftSchemaMetaKey identifies the pinned draft within protocol version 2.
// A client may supply the same key to reject a different schema before use.
const DraftSchemaMetaKey = "caelis/acp_schema"

// ServeDraftStdio exposes experimental v2 initialization and capability discovery
// over the SDK's bounded NDJSON connection. It advertises no Session or auth
// capability: product work cannot be admitted until the v2 asynchronous message,
// state, permission, and replay contracts have a complete Control projection.
// No Host, Runtime, Session, or stable v1 Agent is constructed by this endpoint.
func ServeDraftStdio(ctx context.Context, schema string, info acpsdk.Implementation, in io.Reader, out io.Writer) error {
	if schema != DraftSchemaTag {
		return fmt.Errorf("acp: unsupported draft schema %q; requires %s", schema, DraftSchemaTag)
	}
	if in == nil || out == nil {
		return errors.New("acp: stdio streams are required")
	}
	connectionInput, closeInputOnFailure, err := sdkOwnedServerInput(in)
	if err != nil {
		return err
	}
	connectionOutput, closeOutputOnFailure, err := sdkOwnedServerOutput(out)
	if err != nil {
		closeInputOnFailure()
		return err
	}
	router := &v2.ProtocolRouter{V2: draftInitializeHandler(info)}
	peer, err := acpsdk.NewConnectionWithOptions(router.Handle, connectionOutput, connectionInput,
		acpsdk.ConnectionOptions{MaxFrameSize: serverMaxFrameSize})
	if err != nil {
		closeInputOnFailure()
		closeOutputOnFailure()
		return err
	}
	defer peer.Close()
	if err := peer.Wait(ctx); err != nil {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if errors.Is(err, acpsdk.ErrPeerClosed) || errors.Is(err, acpsdk.ErrConnectionClosed) {
			return nil
		}
		return err
	}
	return nil
}

func draftInitializeHandler(info acpsdk.Implementation) acpsdk.MethodHandler {
	return func(_ context.Context, method string, params json.RawMessage) (any, *acpsdk.RequestError) {
		if method != v2.AgentMethodInitialize {
			return nil, acpsdk.NewMethodNotFound(method)
		}
		var request v2.InitializeRequest
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, acpsdk.NewInvalidParams(map[string]any{"error": err.Error()})
		}
		if err := request.Validate(); err != nil {
			return nil, acpsdk.NewInvalidParams(map[string]any{"error": err.Error()})
		}
		if raw, ok := request.Meta[DraftSchemaMetaKey]; ok {
			var schema string
			if err := json.Unmarshal(raw, &schema); err != nil || schema != DraftSchemaTag {
				return nil, acpsdk.NewInvalidParams(map[string]any{"error": "incompatible ACP draft schema"})
			}
		}
		schema, _ := json.Marshal(DraftSchemaTag)
		return v2.InitializeResponse{
			ProtocolVersion: v2.ProtocolVersionNumber,
			Info:            v2.Implementation{Name: info.Name, Title: info.Title, Version: info.Version},
			Meta:            map[string]json.RawMessage{DraftSchemaMetaKey: schema},
		}, nil
	}
}

package acppermission_test

import (
	"encoding/json"
	"reflect"
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/acppermission"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

func TestPermissionWireRoundTripPreservesSDKSemantics(t *testing.T) {
	t.Parallel()

	wantRef := session.SessionRef{SessionID: "session-1"}
	wantApproval := session.ProtocolApproval{
		ToolCall: session.ProtocolToolCall{
			ID:       "call-1",
			Name:     "RunCommand",
			Kind:     eventstream.ToolKindExecute,
			Title:    "Run command",
			Status:   eventstream.ToolStatusPending,
			RawInput: map[string]any{"command": "go test ./..."},
			Content: []session.ProtocolToolCallContent{{
				Type: "content", Content: map[string]any{"type": "text", "text": "approval needed"},
			}},
		},
		Options: []session.ProtocolApprovalOption{
			{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
			{ID: "reject_once", Name: "Reject", Kind: "reject_once"},
		},
	}
	wantMeta := map[string]any{"provider": map[string]any{"request_id": "request-1"}}

	wire, err := acppermission.EncodePermissionRequest(wantRef, &wantApproval, wantMeta)
	if err != nil {
		t.Fatalf("EncodePermissionRequest() error = %v", err)
	}
	if wire.SessionID != wantRef.SessionID {
		t.Fatalf("wire session id = %q, want %q", wire.SessionID, wantRef.SessionID)
	}
	if !reflect.DeepEqual(wire.Meta, wantMeta) {
		t.Fatalf("wire meta = %#v, want %#v", wire.Meta, wantMeta)
	}
	if got := metautil.String(wire.ToolCall.Meta, metautil.Root, metautil.Runtime, metautil.RuntimeTool, metautil.RuntimeToolName); got != wantApproval.ToolCall.Name {
		t.Fatalf("wire tool name = %q, want %q", got, wantApproval.ToolCall.Name)
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var external eventstream.RequestPermissionRequest
	if err := json.Unmarshal(raw, &external); err != nil {
		t.Fatal(err)
	}
	gotApproval, err := acppermission.DecodePermissionRequest(external)
	if err != nil {
		t.Fatalf("DecodePermissionRequest() error = %v", err)
	}
	if !reflect.DeepEqual(gotApproval, &wantApproval) {
		t.Fatalf("approval = %#v, want %#v", gotApproval, &wantApproval)
	}
}

func TestPermissionDecodeFallsBackFromMissingToolName(t *testing.T) {
	t.Parallel()

	kind := eventstream.ToolKindExecute
	approval, err := acppermission.DecodePermissionRequest(eventstream.RequestPermissionRequest{
		SessionID: "session-1",
		ToolCall: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Kind:          &kind,
		},
	})
	if err != nil {
		t.Fatalf("DecodePermissionRequest() error = %v", err)
	}
	if approval.ToolCall.Name != eventstream.ToolKindExecute {
		t.Fatalf("tool name = %q, want kind fallback %q", approval.ToolCall.Name, eventstream.ToolKindExecute)
	}
}

func TestPermissionDecodeRejectsNoncanonicalOrAmbiguousOptions(t *testing.T) {
	t.Parallel()

	tests := map[string][]acpsdk.PermissionOption{
		"unknown kind":   {{OptionId: "allow_once", Name: "Allow", Kind: "vendor_custom"}},
		"uppercase kind": {{OptionId: "allow_once", Name: "Allow", Kind: "ALLOW_ONCE"}},
		"spaced kind":    {{OptionId: "allow_once", Name: "Allow", Kind: " allow_once "}},
		"duplicate id": {
			{OptionId: "same", Name: "Allow", Kind: acpsdk.PermissionOptionKindAllowOnce},
			{OptionId: "same", Name: "Reject", Kind: acpsdk.PermissionOptionKindRejectOnce},
		},
		"blank id":  {{OptionId: " ", Name: "Allow", Kind: acpsdk.PermissionOptionKindAllowOnce}},
		"spaced id": {{OptionId: " allow_once ", Name: "Allow", Kind: acpsdk.PermissionOptionKindAllowOnce}},
	}
	for name, options := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := acppermission.DecodePermissionRequest(eventstream.RequestPermissionRequest{
				SessionID: "session-1",
				ToolCall:  eventstream.ToolCallUpdate{ToolCallID: "call-1"},
				Options:   options,
			})
			if err == nil {
				t.Fatal("DecodePermissionRequest() error = nil, want fail-closed validation")
			}
		})
	}
}

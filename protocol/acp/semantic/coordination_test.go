package semantic_test

import (
	"encoding/json"
	"reflect"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/protocol/acp/semantic"
)

func TestPermissionWireRoundTripPreservesSDKSemantics(t *testing.T) {
	t.Parallel()

	wantRef := session.SessionRef{SessionID: "session-1"}
	wantApproval := session.ProtocolApproval{
		ToolCall: session.ProtocolToolCall{
			ID:       "call-1",
			Name:     "RUN_COMMAND",
			Kind:     schema.ToolKindExecute,
			Title:    "Run command",
			Status:   schema.ToolStatusPending,
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

	wire, err := semantic.EncodePermissionRequest(wantRef, &wantApproval, wantMeta)
	if err != nil {
		t.Fatalf("EncodePermissionRequest() error = %v", err)
	}
	if got := metautil.String(wire.ToolCall.Meta, metautil.Root, metautil.Runtime, metautil.RuntimeTool, metautil.RuntimeToolName); got != wantApproval.ToolCall.Name {
		t.Fatalf("wire tool name = %q, want %q", got, wantApproval.ToolCall.Name)
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var external schema.RequestPermissionRequest
	if err := json.Unmarshal(raw, &external); err != nil {
		t.Fatal(err)
	}
	gotRef, gotApproval, gotMeta, err := semantic.DecodePermissionRequest(external)
	if err != nil {
		t.Fatalf("DecodePermissionRequest() error = %v", err)
	}
	if gotRef != wantRef {
		t.Fatalf("session ref = %#v, want %#v", gotRef, wantRef)
	}
	if !reflect.DeepEqual(gotApproval, &wantApproval) {
		t.Fatalf("approval = %#v, want %#v", gotApproval, &wantApproval)
	}
	if !reflect.DeepEqual(gotMeta, wantMeta) {
		t.Fatalf("meta = %#v, want %#v", gotMeta, wantMeta)
	}
}

func TestPermissionResponseUsesSharedAllowSemantics(t *testing.T) {
	t.Parallel()

	approval := &session.ProtocolApproval{Options: []session.ProtocolApprovalOption{
		{ID: "allow_once", Kind: "allow_once"},
		{ID: "reject_once", Kind: "reject_once"},
	}}
	tests := []struct {
		name string
		wire schema.RequestPermissionResponse
		want agent.ApprovalResponse
	}{
		{
			name: "allow selection",
			wire: schema.RequestPermissionResponse{Outcome: schema.PermissionOutcome{Outcome: "selected", OptionID: "allow_once"}},
			want: agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true},
		},
		{
			name: "reject selection",
			wire: schema.RequestPermissionResponse{Outcome: schema.PermissionOutcome{Outcome: "selected", OptionID: "reject_once"}},
			want: agent.ApprovalResponse{Outcome: "selected", OptionID: "reject_once"},
		},
		{
			name: "cancelled outcome cannot approve",
			wire: schema.RequestPermissionResponse{Outcome: schema.PermissionOutcome{Outcome: "cancelled", OptionID: "allow_once"}},
			want: agent.ApprovalResponse{Outcome: "cancelled", OptionID: "allow_once"},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := semantic.DecodePermissionResponse(test.wire, approval); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("DecodePermissionResponse() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestPermissionDecodeFallsBackFromMissingToolName(t *testing.T) {
	t.Parallel()

	kind := schema.ToolKindExecute
	_, approval, _, err := semantic.DecodePermissionRequest(schema.RequestPermissionRequest{
		SessionID: "session-1",
		ToolCall: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Kind:          &kind,
		},
	})
	if err != nil {
		t.Fatalf("DecodePermissionRequest() error = %v", err)
	}
	if approval.ToolCall.Name != schema.ToolKindExecute {
		t.Fatalf("tool name = %q, want kind fallback %q", approval.ToolCall.Name, schema.ToolKindExecute)
	}
}

func TestCancelWireRoundTripPreservesSDKSessionIdentity(t *testing.T) {
	t.Parallel()

	want := session.SessionRef{SessionID: "session-1"}
	wire := semantic.EncodeCancelNotification(want)
	if got := semantic.DecodeCancelNotification(wire); got != want {
		t.Fatalf("cancel round trip = %#v, want %#v", got, want)
	}
}

func TestParticipantAndHandoffWireRoundTripPreservesSDKSemantics(t *testing.T) {
	t.Parallel()

	participant := session.ProtocolParticipant{Action: "attached"}
	participantWire := jsonRoundTripProtocol(t, semantic.EncodeParticipant(participant))
	gotParticipant, err := semantic.DecodeParticipant(participantWire)
	if err != nil {
		t.Fatalf("DecodeParticipant() error = %v", err)
	}
	if gotParticipant != participant {
		t.Fatalf("participant = %#v, want %#v", gotParticipant, participant)
	}

	handoff := session.ProtocolHandoff{Phase: "committed"}
	handoffWire := jsonRoundTripProtocol(t, semantic.EncodeHandoff(handoff))
	gotHandoff, err := semantic.DecodeHandoff(handoffWire)
	if err != nil {
		t.Fatalf("DecodeHandoff() error = %v", err)
	}
	if gotHandoff != handoff {
		t.Fatalf("handoff = %#v, want %#v", gotHandoff, handoff)
	}
}

func TestParticipantAndHandoffRejectWrongMethods(t *testing.T) {
	t.Parallel()

	wrong := session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{SessionUpdate: "attached"}}
	if _, err := semantic.DecodeParticipant(wrong); err == nil {
		t.Fatal("DecodeParticipant() error = nil, want method rejection")
	}
	if _, err := semantic.DecodeHandoff(wrong); err == nil {
		t.Fatal("DecodeHandoff() error = nil, want method rejection")
	}
}

func jsonRoundTripProtocol(t *testing.T, in session.EventProtocol) session.EventProtocol {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out session.EventProtocol
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

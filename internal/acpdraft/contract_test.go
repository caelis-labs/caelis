package acpdraft

import (
	"encoding/json"
	"testing"

	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
)

func TestConditionalPromptGuardRequiresExactlyOneCondition(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		raw        string
		activityID string
		idleOnly   bool
		invalid    bool
	}{
		{name: "active", raw: `{"activityId":" turn "}`, activityID: "turn"},
		{name: "idle", raw: `{"idleOnly":true}`, idleOnly: true},
		{name: "both", raw: `{"activityId":"turn","idleOnly":true}`, invalid: true},
		{name: "neither", raw: `{}`, invalid: true},
		{name: "blank", raw: `{"activityId":" "}`, invalid: true},
		{name: "null", raw: `null`, invalid: true},
		{name: "wrongType", raw: `{"idleOnly":"true"}`, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			guard, err := ParseActiveOnly(map[string]json.RawMessage{ActiveOnlyMetaKey: json.RawMessage(test.raw)})
			if test.invalid {
				if err == nil {
					t.Fatal("invalid condition accepted")
				}
				return
			}
			if err != nil || guard.ActivityID != test.activityID || guard.IdleOnly != test.idleOnly {
				t.Fatalf("guard=%+v err=%v", guard, err)
			}
		})
	}
	if guard, err := ParseActiveOnly(nil); err != nil || guard != nil {
		t.Fatalf("unguarded standard prompt: %+v %v", guard, err)
	}
	if guard, err := ParseActiveOnly(IdleOnlyMeta()); err != nil || !guard.IdleOnly {
		t.Fatalf("idle helper: %+v %v", guard, err)
	}
}

func TestPeerRequiresAtomicIdleAndActivePromptConditions(t *testing.T) {
	t.Parallel()
	response := v2.InitializeResponse{ProtocolVersion: 2, Meta: InitializeMeta(), Capabilities: v2.AgentCapabilities{Session: &v2.SessionCapabilities{}}}
	if err := ValidatePeer(response); err != nil {
		t.Fatal(err)
	}
	response.Meta[ConditionalPromptMetaKey] = json.RawMessage(`{"version":1,"activeOnly":true}`)
	if err := ValidatePeer(response); err == nil {
		t.Fatal("peer lacking atomic idle-only admission accepted")
	}
	response.Meta = InitializeMetaWithIdleOnly(false)
	if err := ValidatePeer(response); err == nil {
		t.Fatal("standard experimental peer incorrectly satisfies controlled profile")
	}
}

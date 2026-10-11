// Package acpdraft owns the exact experimental ACP wire profile shared by
// controlled Caelis links. It carries no Session or permission authority.
package acpdraft

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
)

const (
	SchemaTag                = v2.SchemaTag
	SchemaMetaKey            = "caelis/acp_schema"
	ConditionalPromptMetaKey = "caelis/conditional_prompt"
	ActivityMetaKey          = "caelis/activity_id"
	ActiveOnlyMetaKey        = "caelis/active_only"
)

type conditionalCapability struct {
	Version    int  `json:"version"`
	ActiveOnly bool `json:"activeOnly"`
	IdleOnly   bool `json:"idleOnly"`
}

// ActiveOnlyGuard selects either insertion into an identified existing Activity
// or admission while idle. Neither guard authorizes replacement work after its
// condition changes. The wire key is retained within this unpublished profile.
type ActiveOnlyGuard struct {
	ActivityID string `json:"activityId,omitempty"`
	IdleOnly   bool   `json:"idleOnly,omitempty"`
}

// InitializeMeta advertises the pinned draft and the conditional prompt profile.
func InitializeMeta() map[string]json.RawMessage {
	return InitializeMetaWithIdleOnly(true)
}

// InitializeMetaWithIdleOnly advertises whether the actual Session owner can
// atomically reject initial work while busy. An implementation without this
// guarantee can still serve standard experimental v2, but cannot satisfy the
// controlled Caelis profile required by ValidatePeer.
func InitializeMetaWithIdleOnly(idleOnly bool) map[string]json.RawMessage {
	schema, _ := json.Marshal(SchemaTag)
	capability, _ := json.Marshal(conditionalCapability{Version: 1, ActiveOnly: true, IdleOnly: idleOnly})
	return map[string]json.RawMessage{SchemaMetaKey: schema, ConditionalPromptMetaKey: capability}
}

// ValidateInitialize rejects incompatible explicit draft requests before work.
func ValidateInitialize(request v2.InitializeRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if request.ProtocolVersion != v2.ProtocolVersionNumber {
		return fmt.Errorf("ACP draft requires protocol version 2")
	}
	var tag string
	if json.Unmarshal(request.Meta[SchemaMetaKey], &tag) != nil || tag != SchemaTag {
		return errors.New("incompatible ACP draft schema")
	}
	return nil
}

// ValidatePeer requires exact schema and conditional prompt support. A stable
// peer is a separate configured link, never a fallback for this profile.
func ValidatePeer(response v2.InitializeResponse) error {
	if response.ProtocolVersion != v2.ProtocolVersionNumber {
		return errors.New("ACP draft peer did not negotiate v2")
	}
	var tag string
	if json.Unmarshal(response.Meta[SchemaMetaKey], &tag) != nil || tag != SchemaTag {
		return errors.New("ACP draft peer schema mismatch")
	}
	var capability conditionalCapability
	if json.Unmarshal(response.Meta[ConditionalPromptMetaKey], &capability) != nil || capability.Version != 1 || !capability.ActiveOnly {
		return errors.New("ACP draft peer lacks conditional prompt support")
	}
	if !capability.IdleOnly {
		return errors.New("ACP draft peer lacks atomic idle-only prompt admission")
	}
	if response.Capabilities.Session == nil {
		return errors.New("ACP draft peer does not advertise Session support")
	}
	return nil
}

// ActivityMeta supplies the real owner-assigned Activity identity on state
// updates and insertion receipts. A receipt must never infer identity from the
// first running notification: another caller may own that Activity.
func ActivityMeta(activityID string) map[string]json.RawMessage {
	value, _ := json.Marshal(activityID)
	return map[string]json.RawMessage{ActivityMetaKey: value}
}

// ActivityID reads only this profile's recognized Activity identity.
func ActivityID(meta map[string]json.RawMessage) string {
	var value string
	if json.Unmarshal(meta[ActivityMetaKey], &value) != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

// ActiveOnlyMeta asks the peer to insert into one still-active Activity.
func ActiveOnlyMeta(activityID string) map[string]json.RawMessage {
	value, _ := json.Marshal(ActiveOnlyGuard{ActivityID: activityID})
	return map[string]json.RawMessage{ActiveOnlyMetaKey: value}
}

// IdleOnlyMeta asks the actual Session owner to admit a new foreground Activity
// only while idle, atomically with admission. Cached client state is not proof.
func IdleOnlyMeta() map[string]json.RawMessage {
	value, _ := json.Marshal(ActiveOnlyGuard{IdleOnly: true})
	return map[string]json.RawMessage{ActiveOnlyMetaKey: value}
}

// ParseActiveOnly distinguishes absent guards from malformed known metadata
// and rejects conflicting idle-only and active-only conditions.
func ParseActiveOnly(meta map[string]json.RawMessage) (*ActiveOnlyGuard, error) {
	raw, present := meta[ActiveOnlyMetaKey]
	if !present {
		return nil, nil
	}
	var guard ActiveOnlyGuard
	if json.Unmarshal(raw, &guard) != nil {
		return nil, errors.New("invalid ACP conditional prompt guard")
	}
	guard.ActivityID = strings.TrimSpace(guard.ActivityID)
	if (guard.ActivityID != "") == guard.IdleOnly {
		return nil, errors.New("invalid ACP conditional prompt guard")
	}
	return &guard, nil
}

// InactivePromptError proves rejection before insertion or replacement work.
func InactivePromptError() *acp.RequestError {
	return acp.NewInvalidRequest(map[string]any{ConditionalPromptMetaKey: map[string]any{"outcome": "promptRequired"}})
}

// IsInactivePromptError recognizes only the profile's explicit no-effect result.
func IsInactivePromptError(err error) bool {
	var requestError *acp.RequestError
	if !errors.As(err, &requestError) || requestError.Code != -32600 {
		return false
	}
	raw, marshalErr := json.Marshal(requestError.Data)
	if marshalErr != nil {
		return false
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(raw, &data) != nil {
		return false
	}
	var result struct {
		Outcome string `json:"outcome"`
	}
	return json.Unmarshal(data[ConditionalPromptMetaKey], &result) == nil && result.Outcome == "promptRequired"
}

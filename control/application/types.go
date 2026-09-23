// Package application owns application credentials, explicit execution profiles,
// callback dispatch receipts, and execution resources. Canonical execution and
// transcript facts remain with the Session Runtime.
package application

import (
	"bytes"
	"encoding/json"
	"time"
)

// Capability identifies the supported application contract in Host discovery.
const Capability = "application-runtime-v1"

// StateKey locates the application marker in canonical Session state.
const StateKey = "control_application_v1"

// MetadataKind identifies application-owned Sessions to Host projection.
const MetadataKind = "application"

// Scope is authenticated adapter context, never model or request-body authority.
type Scope struct {
	PrincipalID   string `json:"principal_id"`
	ApplicationID string `json:"application_id"`
	ConnectionID  string `json:"connection_id"`
}

// Profile is a complete execution snapshot. Binding.Profile is the immutable
// creation snapshot; Configuration.Profile is the revisioned desired snapshot.
type Profile struct {
	Version         string           `json:"version"`
	Instructions    string           `json:"instructions"`
	Model           string           `json:"model"`
	ReasoningEffort string           `json:"reasoning_effort,omitempty"`
	ServiceTier     string           `json:"service_tier,omitempty"`
	ToolsVersion    string           `json:"tools_version"`
	Tools           []ToolDefinition `json:"tools,omitempty"`
	NativeTools     []string         `json:"native_tools,omitempty"`
	Execution       string           `json:"execution"` // tools-only or workspace-write
	Inherit         Inheritance      `json:"inherit"`
	Workspace       Workspace        `json:"workspace,omitempty"`
	Permissions     Permissions      `json:"permissions,omitempty"`
}

// MarshalJSON keeps the original creation-profile field order and omission
// exactly when new selectors are absent. Permanent creation operation digests
// use these bytes. A nonnil empty native_tools is emitted as [] (not omitted):
// omission selects defaults while [] clears the native tool set.
func (p Profile) MarshalJSON() ([]byte, error) {
	type alias Profile
	var native *[]string
	if p.NativeTools != nil {
		native = &p.NativeTools
	}
	var workspace *Workspace
	if p.Workspace.CWD != "" || len(p.Workspace.Access) != 0 {
		workspace = &p.Workspace
	}
	var permissions *Permissions
	if p.Permissions != (Permissions{}) {
		permissions = &p.Permissions
	}
	return json.Marshal(struct {
		alias
		NativeTools *[]string    `json:"native_tools,omitempty"`
		Workspace   *Workspace   `json:"workspace,omitempty"`
		Permissions *Permissions `json:"permissions,omitempty"`
	}{alias: alias(p), NativeTools: native, Workspace: workspace, Permissions: permissions})
}

// UnmarshalJSON rejects native_tools:null: treating it as absence would grant
// the default native tool set rather than the explicit empty selection.
func (p *Profile) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil || string(fields["native_tools"]) == "null" {
		return ErrInvalid
	}
	type alias Profile
	var value alias
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*p = Profile(value)
	return nil
}

// RequestConfiguration identifies the latest request admitted under a revision.
type RequestConfiguration struct {
	Revision        uint64 `json:"revision"`
	RequestID       string `json:"request_id"`
	TurnID          string `json:"turn_id"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	ServiceTier     string `json:"service_tier,omitempty"`
	ToolsVersion    string `json:"tools_version,omitempty"`
}

// Configuration is the revisioned desired execution snapshot, independently of
// the immutable creation Binding and independently of admitted request history.
type Configuration struct {
	SessionID   string                `json:"session_id"`
	Revision    uint64                `json:"revision"`
	Profile     Profile               `json:"profile"`
	LastRequest *RequestConfiguration `json:"last_request,omitempty"`
}

// ConfigurationPatch changes only present hot-config fields. Nil preserves the
// previous field; explicit JSON null is invalid. Empty tool lists clear catalogs.
type ConfigurationPatch struct {
	Instructions    *string           `json:"instructions,omitempty"`
	Model           *string           `json:"model,omitempty"`
	ReasoningEffort *string           `json:"reasoning_effort,omitempty"`
	ServiceTier     *string           `json:"service_tier,omitempty"`
	ToolsVersion    *string           `json:"tools_version,omitempty"`
	Tools           *[]ToolDefinition `json:"tools,omitempty"`
	NativeTools     *[]string         `json:"native_tools,omitempty"`
}

// UnmarshalJSON rejects null fields rather than treating them as absence.
func (p *ConfigurationPatch) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return ErrInvalid
	}
	for key, value := range fields {
		switch key {
		case "instructions", "model", "reasoning_effort", "service_tier", "tools_version", "tools", "native_tools":
		default:
			return ErrInvalid
		}
		if string(value) == "null" {
			return ErrInvalid
		}
	}
	type alias ConfigurationPatch
	var result alias
	if err := json.Unmarshal(data, &result); err != nil {
		return ErrInvalid
	}
	*p = ConfigurationPatch(result)
	return nil
}

// UpdateConfigurationRequest atomically commits a compare-and-swap update and
// immutable operation receipt. Retries with the same ID return the original result.
type UpdateConfigurationRequest struct {
	OperationID                   string             `json:"operation_id"`
	ExpectedConfigurationRevision uint64             `json:"expected_configuration_revision"`
	Patch                         ConfigurationPatch `json:"patch"`
}

// Workspace selects a persistent application working directory and additional
// access directories. Empty CWD selects the allocated Session workspace;
// directory selection never enables context inheritance.
type Workspace struct {
	CWD    string            `json:"cwd,omitempty"`
	Access []WorkspaceAccess `json:"access,omitempty"`
}

// WorkspaceAccess names an additional directory. Read-only grants no write
// access; ordinary workspace-write permits ambient filesystem reads, so this
// is not a hard read-isolation boundary.
type WorkspaceAccess struct {
	Path string `json:"path"`
	Mode string `json:"mode"` // read-only or read-write
}

// Permissions selects native policy and approval behavior independently of
// model/instruction/tool settings. Empty values default to workspace-write
// policy and manual approval.
type Permissions struct {
	Mode         string `json:"mode,omitempty"`
	ApprovalMode string `json:"approval_mode,omitempty"`
}

// Inheritance makes omitted configuration authority explicit. Version 1 rejects
// true values; Host configuration never implicitly enters an application Session.
type Inheritance struct {
	CWDInstructions bool `json:"cwd_instructions"`
	MCP             bool `json:"mcp"`
	Skills          bool `json:"skills"`
	WorkspaceMemory bool `json:"workspace_memory"`
}

// ToolDefinition describes an application-owned callback, not a native tool.
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// Registration enrolls one application using an externally persisted random
// credential. Only an authenticated Host principal may enroll. Credential is
// hashed at rest and must start with app-client- followed by 64 hex digits.
type Registration struct {
	OperationID string `json:"operation_id"`
	Name        string `json:"name"`
	Credential  string `json:"credential"`
}

// Connection is a revocable tool-host lease. Detaching a UI does not revoke it.
type Connection struct {
	Scope
	Name      string    `json:"name"`
	ExpiresAt time.Time `json:"expires_at"`
	Revoked   bool      `json:"revoked"`
}

// Binding is the immutable, server-created ownership record for a native Session.
type Binding struct {
	Scope
	SessionID      string  `json:"session_id"`
	Profile        Profile `json:"profile"`
	CreationDigest string  `json:"creation_digest"`
	Archived       bool    `json:"archived"`
}

// Source distinguishes user input, non-authorizing evidence, and admitted
// background work. AuthorizedSource is copied from a durable grant by Control,
// never from a caller-supplied prompt or model-generated summary.
type Source struct {
	Kind             string `json:"kind"` // user, application_summary, external_material, authorized_background
	OperationID      string `json:"operation_id"`
	GrantID          string `json:"grant_id,omitempty"`
	AuthorizedSource string `json:"authorized_source,omitempty"`
}

// CallContext is native provenance bound by the Runtime, not callback arguments.
type CallContext struct {
	Scope
	SessionID             string `json:"session_id"`
	TurnID                string `json:"turn_id"`
	ItemID                string `json:"item_id"`
	CallID                string `json:"call_id"`
	ToolsVersion          string `json:"tools_version"`
	ConfigurationRevision uint64 `json:"configuration_revision"`
	Source                Source `json:"source"`
}

// Call is a durable intent. Claiming is at most once: an unacknowledged claimed
// effect remains unknown, never automatically reissued on reconnect or restart.
type Call struct {
	// ID is the durable callback receipt address, independent of provider CallID reuse.
	ID string `json:"id"`
	CallContext
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	State     string          `json:"state"` // pending, claimed, completed, cancelled, unknown
	Result    *CallResult     `json:"result,omitempty"`
}

// CallResult reports an application's knowledge of its effect, not inferred prose.
type CallResult struct {
	Outcome string          `json:"outcome"` // succeeded, failed, unknown
	Content json.RawMessage `json:"content"`
}

// Resource is an immutable owned byte snapshot, independent of worker paths.
type Resource struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
}

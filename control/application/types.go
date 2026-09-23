// Package application owns application credentials, explicit execution profiles,
// callback dispatch receipts, and execution resources. Canonical execution and
// transcript facts remain with the Session Runtime.
package application

import (
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

// Profile is immutable for the lifetime of one Session. A different profile
// requires a new Session; no in-flight model prefix or tool schema is rewritten.
type Profile struct {
	Version      string           `json:"version"`
	Instructions string           `json:"instructions"`
	Model        string           `json:"model"`
	ToolsVersion string           `json:"tools_version"`
	Tools        []ToolDefinition `json:"tools,omitempty"`
	Execution    string           `json:"execution"` // tools-only or workspace-write
	Inherit      Inheritance      `json:"inherit"`
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

// Source distinguishes authority-bearing authenticated input from evidence.
// Background triggers require grants and are not supported by version 1.
type Source struct {
	Kind        string `json:"kind"` // user, application_summary, external_material
	OperationID string `json:"operation_id"`
}

// CallContext is native provenance bound by the Runtime, not callback arguments.
type CallContext struct {
	Scope
	SessionID    string `json:"session_id"`
	TurnID       string `json:"turn_id"`
	ItemID       string `json:"item_id"`
	CallID       string `json:"call_id"`
	ToolsVersion string `json:"tools_version"`
	Source       Source `json:"source"`
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

package tool

import (
	"context"

	"github.com/OnslaughtSnail/caelis/sandbox"
)

// Definition describes a tool's identity and parameter schema.
type Definition struct {
	Name        string
	Description string
	Schema      Schema
	Metadata    map[string]any
}

// Schema describes the expected parameters for a tool call.
type Schema struct {
	Type        string
	Properties  map[string]Schema
	Required    []string
	Items       *Schema
	Enum        []any
	Format      string
	Description string
}

// Call represents an invocation of a tool.
type Call struct {
	CallID string
	Name   string
	Args   map[string]any

	// Metadata carries cross-wrapper data, such as policy decisions.
	// Nil-safe: wrappers must check before reading.
	Metadata map[string]any

	// Observer receives transient tool events (live output, progress).
	// Observer events are UI-only and must not be persisted to model context.
	Observer Observer
}

// Result is the outcome of a tool execution.
type Result struct {
	Output    string
	Parts     []ResultPart
	IsError   bool
	Truncated bool
	Metadata  map[string]any
}

// ResultPart is a content part in a tool result.
type ResultPart struct {
	Kind     string
	Text     string
	MIMEType string
	Data     []byte
	URI      string
}

// Context provides runtime information available to a tool during execution.
type Context interface {
	// Context returns the Go context.
	context.Context

	// SessionRef returns the current session reference.
	SessionRef() string

	// InvocationID returns the current invocation identifier.
	InvocationID() string

	// AgentName returns the name of the agent running the tool.
	AgentName() string

	// FileSystem returns the sandboxed filesystem, or nil if not available.
	FileSystem() sandbox.FileSystem
}

// CloneCall returns a deep copy of the call, preserving Metadata and Observer.
func CloneCall(c Call) Call {
	cp := c
	if c.Args != nil {
		cp.Args = make(map[string]any, len(c.Args))
		for k, v := range c.Args {
			cp.Args[k] = v
		}
	}
	if c.Metadata != nil {
		cp.Metadata = make(map[string]any, len(c.Metadata))
		for k, v := range c.Metadata {
			cp.Metadata[k] = v
		}
	}
	// Observer is an interface — shallow copy (same reference).
	return cp
}

// WithMetadata returns a copy of the call with the given key-value set in Metadata.
func (c Call) WithMetadata(key string, value any) Call {
	cp := CloneCall(c)
	if cp.Metadata == nil {
		cp.Metadata = make(map[string]any)
	}
	cp.Metadata[key] = value
	return cp
}

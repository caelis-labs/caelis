// Package prompt defines the public prompt assembly port.
package prompt

import (
	"context"

	"github.com/OnslaughtSnail/caelis/ports/skill"
)

type Assembler interface {
	Assemble(context.Context, Request) (Result, error)
}

type FragmentProvider interface {
	Fragments(context.Context, Request) ([]Fragment, error)
}

type Request struct {
	WorkspaceDir string
	SessionState map[string]any
	UserOverride string
	Skills       []skill.Meta
	Agents       []AgentMeta
	Runtime      RuntimeFacts
}

type Result struct {
	SystemText string
	Metadata   map[string]any
	Fragments  []Fragment
}

type Fragment struct {
	ID       string
	Title    string
	Priority int
	Text     string
	Metadata map[string]any
}

type AgentMeta struct {
	Name        string
	Description string
}

type RuntimeFacts struct {
	ModelAlias    string
	PolicyMode    string
	SandboxMode   string
	ApprovalMode  string
	WorkspaceRoot string
}

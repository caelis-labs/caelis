// Package endpoint defines the late-bound ACP process seam consumed by the
// bridge. Product Hosts may resolve a durable logical endpoint into an
// ephemeral process without exposing Host credentials to Control state.
package endpoint

import (
	"context"
	"errors"
	"io"
	"strings"
)

// Request is the complete scoped input for one endpoint resolution.
type Request struct {
	AdapterID    string
	ConnectionID string
	CWD          string
	// ACPDraftSchema selects one explicitly supported experimental wire profile.
	ACPDraftSchema string
	// ModelImageInput is the frozen native model's read-only capability, when
	// known. Local endpoint presentation must not substitute a parent's model.
	ModelImageInput *bool
}

// Process is one ephemeral stdio ACP process declaration. Release removes
// any short-lived launch material after the process no longer needs it.
type Process struct {
	// Open supplies an owned local transport instead of launching a process.
	// Exactly one of Open and Command must be supplied.
	Open    func(context.Context) (Stream, error)
	Command string
	Args    []string
	Env     map[string]string
	WorkDir string
	Release func()
}

// Stream is an owned ACP channel. Close must stop both directions; Wait joins
// the server after its input is closed. Neither function closes durable Sessions.
type Stream struct {
	Input  io.WriteCloser
	Output io.ReadCloser
	Close  func() error
	Wait   func(context.Context) error
}

// Resolver is implemented by the product Host composition for logical,
// Host-managed endpoints. The reusable ACP bridge depends only on this seam.
type Resolver interface {
	ResolveACPProcess(context.Context, Request) (Process, error)
}

// Resolve validates and resolves one hosted request.
func Resolve(ctx context.Context, resolver Resolver, request Request) (Process, error) {
	request.AdapterID = strings.ToLower(strings.TrimSpace(request.AdapterID))
	request.ConnectionID = strings.TrimSpace(request.ConnectionID)
	request.CWD = strings.TrimSpace(request.CWD)
	if request.AdapterID == "" || request.ConnectionID == "" || request.CWD == "" {
		return Process{}, errors.New("acp endpoint: adapter, connection, and cwd are required")
	}
	if resolver == nil {
		return Process{}, errors.New("acp endpoint: hosted endpoint resolver is unavailable")
	}
	process, err := resolver.ResolveACPProcess(ctx, request)
	if err != nil {
		return Process{}, err
	}
	process.Command = strings.TrimSpace(process.Command)
	process.WorkDir = strings.TrimSpace(process.WorkDir)
	process.Args = append([]string(nil), process.Args...)
	if (process.Command == "") == (process.Open == nil) {
		if process.Release != nil {
			process.Release()
		}
		return Process{}, errors.New("acp endpoint: resolver must supply exactly one process or local stream")
	}
	return process, nil
}

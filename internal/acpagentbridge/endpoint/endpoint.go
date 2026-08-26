// Package endpoint defines the late-bound ACP process seam consumed by the
// bridge. Product Hosts may resolve a durable logical endpoint into an
// ephemeral process without exposing Host credentials to Control state.
package endpoint

import (
	"context"
	"errors"
	"strings"
)

// Request is the complete scoped input for one endpoint resolution.
type Request struct {
	AdapterID    string
	ConnectionID string
	CWD          string
}

// Process is one ephemeral stdio ACP process declaration. Release removes
// any short-lived launch material after the process no longer needs it.
type Process struct {
	Command string
	Args    []string
	Env     map[string]string
	WorkDir string
	Release func()
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
	if process.Command == "" {
		if process.Release != nil {
			process.Release()
		}
		return Process{}, errors.New("acp endpoint: resolver returned an empty command")
	}
	return process, nil
}

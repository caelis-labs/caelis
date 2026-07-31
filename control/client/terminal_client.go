package controlclient

import (
	"context"
	"errors"
	"strings"
)

type TerminalRequest struct {
	SessionID  string `json:"session_id"`
	TerminalID string `json:"terminal_id"`
}

type TerminalExitStatus struct {
	ExitCode *int    `json:"exit_code,omitempty"`
	Signal   *string `json:"signal,omitempty"`
}

type TerminalOutput struct {
	Output     string              `json:"output"`
	Truncated  bool                `json:"truncated"`
	ExitStatus *TerminalExitStatus `json:"exit_status,omitempty"`
}

// TerminalService owns authorized terminal RPC over the Session-addressed
// Runtime stream selected by AppServer.
type TerminalService interface {
	TerminalOutput(context.Context, Principal, TerminalRequest) (TerminalOutput, error)
	WaitTerminal(context.Context, Principal, TerminalRequest) (TerminalExitStatus, error)
	KillTerminal(context.Context, Principal, TerminalRequest) error
	ReleaseTerminal(context.Context, Principal, TerminalRequest) error
}

type TerminalClient interface {
	TerminalOutput(context.Context, TerminalRequest) (TerminalOutput, error)
	WaitTerminal(context.Context, TerminalRequest) (TerminalExitStatus, error)
	KillTerminal(context.Context, TerminalRequest) error
	ReleaseTerminal(context.Context, TerminalRequest) error
}

type boundTerminalClient struct {
	service   TerminalService
	principal Principal
}

func BindTerminalClient(service TerminalService, principal Principal) (TerminalClient, error) {
	if service == nil {
		return nil, errors.New("controlclient: terminal service is required")
	}
	principal.ID = strings.TrimSpace(principal.ID)
	if principal.ID == "" {
		return nil, errors.New("controlclient: principal ID is required")
	}
	principal.Roles = append([]string(nil), principal.Roles...)
	return &boundTerminalClient{service: service, principal: principal}, nil
}

func (c *boundTerminalClient) principalCopy() Principal {
	principal := c.principal
	principal.Roles = append([]string(nil), principal.Roles...)
	return principal
}

func (c *boundTerminalClient) TerminalOutput(ctx context.Context, req TerminalRequest) (TerminalOutput, error) {
	return c.service.TerminalOutput(ctx, c.principalCopy(), req)
}
func (c *boundTerminalClient) WaitTerminal(ctx context.Context, req TerminalRequest) (TerminalExitStatus, error) {
	return c.service.WaitTerminal(ctx, c.principalCopy(), req)
}
func (c *boundTerminalClient) KillTerminal(ctx context.Context, req TerminalRequest) error {
	return c.service.KillTerminal(ctx, c.principalCopy(), req)
}
func (c *boundTerminalClient) ReleaseTerminal(ctx context.Context, req TerminalRequest) error {
	return c.service.ReleaseTerminal(ctx, c.principalCopy(), req)
}

var _ TerminalClient = (*boundTerminalClient)(nil)

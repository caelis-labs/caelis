// Compatibility types that allow agent/remote to import only acp/client
// instead of protocol/acp/client. These re-export or alias the canonical
// acp package types plus thin wrappers needed by the remote agent.

package client

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/acp"
)

// UpdateEnvelope wraps a session update notification for streaming callbacks.
type UpdateEnvelope struct {
	SessionID string
	Update    acp.Update
}

// PermissionHandler handles session/request_permission from a remote agent.
type PermissionHandler func(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error)

// TerminalHandler handles terminal/* requests from a remote agent.
type TerminalHandler = acp.TerminalClientCallbacks

// FileSystemHandler handles fs/* requests from a remote agent.
type FileSystemHandler = acp.FileSystemClientCallbacks

// Implementation identifies the client to the remote agent.
type Implementation struct {
	Title       string `json:"title,omitempty"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
}

// Re-export response types from acp so agent/remote can use acp/client
// as its single import path.
type (
	InitializeResponse    = acp.InitializeResponse
	NewSessionResponse    = acp.NewSessionResponse
	PromptResponse        = acp.PromptResponse
	LoadSessionResponse   = acp.LoadSessionResponse
	CloseSessionResponse  = acp.CloseSessionResponse
	CancelNotification    = acp.CancelNotification
	RequestPermissionRequest  = acp.RequestPermissionRequest
	RequestPermissionResponse = acp.RequestPermissionResponse
	ToolCallUpdate        = acp.ToolCallUpdate
	PermissionOption      = acp.PermissionOption
)

// PermissionSelectedOutcome returns a RequestPermissionResponse with the
// selected option ID.
func PermissionSelectedOutcome(optionID string) acp.RequestPermissionResponse {
	return acp.PermissionSelectedOutcome(optionID)
}

// SelectPermissionOptionID picks the best permission option from the list.
// If approved is true, picks the first "allow" option; otherwise picks
// the first "reject" option.
func SelectPermissionOptionID(options []acp.PermissionOption, approved bool) string {
	for _, opt := range options {
		if approved && isAllowOption(opt.Kind) {
			return opt.OptionID
		}
		if !approved && isRejectOption(opt.Kind) {
			return opt.OptionID
		}
	}
	if len(options) > 0 {
		return options[0].OptionID
	}
	return ""
}

func isAllowOption(kind string) bool {
	return kind == "allow_once" || kind == "allow_always"
}

func isRejectOption(kind string) bool {
	return kind == "reject_once" || kind == "reject_always"
}

// ACPClient is the narrow client surface an ACP-backed agent needs.
type ACPClient interface {
	Initialize(context.Context) (acp.InitializeResponse, error)
	NewSession(context.Context, acp.NewSessionRequest) (acp.NewSessionResponse, error)
	PromptText(context.Context, string, string) (acp.PromptResponse, error)
	Cancel(context.Context, string) error
	Close() error
}

// ACPReusableClient can load an existing external ACP session for continuation.
type ACPReusableClient interface {
	LoadSession(context.Context, acp.LoadSessionRequest) (acp.LoadSessionResponse, error)
}

// ACPClientFactory creates one ACP client per agent run.
type ACPClientFactory interface {
	Start(context.Context, ACPClientCallbacks) (ACPClient, error)
}

// ACPClientCallbacks are installed so the transport can stream remote updates
// and permission requests back into Layer 4 contracts.
type ACPClientCallbacks struct {
	OnUpdate            func(UpdateEnvelope)
	OnPermissionRequest PermissionHandler
}

// ProcessFactoryConfig configures a process-backed ACP client factory.
type ProcessFactoryConfig struct {
	Command    string
	Args       []string
	Env        map[string]string
	WorkDir    string
	ClientInfo *Implementation
	Terminal   TerminalHandler
	FileSystem FileSystemHandler
}

// ProcessFactory starts an ACP agent process through acp/client.
type ProcessFactory struct {
	Config ProcessFactoryConfig
}

// Start creates a process-backed ACP client.
func (f ProcessFactory) Start(ctx context.Context, callbacks ACPClientCallbacks) (ACPClient, error) {
	envSlice := envMapToSlice(f.Config.Env)
	handlers := Handlers{
		OnUpdate: func(sn acp.SessionNotification) {
			if callbacks.OnUpdate != nil {
				callbacks.OnUpdate(UpdateEnvelope{
					SessionID: sn.SessionID,
					Update:    sn.Update,
				})
			}
		},
		OnPermissionRequest: callbacks.OnPermissionRequest,
		Terminal:            f.Config.Terminal,
		FileSystem:          f.Config.FileSystem,
	}
	c, err := Start(ctx, Config{
		Command:  strings.TrimSpace(f.Config.Command),
		Args:     append([]string(nil), f.Config.Args...),
		Env:      envSlice,
		WorkDir:  strings.TrimSpace(f.Config.WorkDir),
		Handlers: handlers,
	})
	if err != nil {
		return nil, fmt.Errorf("acp/client: start process: %w", err)
	}
	return &processFactoryClient{Client: c}, nil
}

// processFactoryClient adapts *Client to the ACPClient interface.
type processFactoryClient struct {
	*Client
}

func (c *processFactoryClient) Initialize(ctx context.Context) (acp.InitializeResponse, error) {
	return c.Client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1})
}

func (c *processFactoryClient) NewSession(ctx context.Context, req acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	return c.Client.NewSession(ctx, req)
}

func (c *processFactoryClient) PromptText(ctx context.Context, sessionID string, text string) (acp.PromptResponse, error) {
	return c.Client.PromptText(ctx, sessionID, text)
}

func envMapToSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

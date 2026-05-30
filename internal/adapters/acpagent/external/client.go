// Package external adapts an external ACP agent into core canonical events.
package external

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/jsonrpc"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

type PermissionHandler func(context.Context, schema.RequestPermissionRequest) (schema.RequestPermissionResponse, error)

type Config struct {
	AgentID        string
	AgentName      string
	Command        string
	Args           []string
	WorkDir        string
	Env            []string
	Implementation schema.Implementation
	Permission     PermissionHandler
}

type Client struct {
	cfg          Config
	conn         *jsonrpc.Conn
	events       chan session.Event
	serveErr     chan error
	serveOnce    sync.Once
	closeOnce    sync.Once
	closeProcess func() error
}

type PromptResult struct {
	StopReason string
	Events     []session.Event
}

func StartProcess(ctx context.Context, cfg Config) (*Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	command := strings.TrimSpace(cfg.Command)
	if command == "" {
		return nil, errors.New("acpagent/external: command is required")
	}
	cmd := exec.CommandContext(ctx, command, cfg.Args...)
	if strings.TrimSpace(cfg.WorkDir) != "" {
		cmd.Dir = strings.TrimSpace(cfg.WorkDir)
	}
	if len(cfg.Env) > 0 {
		cmd.Env = append(os.Environ(), cfg.Env...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	client := New(stdout, stdin, cfg)
	client.closeProcess = func() error {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return cmd.Wait()
	}
	return client, nil
}

func New(reader io.Reader, writer io.Writer, cfg Config) *Client {
	if strings.TrimSpace(cfg.AgentID) == "" {
		cfg.AgentID = firstNonEmpty(cfg.AgentName, cfg.Command, "external-acp")
	}
	if strings.TrimSpace(cfg.AgentName) == "" {
		cfg.AgentName = cfg.AgentID
	}
	return &Client{
		cfg:      cfg,
		conn:     jsonrpc.New(reader, writer),
		events:   make(chan session.Event, 64),
		serveErr: make(chan error, 1),
	}
}

func (c *Client) Start(ctx context.Context) {
	c.serveOnce.Do(func() {
		go func() {
			c.serveErr <- c.conn.Serve(ctx, c.handleRequest, c.handleNotification)
		}()
	})
}

func (c *Client) Initialize(ctx context.Context) (schema.InitializeResponse, error) {
	c.Start(ctx)
	req := schema.InitializeRequest{
		ProtocolVersion:    schema.CurrentProtocolVersion,
		ClientCapabilities: map[string]any{},
		ClientInfo:         &c.cfg.Implementation,
	}
	var resp schema.InitializeResponse
	if err := c.conn.Call(ctx, schema.MethodInitialize, req, &resp); err != nil {
		return schema.InitializeResponse{}, err
	}
	return resp, nil
}

func (c *Client) InitializeSession(ctx context.Context) error {
	_, err := c.Initialize(ctx)
	return err
}

func (c *Client) NewSession(ctx context.Context, cwd string) (schema.NewSessionResponse, error) {
	c.Start(ctx)
	var resp schema.NewSessionResponse
	if err := c.conn.Call(ctx, schema.MethodSessionNew, schema.NewSessionRequest{CWD: strings.TrimSpace(cwd)}, &resp); err != nil {
		return schema.NewSessionResponse{}, err
	}
	return resp, nil
}

func (c *Client) NewCoreSession(ctx context.Context, workspace session.Workspace) (string, error) {
	resp, err := c.NewSession(ctx, workspace.CWD)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.SessionID), nil
}

func (c *Client) Prompt(ctx context.Context, sessionID string, parts []model.ContentPart) (PromptResult, error) {
	c.Start(ctx)
	req := schema.PromptRequest{
		SessionID: strings.TrimSpace(sessionID),
		Prompt:    promptParts(parts),
	}
	done := make(chan promptCallResult, 1)
	go func() {
		var resp schema.PromptResponse
		err := c.conn.Call(ctx, schema.MethodSessionPrompt, req, &resp)
		done <- promptCallResult{response: resp, err: err}
	}()

	var events []session.Event
	for {
		select {
		case event := <-c.events:
			if strings.TrimSpace(event.SessionID) == "" || event.SessionID == req.SessionID {
				events = append(events, session.CloneEvent(event))
			}
		case result := <-done:
			for {
				select {
				case event := <-c.events:
					if strings.TrimSpace(event.SessionID) == "" || event.SessionID == req.SessionID {
						events = append(events, session.CloneEvent(event))
					}
				default:
					return PromptResult{StopReason: result.response.StopReason, Events: events}, result.err
				}
			}
		case err := <-c.serveErr:
			if err == nil || errors.Is(err, io.EOF) {
				return PromptResult{Events: events}, nil
			}
			return PromptResult{Events: events}, err
		case <-ctx.Done():
			return PromptResult{Events: events}, ctx.Err()
		}
	}
}

func (c *Client) PromptCore(ctx context.Context, sessionID string, parts []model.ContentPart) ([]session.Event, error) {
	result, err := c.Prompt(ctx, sessionID, parts)
	if err != nil {
		return nil, err
	}
	return result.Events, nil
}

func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		if c.closeProcess != nil {
			err = c.closeProcess()
		}
	})
	return err
}

type promptCallResult struct {
	response schema.PromptResponse
	err      error
}

func (c *Client) handleRequest(ctx context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
	if msg.Method != schema.MethodSessionReqPermission {
		return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
	}
	var req schema.RequestPermissionRequest
	if err := json.Unmarshal(msg.Params, &req); err != nil {
		return nil, &jsonrpc.RPCError{Code: -32602, Message: "invalid params", Data: err.Error()}
	}
	c.emit(permissionEvent(c.cfg, req, session.ApprovalPending, "", ""))
	handler := c.cfg.Permission
	if handler == nil {
		handler = allowOncePermission
	}
	resp, err := handler(ctx, req)
	if err != nil {
		return nil, &jsonrpc.RPCError{Code: -32000, Message: err.Error()}
	}
	status := session.ApprovalRejected
	if permissionApproved(resp) {
		status = session.ApprovalApproved
	}
	c.emit(permissionEvent(c.cfg, req, status, strings.TrimSpace(resp.Outcome.OptionID), strings.TrimSpace(resp.Outcome.Outcome)))
	return resp, nil
}

func (c *Client) handleNotification(_ context.Context, msg jsonrpc.Message) {
	if msg.Method != schema.MethodSessionUpdate {
		return
	}
	events, err := eventsFromSessionUpdate(c.cfg, msg.Params)
	if err != nil {
		c.emit(session.Event{
			Type:       session.EventNotice,
			Visibility: session.VisibilityCanonical,
			Time:       time.Now().UTC(),
			Actor:      actor(c.cfg),
			Message: &model.Message{
				Role:  model.RoleSystem,
				Parts: []model.Part{model.NewTextPart(err.Error())},
			},
		})
		return
	}
	for _, event := range events {
		c.emit(event)
	}
}

func (c *Client) emit(event session.Event) {
	next := session.CloneEvent(event)
	if next.Visibility == "" {
		next.Visibility = session.VisibilityCanonical
	}
	if next.Time.IsZero() {
		next.Time = time.Now().UTC()
	}
	select {
	case c.events <- next:
	default:
		c.events <- next
	}
}

func promptParts(parts []model.ContentPart) []json.RawMessage {
	if len(parts) == 0 {
		return nil
	}
	out := make([]json.RawMessage, 0, len(parts))
	for _, part := range model.CloneContentParts(parts) {
		switch part.Type {
		case model.ContentPartText:
			if strings.TrimSpace(part.Text) != "" {
				out = append(out, jsonrpc.MustMarshalRaw(schema.TextContent{Type: "text", Text: part.Text}))
			}
		case model.ContentPartImage:
			out = append(out, jsonrpc.MustMarshalRaw(map[string]any{
				"type":     "image",
				"mimeType": part.MimeType,
				"data":     part.Data,
				"uri":      part.URI,
				"name":     part.FileName,
			}))
		case model.ContentPartFile:
			out = append(out, jsonrpc.MustMarshalRaw(map[string]any{
				"type":     "file",
				"mimeType": part.MimeType,
				"uri":      part.URI,
				"name":     part.FileName,
			}))
		}
	}
	return out
}

func allowOncePermission(context.Context, schema.RequestPermissionRequest) (schema.RequestPermissionResponse, error) {
	return schema.RequestPermissionResponse{
		Outcome: schema.PermissionOutcome{
			Outcome:  schema.PermAllowOnce,
			OptionID: schema.PermAllowOnce,
		},
	}, nil
}

func permissionApproved(resp schema.RequestPermissionResponse) bool {
	outcome := strings.ToLower(strings.TrimSpace(resp.Outcome.Outcome))
	optionID := strings.ToLower(strings.TrimSpace(resp.Outcome.OptionID))
	return strings.HasPrefix(outcome, "allow") || strings.HasPrefix(optionID, "allow")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func formatDecodeError(kind string, raw json.RawMessage, err error) error {
	return fmt.Errorf("acpagent/external: decode %s: %w: %s", kind, err, strings.TrimSpace(string(raw)))
}

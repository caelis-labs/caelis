// This independent module builds a real SDK peer without Core's module graph.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
)

type agent struct{ conn *acp.AgentSideConnection }

func (*agent) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{ProtocolVersion: 1}, nil
}
func (*agent) NewSession(context.Context, acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	return acp.NewSessionResponse{SessionId: "compat"}, nil
}
func (*agent) Cancel(context.Context, acp.CancelNotification) error { return nil }
func (a *agent) Prompt(ctx context.Context, req acp.PromptRequest) (acp.PromptResponse, error) {
	if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: req.SessionId, Update: acp.UpdateAgentMessageText("before")}); err != nil {
		return acp.PromptResponse{}, err
	}
	permission, err := a.conn.RequestPermission(ctx, acp.RequestPermissionRequest{SessionId: req.SessionId, ToolCall: acp.ToolCallUpdate{ToolCallId: "tool", Title: acp.Ptr("Read")}, Options: []acp.PermissionOption{{OptionId: "original", Name: "Allow", Kind: acp.PermissionOptionKindAllowOnce}}})
	if err != nil {
		return acp.PromptResponse{}, err
	}
	if permission.Outcome.Selected == nil || permission.Outcome.Selected.OptionId != "original" {
		return acp.PromptResponse{}, errors.New("permission identity changed")
	}
	if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: req.SessionId, Update: acp.UpdateAgentMessageText("after")}); err != nil {
		return acp.PromptResponse{}, err
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

type client struct {
	mu          sync.Mutex
	before      chan struct{}
	text        strings.Builder
	permissions int
}

func (c *client) SessionUpdate(_ context.Context, notification acp.SessionNotification) error {
	if chunk := notification.Update.AgentMessageChunk; chunk != nil && chunk.Content.Text != nil {
		c.mu.Lock()
		c.text.WriteString(chunk.Content.Text.Text)
		c.mu.Unlock()
		if chunk.Content.Text.Text == "before" {
			close(c.before)
		}
	}
	return nil
}
func (c *client) RequestPermission(ctx context.Context, _ acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	select {
	case <-c.before:
	case <-ctx.Done():
		return acp.RequestPermissionResponse{}, ctx.Err()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.text.String() != "before" {
		return acp.RequestPermissionResponse{}, errors.New("notification/permission ordering changed")
	}
	c.permissions++
	return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected("original")}, nil
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if len(os.Args) != 2 {
		return errors.New("expected agent or client role")
	}
	if os.Args[1] == "agent" {
		a := &agent{}
		conn, err := acp.NewAgentSideConnectionWithOptions(a, os.Stdout, os.Stdin, acp.ConnectionOptions{})
		if err != nil {
			return err
		}
		a.conn = conn
		defer conn.Close()
		err = conn.Wait(ctx)
		if errors.Is(err, acp.ErrPeerClosed) || errors.Is(err, acp.ErrConnectionClosed) {
			return nil
		}
		return err
	}
	c := &client{before: make(chan struct{})}
	conn, err := acp.NewClientSideConnectionWithOptions(c, os.Stdout, os.Stdin, acp.ConnectionOptions{})
	if err != nil {
		return err
	}
	defer conn.Close()
	initialized, err := conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1})
	if err != nil || initialized.ProtocolVersion != 1 {
		return fmt.Errorf("initialize: %+v %w", initialized, err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	session, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
	if err != nil {
		return err
	}
	response, err := conn.Prompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("compat")}})
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if response.StopReason != acp.StopReasonEndTurn || c.text.String() != "beforeafter" || c.permissions != 1 {
		return fmt.Errorf("conversation: stop=%s text=%q permissions=%d", response.StopReason, c.text.String(), c.permissions)
	}
	fmt.Fprintln(os.Stderr, "stable-v1 conversation and permission ordering passed")
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

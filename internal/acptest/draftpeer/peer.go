// Package draftpeer supplies an actual alpha.8 SDK peer for owning-path tests.
package draftpeer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/endpoint"
	"github.com/caelis-labs/caelis/internal/acpdraft"
)

// Peer owns fixture Sessions and enforces the advertised admission guards.
// Callbacks may deliberately omit evidence to test a client's failure boundary.
type Peer struct {
	SessionID string
	OnNew     func(v2.NewSessionRequest) (v2.NewSessionResponse, error)
	OnResume  func(v2.ResumeSessionRequest) (v2.ResumeSessionResponse, error)
	OnConfig  func(v2.SetSessionConfigOptionRequest) (v2.SetSessionConfigOptionResponse, error)
	OnPrompt  func(context.Context, v2.PromptRequest, string, string) (v2.PromptResponse, error)
	OnCancel  func(context.Context, v2.CancelSessionNotification) error
	Opens     atomic.Int32
	Prompts   atomic.Int32
	Cancels   atomic.Int32
	mu        sync.Mutex
	conn      *v2.AgentSideConnection
	sessions  map[v2.SessionId]string
	active    map[v2.SessionId]string
}

// Serve attaches the real SDK dispatcher to the supplied isolated transport.
func (p *Peer) Serve(input io.Reader, output io.Writer) (*v2.AgentSideConnection, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sessions = make(map[v2.SessionId]string)
	p.active = make(map[v2.SessionId]string)
	conn, err := v2.NewAgentSideConnection(p, output, input)
	if err != nil {
		return nil, err
	}
	p.conn = conn
	return conn, nil
}

// ResolveACPProcess implements the production late-bound endpoint seam.
func (p *Peer) ResolveACPProcess(_ context.Context, req endpoint.Request) (endpoint.Process, error) {
	if req.ACPDraftSchema != acpdraft.SchemaTag {
		return endpoint.Process{}, fmt.Errorf("fixture requires exact alpha.8, got %q", req.ACPDraftSchema)
	}
	return endpoint.Process{Open: func(context.Context) (endpoint.Stream, error) {
		if p.Opens.Add(1) != 1 {
			return endpoint.Stream{}, fmt.Errorf("fixture forbids automatic reconnection/retry")
		}
		left, right := net.Pipe()
		conn, err := p.Serve(right, right)
		if err != nil {
			_ = left.Close()
			_ = right.Close()
			return endpoint.Stream{}, err
		}
		return endpoint.Stream{Input: left, Output: left, Close: func() error { _ = left.Close(); _ = right.Close(); return conn.Close() }, Wait: func(ctx context.Context) error {
			select {
			case <-conn.Done():
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}}, nil
	}}, nil
}

func (p *Peer) Initialize(_ context.Context, req v2.InitializeRequest) (v2.InitializeResponse, error) {
	if err := acpdraft.ValidateInitialize(req); err != nil {
		return v2.InitializeResponse{}, err
	}
	return v2.InitializeResponse{ProtocolVersion: 2, Info: v2.Implementation{Name: "owning-path-fixture", Version: "test"}, Capabilities: v2.AgentCapabilities{Session: &v2.SessionCapabilities{}}, Meta: acpdraft.InitializeMeta()}, nil
}
func (*Peer) LoginAuth(context.Context, v2.LoginAuthRequest) (v2.LoginAuthResponse, error) {
	return v2.LoginAuthResponse{}, acp.NewInvalidRequest("fixture has no authentication methods")
}
func (*Peer) LogoutAuth(context.Context, v2.LogoutAuthRequest) (v2.LogoutAuthResponse, error) {
	return v2.LogoutAuthResponse{}, nil
}
func (p *Peer) NewSession(_ context.Context, req v2.NewSessionRequest) (v2.NewSessionResponse, error) {
	resp := v2.NewSessionResponse{SessionId: v2.SessionId(p.SessionID)}
	var err error
	if p.OnNew != nil {
		resp, err = p.OnNew(req)
	}
	if err == nil {
		p.mu.Lock()
		p.sessions[resp.SessionId] = string(req.Cwd)
		p.mu.Unlock()
	}
	return resp, err
}
func (p *Peer) ResumeSession(_ context.Context, req v2.ResumeSessionRequest) (v2.ResumeSessionResponse, error) {
	if string(req.SessionId) != p.SessionID {
		return v2.ResumeSessionResponse{}, acp.NewInvalidParams("fixture Session not found")
	}
	resp := v2.ResumeSessionResponse{}
	var err error
	if p.OnResume != nil {
		resp, err = p.OnResume(req)
	}
	if err == nil {
		p.mu.Lock()
		p.sessions[req.SessionId] = string(req.Cwd)
		p.mu.Unlock()
	}
	return resp, err
}
func (p *Peer) ListSessions(context.Context, v2.ListSessionsRequest) (v2.ListSessionsResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	resp := v2.ListSessionsResponse{Sessions: []v2.SessionInfo{}}
	for id, cwd := range p.sessions {
		resp.Sessions = append(resp.Sessions, v2.SessionInfo{SessionId: id, Cwd: v2.AbsolutePath(cwd)})
	}
	return resp, nil
}
func (p *Peer) CloseSession(_ context.Context, req v2.CloseSessionRequest) (v2.CloseSessionResponse, error) {
	p.mu.Lock()
	delete(p.sessions, req.SessionId)
	delete(p.active, req.SessionId)
	p.mu.Unlock()
	return v2.CloseSessionResponse{}, nil
}
func (p *Peer) SetSessionConfigOption(_ context.Context, req v2.SetSessionConfigOptionRequest) (v2.SetSessionConfigOptionResponse, error) {
	if p.OnConfig != nil {
		return p.OnConfig(req)
	}
	return v2.SetSessionConfigOptionResponse{}, acp.NewInvalidParams("fixture has no Session configuration")
}
func (p *Peer) Prompt(ctx context.Context, req v2.PromptRequest) (v2.PromptResponse, error) {
	guard, err := acpdraft.ParseActiveOnly(req.Meta)
	if err != nil {
		return v2.PromptResponse{}, err
	}
	p.mu.Lock()
	if _, ok := p.sessions[req.SessionId]; !ok {
		p.mu.Unlock()
		return v2.PromptResponse{}, acp.NewInvalidParams("fixture Session not found")
	}
	activity := p.active[req.SessionId]
	if guard != nil && ((guard.IdleOnly && activity != "") || (!guard.IdleOnly && guard.ActivityID != activity)) {
		p.mu.Unlock()
		return v2.PromptResponse{}, acpdraft.InactivePromptError()
	}
	count := p.Prompts.Add(1)
	if activity == "" {
		activity = fmt.Sprintf("peer-activity-%d", count)
		p.active[req.SessionId] = activity
	}
	message := fmt.Sprintf("peer-user-%d", count)
	p.mu.Unlock()
	return p.OnPrompt(ctx, req, activity, message)
}
func (p *Peer) Cancel(ctx context.Context, req v2.CancelSessionNotification) error {
	p.mu.Lock()
	_, exists := p.sessions[req.SessionId]
	p.mu.Unlock()
	if !exists {
		return acp.NewInvalidParams("fixture Session not found")
	}
	p.Cancels.Add(1)
	if p.OnCancel != nil {
		return p.OnCancel(ctx, req)
	}
	return nil
}

// Update sends a standard variant while preserving explicit [] and null fields.
func (p *Peer) Update(ctx context.Context, id v2.SessionId, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	p.mu.Lock()
	conn := p.conn
	p.mu.Unlock()
	return conn.SessionUpdate(ctx, v2.UpdateSessionNotification{SessionId: id, Update: v2.SessionUpdate{Other: func() *json.RawMessage { value := json.RawMessage(raw); return &value }()}})
}

// State emits owner-bound standard state evidence.
func (p *Peer) State(ctx context.Context, id v2.SessionId, activity, state, reason string) error {
	if state == "idle" {
		p.mu.Lock()
		if p.active[id] == activity {
			delete(p.active, id)
		}
		p.mu.Unlock()
	}
	value := map[string]any{"sessionUpdate": "state_update", "state": state, "_meta": acpdraft.ActivityMeta(activity)}
	if state == "idle" {
		value["stopReason"] = reason
	}
	return p.Update(ctx, id, value)
}

// Echo projects the same user item identified in the insertion receipt.
func (p *Peer) Echo(ctx context.Context, id v2.SessionId, message, text string) error {
	return p.Update(ctx, id, map[string]any{"sessionUpdate": "user_message", "messageId": message, "content": []any{map[string]any{"type": "text", "text": text}}})
}

// Permission traverses the native SDK reverse request and original option IDs.
func (p *Peer) Permission(ctx context.Context, id v2.SessionId) (v2.RequestPermissionResponse, error) {
	p.mu.Lock()
	conn := p.conn
	p.mu.Unlock()
	return conn.RequestPermission(ctx, v2.RequestPermissionRequest{SessionId: id, Title: "Original permission", Options: []v2.PermissionOption{{OptionId: "native-allow", Name: "Allow once", Kind: "allow_once"}, {OptionId: "native-deny", Name: "Reject once", Kind: "reject_once"}}, Subject: &v2.RequestPermissionSubject{Command: &v2.RequestPermissionSubjectCommand{Type: "command", Command: "git status", Cwd: "/isolated/workspace"}}})
}

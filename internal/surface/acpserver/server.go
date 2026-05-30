// Package acpserver exposes the new core runtime as an ACP JSON-RPC server.
package acpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	"github.com/OnslaughtSnail/caelis/core/session"
	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
	"github.com/OnslaughtSnail/caelis/protocol/acp/jsonrpc"
	coreprojector "github.com/OnslaughtSnail/caelis/protocol/acp/projector/core"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

type Config struct {
	Engine         coreruntime.Engine
	Services       appservices.Services
	AppName        string
	UserID         string
	Implementation schema.Implementation
}

type Server struct {
	engine         coreruntime.Engine
	services       appservices.Services
	appName        string
	userID         string
	implementation schema.Implementation
	projector      coreprojector.Projector
	conn           *jsonrpc.Conn
}

func New(cfg Config) (*Server, error) {
	engine := cfg.Engine
	if engine == nil {
		engine = cfg.Services.Engine()
	}
	if engine == nil {
		return nil, errors.New("surface/acpserver: engine is required")
	}
	appName := strings.TrimSpace(cfg.AppName)
	if appName == "" {
		appName = firstNonEmpty(cfg.Services.AppName(), "caelis")
	}
	userID := strings.TrimSpace(cfg.UserID)
	if userID == "" {
		userID = firstNonEmpty(cfg.Services.UserID(), "local-user")
	}
	impl := cfg.Implementation
	if strings.TrimSpace(impl.Name) == "" {
		impl.Name = appName
	}
	if strings.TrimSpace(impl.Version) == "" {
		impl.Version = "dev"
	}
	return &Server{
		engine:         engine,
		services:       cfg.Services,
		appName:        appName,
		userID:         userID,
		implementation: impl,
	}, nil
}

func ServeStdio(ctx context.Context, cfg Config, in io.Reader, out io.Writer) error {
	server, err := New(cfg)
	if err != nil {
		return err
	}
	return server.Serve(ctx, in, out)
}

func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	if in == nil || out == nil {
		return errors.New("surface/acpserver: stdio streams are required")
	}
	conn := jsonrpc.New(in, out)
	s.conn = conn
	return conn.Serve(ctx, s.handleRequest, s.handleNotification)
}

func (s *Server) handleRequest(ctx context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
	switch msg.Method {
	case schema.MethodInitialize:
		var req schema.InitializeRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		return s.initialize(ctx, req)
	case schema.MethodSessionNew:
		var req schema.NewSessionRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		return responseOrError(s.newSession(ctx, req))
	case schema.MethodSessionList:
		var req schema.SessionListRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		return responseOrError(s.listSessions(ctx, req))
	case schema.MethodSessionLoad:
		var req schema.LoadSessionRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		return responseOrError(s.loadSession(ctx, req))
	case schema.MethodSessionResume:
		var req schema.ResumeSessionRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		return responseOrError(s.resumeSession(ctx, req))
	case schema.MethodSessionSetMode:
		var req schema.SetSessionModeRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		return responseOrError(s.setSessionMode(ctx, req))
	case schema.MethodSessionClose:
		var req schema.CloseSessionRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		return responseOrError(s.closeSession(ctx, req))
	case schema.MethodSessionSetConfig:
		var req schema.SetSessionConfigOptionRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		return responseOrError(s.setSessionConfigOption(ctx, req))
	case schema.MethodSessionSetModel:
		var req schema.SetSessionModelRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		return responseOrError(s.setSessionModel(ctx, req))
	case schema.MethodSessionPrompt:
		var req schema.PromptRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		return responseOrError(s.prompt(ctx, req))
	default:
		return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
	}
}

func (s *Server) handleNotification(ctx context.Context, msg jsonrpc.Message) {
	if msg.Method != schema.MethodSessionCancel {
		return
	}
	var req schema.CancelNotification
	if err := decodeParams(msg.Params, &req); err != nil {
		return
	}
	_ = s.engine.Interrupt(ctx, session.Ref{
		AppName:   s.appName,
		UserID:    s.userID,
		SessionID: req.SessionID,
	})
}

func (s *Server) initialize(context.Context, schema.InitializeRequest) (any, *jsonrpc.RPCError) {
	return schema.InitializeResponse{
		ProtocolVersion: schema.CurrentProtocolVersion,
		AgentCapabilities: schema.AgentCapabilities{
			LoadSession: true,
			MCPCapabilities: schema.MCPCapabilities{
				HTTP: false,
				SSE:  false,
			},
			PromptCapabilities: schema.PromptCapabilities{
				Audio:           false,
				EmbeddedContext: false,
				Image:           true,
			},
		},
		AgentInfo: &s.implementation,
	}, nil
}

func (s *Server) newSession(ctx context.Context, req schema.NewSessionRequest) (schema.NewSessionResponse, error) {
	cwd := strings.TrimSpace(req.CWD)
	active, err := s.engine.StartSession(ctx, session.StartRequest{
		AppName: s.appName,
		UserID:  s.userID,
		Workspace: session.Workspace{
			Key: workspaceKey(cwd),
			CWD: cwd,
		},
	})
	if err != nil {
		return schema.NewSessionResponse{}, err
	}
	resp := schema.NewSessionResponse{SessionID: active.SessionID}
	if err := s.applySessionMetadata(ctx, active.Ref, &resp.ConfigOptions, &resp.Models, &resp.Modes); err != nil {
		return schema.NewSessionResponse{}, err
	}
	return resp, nil
}

func (s *Server) listSessions(ctx context.Context, req schema.SessionListRequest) (schema.SessionListResponse, error) {
	cwd := strings.TrimSpace(req.CWD)
	workspace := ""
	if cwd != "" {
		workspace = workspaceKey(cwd)
	}
	page, err := s.engine.ListSessions(ctx, session.ListQuery{
		Ref: session.Ref{
			AppName:      s.appName,
			UserID:       s.userID,
			WorkspaceKey: workspace,
		},
		WorkspaceCWD: cwd,
		After:        session.Cursor(strings.TrimSpace(req.Cursor)),
	})
	if err != nil {
		return schema.SessionListResponse{}, err
	}
	out := schema.SessionListResponse{
		Sessions:   make([]schema.SessionSummary, 0, len(page.Sessions)),
		NextCursor: strings.TrimSpace(string(page.NextCursor)),
	}
	for _, item := range page.Sessions {
		out.Sessions = append(out.Sessions, schema.SessionSummary{
			SessionID: strings.TrimSpace(item.Session.SessionID),
			CWD:       strings.TrimSpace(item.Session.Workspace.CWD),
			Title:     strings.TrimSpace(item.Session.Title),
			UpdatedAt: formatACPTime(item.Session.UpdatedAt),
		})
	}
	return out, nil
}

func (s *Server) loadSession(ctx context.Context, req schema.LoadSessionRequest) (schema.LoadSessionResponse, error) {
	snapshot, err := s.loadSnapshot(ctx, req.SessionID)
	if err != nil {
		return schema.LoadSessionResponse{}, err
	}
	if err := s.publishSnapshot(ctx, snapshot); err != nil {
		return schema.LoadSessionResponse{}, err
	}
	resp := schema.LoadSessionResponse{}
	if err := s.applySessionMetadata(ctx, snapshot.Session.Ref, &resp.ConfigOptions, &resp.Models, &resp.Modes); err != nil {
		return schema.LoadSessionResponse{}, err
	}
	return resp, nil
}

func (s *Server) resumeSession(ctx context.Context, req schema.ResumeSessionRequest) (schema.ResumeSessionResponse, error) {
	snapshot, err := s.loadSnapshot(ctx, req.SessionID)
	if err != nil {
		return schema.ResumeSessionResponse{}, err
	}
	resp := schema.ResumeSessionResponse{}
	if err := s.applySessionMetadata(ctx, snapshot.Session.Ref, &resp.ConfigOptions, &resp.Models, &resp.Modes); err != nil {
		return schema.ResumeSessionResponse{}, err
	}
	return resp, nil
}

func (s *Server) setSessionMode(ctx context.Context, req schema.SetSessionModeRequest) (schema.SetSessionModeResponse, error) {
	if strings.TrimSpace(req.SessionID) == "" {
		return schema.SetSessionModeResponse{}, fmt.Errorf("surface/acpserver: session id is required")
	}
	if s.services.Engine() == nil {
		return schema.SetSessionModeResponse{}, errors.New("surface/acpserver: mode service is not configured")
	}
	if _, err := s.services.Modes().Set(ctx, s.sessionRef(req.SessionID), req.ModeID); err != nil {
		return schema.SetSessionModeResponse{}, err
	}
	return schema.SetSessionModeResponse{}, nil
}

func (s *Server) closeSession(ctx context.Context, req schema.CloseSessionRequest) (schema.CloseSessionResponse, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return schema.CloseSessionResponse{}, fmt.Errorf("surface/acpserver: session id is required")
	}
	if err := s.engine.Interrupt(ctx, s.sessionRef(sessionID)); err != nil && !errors.Is(err, coreruntime.ErrNoActiveTurn) {
		return schema.CloseSessionResponse{}, err
	}
	return schema.CloseSessionResponse{}, nil
}

func (s *Server) prompt(ctx context.Context, req schema.PromptRequest) (schema.PromptResponse, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return schema.PromptResponse{}, fmt.Errorf("surface/acpserver: session id is required")
	}
	input, parts, err := promptParts(req.Prompt)
	if err != nil {
		return schema.PromptResponse{}, err
	}
	turn, err := s.beginTurn(ctx, s.sessionRef(sessionID), input, parts)
	if err != nil {
		return schema.PromptResponse{}, err
	}
	stopReason := schema.StopReasonEndTurn
	for env := range turn.Events() {
		if env.Err != "" {
			return schema.PromptResponse{}, errors.New(env.Err)
		}
		if err := s.publishEvent(ctx, turn, env.Event); err != nil {
			return schema.PromptResponse{}, err
		}
	}
	return schema.PromptResponse{StopReason: stopReason}, nil
}

func (s *Server) beginTurn(ctx context.Context, ref session.Ref, input string, parts []model.ContentPart) (coreruntime.Turn, error) {
	if s.services.Engine() != nil {
		return s.services.Turns().Begin(ctx, appservices.BeginTurnRequest{
			SessionRef:   ref,
			Input:        input,
			ContentParts: parts,
			Surface:      "acp",
		})
	}
	return s.engine.BeginTurn(ctx, coreruntime.TurnRequest{
		SessionRef:   ref,
		Input:        input,
		ContentParts: parts,
		Surface:      "acp",
	})
}

func (s *Server) loadSnapshot(ctx context.Context, sessionID string) (session.Snapshot, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return session.Snapshot{}, fmt.Errorf("surface/acpserver: session id is required")
	}
	return s.engine.LoadSession(ctx, s.sessionRef(sessionID))
}

func (s *Server) sessionRef(sessionID string) session.Ref {
	return session.Ref{
		AppName:   s.appName,
		UserID:    s.userID,
		SessionID: sessionID,
	}
}

func (s *Server) publishSnapshot(ctx context.Context, snapshot session.Snapshot) error {
	if s.conn == nil {
		return errors.New("surface/acpserver: connection is unavailable")
	}
	for _, event := range snapshot.Events {
		notifications, err := s.projector.ProjectNotifications(event)
		if err != nil {
			return err
		}
		for _, notification := range notifications {
			if strings.TrimSpace(notification.SessionID) == "" {
				notification.SessionID = strings.TrimSpace(snapshot.Session.SessionID)
			}
			if err := s.conn.Notify(schema.MethodSessionUpdate, notification); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) publishEvent(ctx context.Context, turn coreruntime.Turn, event session.Event) error {
	if s.conn == nil {
		return errors.New("surface/acpserver: connection is unavailable")
	}
	if permission, ok, err := s.projector.ProjectPermissionRequest(event); err != nil {
		return err
	} else if ok {
		var response schema.RequestPermissionResponse
		if err := s.conn.Call(ctx, schema.MethodSessionReqPermission, permission, &response); err != nil {
			return err
		}
		if turn == nil {
			return errors.New("surface/acpserver: turn is unavailable for permission response")
		}
		return turn.Submit(ctx, coreruntime.Submission{
			Kind: coreruntime.SubmissionApproval,
			Approval: &coreruntime.ApprovalDecision{
				Outcome:  strings.TrimSpace(response.Outcome.Outcome),
				OptionID: strings.TrimSpace(response.Outcome.OptionID),
				Approved: permissionApproved(response),
			},
		})
	}
	notifications, err := s.projector.ProjectNotifications(event)
	if err != nil {
		return err
	}
	for _, notification := range notifications {
		if strings.TrimSpace(notification.SessionID) == "" {
			notification.SessionID = strings.TrimSpace(event.SessionID)
		}
		if err := s.conn.Notify(schema.MethodSessionUpdate, notification); err != nil {
			return err
		}
	}
	return nil
}

func permissionApproved(response schema.RequestPermissionResponse) bool {
	outcome := strings.ToLower(strings.TrimSpace(response.Outcome.Outcome))
	optionID := strings.ToLower(strings.TrimSpace(response.Outcome.OptionID))
	switch {
	case strings.HasPrefix(outcome, "allow"):
		return true
	case strings.HasPrefix(optionID, "allow"):
		return true
	default:
		return false
	}
}

func formatACPTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func promptParts(raw []json.RawMessage) (string, []model.ContentPart, error) {
	parts := make([]coreContentPart, 0, len(raw))
	var text []string
	for _, item := range raw {
		part, err := decodePromptPart(item)
		if err != nil {
			return "", nil, err
		}
		if part.Type == "" {
			continue
		}
		parts = append(parts, part)
		if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
			text = append(text, strings.TrimSpace(part.Text))
		}
	}
	return strings.Join(text, "\n"), toModelContentParts(parts), nil
}

type coreContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
	Name     string `json:"name,omitempty"`
	URI      string `json:"uri,omitempty"`
}

func decodePromptPart(raw json.RawMessage) (coreContentPart, error) {
	if len(raw) == 0 {
		return coreContentPart{}, nil
	}
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return coreContentPart{}, err
	}
	switch strings.TrimSpace(envelope.Type) {
	case "text":
		var part coreContentPart
		if err := json.Unmarshal(raw, &part); err != nil {
			return coreContentPart{}, err
		}
		part.Type = "text"
		return part, nil
	case "image":
		var part coreContentPart
		if err := json.Unmarshal(raw, &part); err != nil {
			return coreContentPart{}, err
		}
		part.Type = "image"
		return part, nil
	default:
		return coreContentPart{}, nil
	}
}

func toModelContentParts(in []coreContentPart) []model.ContentPart {
	out := make([]model.ContentPart, 0, len(in))
	for _, item := range in {
		switch item.Type {
		case "text":
			out = append(out, model.ContentPart{
				Type: model.ContentPartText,
				Text: item.Text,
			})
		case "image":
			out = append(out, model.ContentPart{
				Type:     model.ContentPartImage,
				MimeType: item.MimeType,
				Data:     item.Data,
				URI:      item.URI,
				FileName: item.Name,
			})
		}
	}
	return out
}

func workspaceKey(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return "workspace"
	}
	parts := strings.FieldsFunc(cwd, func(r rune) bool {
		return r == '/' || r == '\\'
	})
	for i := len(parts) - 1; i >= 0; i-- {
		if part := strings.TrimSpace(parts[i]); part != "" {
			return part
		}
	}
	return "workspace"
}

func decodeParams(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func invalidParams(err error) *jsonrpc.RPCError {
	return &jsonrpc.RPCError{Code: -32602, Message: "invalid params", Data: err.Error()}
}

func responseOrError[T any](resp T, err error) (any, *jsonrpc.RPCError) {
	if err != nil {
		return nil, &jsonrpc.RPCError{Code: -32000, Message: err.Error()}
	}
	return resp, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

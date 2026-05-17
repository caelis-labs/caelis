package kernel

import (
	"context"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/session"
)

func (g *Gateway) StartSession(ctx context.Context, req StartSessionRequest) (session.Session, error) {
	activeSession, err := g.sessions.StartSession(ctx, session.StartSessionRequest{
		AppName:            req.AppName,
		UserID:             req.UserID,
		Workspace:          req.Workspace,
		PreferredSessionID: req.PreferredSessionID,
		Title:              req.Title,
		Metadata:           cloneMap(req.Metadata),
	})
	if err != nil {
		return session.Session{}, wrapSessionError(err)
	}
	g.bind(req.BindingKey, activeSession.SessionRef, req.Binding)
	return activeSession, nil
}

func (g *Gateway) BindSession(ctx context.Context, req BindSessionRequest) error {
	ref, err := g.sessionTarget(req.SessionRef, "")
	if err != nil {
		return err
	}
	if _, err := g.sessions.Session(ctx, ref); err != nil {
		return wrapSessionError(err)
	}
	g.bind(req.BindingKey, ref, req.Binding)
	return nil
}

func (g *Gateway) ForkSession(ctx context.Context, req ForkSessionRequest) (session.Session, error) {
	if strings.TrimSpace(req.SourceSessionRef.SessionID) == "" {
		return session.Session{}, &Error{
			Kind:        KindValidation,
			Code:        CodeInvalidRequest,
			UserVisible: true,
			Message:     "gateway: source session ref is required",
		}
	}
	source, err := g.sessions.Session(ctx, req.SourceSessionRef)
	if err != nil {
		return session.Session{}, wrapSessionError(err)
	}
	metadata := cloneMap(source.Metadata)
	for key, value := range req.Metadata {
		metadata[key] = value
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["forked_from_session_id"] = source.SessionID
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = source.Title
	}
	started, err := g.sessions.StartSession(ctx, session.StartSessionRequest{
		AppName:            source.AppName,
		UserID:             source.UserID,
		Workspace:          session.WorkspaceRef{Key: source.WorkspaceKey, CWD: source.CWD},
		PreferredSessionID: req.PreferredSessionID,
		Title:              title,
		Metadata:           metadata,
	})
	if err != nil {
		return session.Session{}, wrapSessionError(err)
	}
	g.bind(req.BindingKey, started.SessionRef, req.Binding)
	return started, nil
}

func (g *Gateway) LoadSession(ctx context.Context, req LoadSessionRequest) (session.LoadedSession, error) {
	loaded, err := g.sessions.LoadSession(ctx, session.LoadSessionRequest{
		SessionRef:       req.SessionRef,
		Limit:            req.Limit,
		IncludeTransient: req.IncludeTransient,
	})
	if err != nil {
		return session.LoadedSession{}, wrapSessionError(err)
	}
	g.bind(req.BindingKey, loaded.Session.SessionRef, req.Binding)
	return loaded, nil
}

func (g *Gateway) ResumeSession(ctx context.Context, req ResumeSessionRequest) (session.LoadedSession, error) {
	list, err := g.ListSessions(ctx, ListSessionsRequest{
		AppName:      req.AppName,
		UserID:       req.UserID,
		WorkspaceKey: req.Workspace.Key,
		Limit:        200,
	})
	if err != nil {
		return session.LoadedSession{}, err
	}
	target, err := g.resolveResumeTarget(req, list.Sessions)
	if err != nil {
		return session.LoadedSession{}, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 200
	}
	return g.LoadSession(ctx, LoadSessionRequest{
		SessionRef:       target.SessionRef,
		Limit:            limit,
		IncludeTransient: req.IncludeTransient,
		BindingKey:       req.BindingKey,
		Binding:          req.Binding,
	})
}

func (g *Gateway) ListSessions(ctx context.Context, req ListSessionsRequest) (session.SessionList, error) {
	list, err := g.sessions.ListSessions(ctx, session.ListSessionsRequest{
		AppName:      req.AppName,
		UserID:       req.UserID,
		WorkspaceKey: req.WorkspaceKey,
		Cursor:       req.Cursor,
		Limit:        req.Limit,
	})
	if err != nil {
		return session.SessionList{}, wrapSessionError(err)
	}
	return list, nil
}

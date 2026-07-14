package kernel

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
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
	limit := req.Limit
	if limit <= 0 {
		limit = 200
	}
	if targetID := strings.TrimSpace(req.SessionID); targetID != "" {
		loaded, err := g.loadResumeTarget(ctx, session.SessionRef{SessionID: targetID}, req, limit)
		switch {
		case err == nil && loaded.Session.SessionID == targetID:
			if !resumeSessionInScope(loaded.Session, req) {
				return session.LoadedSession{}, resumeSessionNotFoundError()
			}
			g.bind(req.BindingKey, loaded.Session.SessionRef, req.Binding)
			return loaded, nil
		case err != nil && !errors.Is(err, session.ErrSessionNotFound):
			return session.LoadedSession{}, wrapSessionError(err)
		}
		// A non-exact token may still be a supported unique SessionID prefix.
		// Exact IDs take the direct path above; only prefixes fall back to the
		// bounded namespace list.
	}
	workspaceKey := strings.TrimSpace(req.Workspace.Key)
	if strings.TrimSpace(req.SessionID) != "" {
		workspaceKey = ""
	}
	summaries, err := g.resumeTargetSummaries(ctx, req, workspaceKey)
	if err != nil {
		return session.LoadedSession{}, err
	}
	target, err := g.resolveResumeTarget(req, summaries)
	if err != nil {
		return session.LoadedSession{}, err
	}
	loaded, err := g.loadResumeTarget(ctx, target.SessionRef, req, limit)
	if err != nil {
		return session.LoadedSession{}, wrapSessionError(err)
	}
	if !resumeSessionInScope(loaded.Session, req) {
		return session.LoadedSession{}, resumeSessionNotFoundError()
	}
	g.bind(req.BindingKey, loaded.Session.SessionRef, req.Binding)
	return loaded, nil
}

func (g *Gateway) resumeTargetSummaries(ctx context.Context, req ResumeSessionRequest, workspaceKey string) ([]session.SessionSummary, error) {
	targetID := strings.TrimSpace(req.SessionID)
	cursor := ""
	out := make([]session.SessionSummary, 0, 2)
	for {
		list, err := g.ListSessions(ctx, ListSessionsRequest{
			AppName: req.AppName, UserID: req.UserID, WorkspaceKey: workspaceKey,
			Cursor: cursor, Limit: 200,
		})
		if err != nil {
			return nil, err
		}
		for _, summary := range list.Sessions {
			if targetID != "" && !strings.HasPrefix(strings.TrimSpace(summary.SessionID), targetID) {
				continue
			}
			out = append(out, summary)
			// A prefix with two matches is already ambiguous. With no explicit
			// target, only enough recent candidates to skip the current binding
			// are needed.
			if targetID != "" && len(out) >= 2 {
				return session.CloneSessionSummaries(out), nil
			}
			if targetID == "" && len(out) >= 2 {
				return session.CloneSessionSummaries(out), nil
			}
		}
		next := strings.TrimSpace(list.NextCursor)
		if next == "" || next == cursor {
			return session.CloneSessionSummaries(out), nil
		}
		cursor = next
	}
}

func (g *Gateway) loadResumeTarget(ctx context.Context, ref session.SessionRef, req ResumeSessionRequest, limit int) (session.LoadedSession, error) {
	if req.MetadataOnly {
		active, err := g.sessions.Session(ctx, ref)
		if err != nil {
			return session.LoadedSession{}, err
		}
		return session.LoadedSession{Session: active}, nil
	}
	return g.sessions.LoadSession(ctx, session.LoadSessionRequest{
		SessionRef: ref, Limit: limit, IncludeTransient: req.IncludeTransient,
	})
}

func resumeSessionInScope(active session.Session, req ResumeSessionRequest) bool {
	if appName := strings.TrimSpace(req.AppName); appName != "" && active.AppName != appName {
		return false
	}
	if userID := strings.TrimSpace(req.UserID); userID != "" && active.UserID != userID {
		return false
	}
	if workspaceKey := strings.TrimSpace(req.Workspace.Key); workspaceKey != "" && active.WorkspaceKey != workspaceKey {
		return false
	}
	return true
}

func resumeSessionNotFoundError() error {
	return &Error{
		Kind: KindNotFound, Code: CodeSessionNotFound, UserVisible: true,
		Message: "gateway: session not found",
	}
}

func (g *Gateway) ListSessions(ctx context.Context, req ListSessionsRequest) (session.SessionList, error) {
	if req.Limit <= 0 {
		list, err := g.listSessionPage(ctx, req, req.Cursor, 0)
		if err != nil {
			return session.SessionList{}, err
		}
		list.Sessions, err = g.filterListedSessions(ctx, list.Sessions)
		if err != nil {
			return session.SessionList{}, err
		}
		return list, nil
	}

	visible := make([]session.SessionSummary, 0, req.Limit)
	seen := make(map[string]struct{}, req.Limit)
	cursor := strings.TrimSpace(req.Cursor)
	for len(visible) < req.Limit {
		page, err := g.listSessionPage(ctx, req, cursor, req.Limit-len(visible))
		if err != nil {
			return session.SessionList{}, err
		}
		filtered, err := g.filterListedSessions(ctx, page.Sessions)
		if err != nil {
			return session.SessionList{}, err
		}
		for _, summary := range filtered {
			sessionID := strings.TrimSpace(summary.SessionID)
			if _, ok := seen[sessionID]; ok {
				continue
			}
			seen[sessionID] = struct{}{}
			visible = append(visible, summary)
		}
		next := strings.TrimSpace(page.NextCursor)
		if len(visible) >= req.Limit {
			return session.SessionList{
				Sessions: session.CloneSessionSummaries(visible[:req.Limit]), NextCursor: next,
			}, nil
		}
		if next == "" || next == cursor {
			return session.SessionList{Sessions: session.CloneSessionSummaries(visible)}, nil
		}
		cursor = next
	}
	return session.SessionList{Sessions: session.CloneSessionSummaries(visible)}, nil
}

func (g *Gateway) listSessionPage(ctx context.Context, req ListSessionsRequest, cursor string, limit int) (session.SessionList, error) {
	list, err := g.sessions.ListSessions(ctx, session.ListSessionsRequest{
		AppName: req.AppName, UserID: req.UserID, WorkspaceKey: req.WorkspaceKey,
		Cursor: cursor, Limit: limit,
	})
	if err != nil {
		return session.SessionList{}, wrapSessionError(err)
	}
	return list, nil
}

func (g *Gateway) filterListedSessions(ctx context.Context, summaries []session.SessionSummary) ([]session.SessionSummary, error) {
	if len(summaries) == 0 {
		return nil, nil
	}
	out := make([]session.SessionSummary, 0, len(summaries))
	for _, summary := range summaries {
		systemManaged, err := g.isSystemManagedSessionSummary(ctx, summary)
		if err != nil {
			return nil, wrapSessionError(err)
		}
		if systemManaged {
			continue
		}
		out = append(out, summary)
	}
	return session.CloneSessionSummaries(out), nil
}

func (g *Gateway) isSystemManagedSessionSummary(ctx context.Context, summary session.SessionSummary) (bool, error) {
	if metadataString(summary.Metadata, "system_managed_agent") != "" {
		return true, nil
	}
	if !looksLikeLegacySystemManagedSessionSummary(summary) {
		return false, nil
	}
	if g == nil || g.sessions == nil {
		return true, nil
	}
	loaded, err := g.sessions.Session(ctx, summary.SessionRef)
	if err != nil {
		return false, err
	}
	return metadataString(loaded.Metadata, "system_managed_agent") != "", nil
}

func looksLikeLegacySystemManagedSessionSummary(summary session.SessionSummary) bool {
	if strings.EqualFold(strings.TrimSpace(summary.Title), "Guardian approval review") {
		return true
	}
	return strings.Contains(strings.TrimSpace(summary.SessionID), "-approval-review")
}

func (g *Gateway) updateSessionState(
	ctx context.Context,
	ref session.SessionRef,
	guard session.MutationGuard,
	update func(map[string]any) (map[string]any, error),
) error {
	current, err := g.sessions.Session(ctx, ref)
	if err != nil {
		return err
	}
	_, err = g.sessions.UpdateState(ctx, session.UpdateStateRequest{
		SessionRef: ref, ExpectedRevision: &current.Revision, MutationGuard: guard, Update: update,
	})
	return err
}

package loader

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/ports/session"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

// SessionServiceLoaderConfig configures one default ACP session/load adapter
// backed by the SDK session service.
type SessionServiceLoaderConfig struct {
	Sessions     session.Service
	Projector    projector.Projector
	AppName      string
	UserID       string
	WorkspaceKey string
	Modes        acp.ModeProvider
	Config       acp.ConfigProvider
}

// SessionServiceLoader replays one durable SDK session through ACP
// session/update notifications.
type SessionServiceLoader struct {
	sessions     session.Service
	projector    projector.Projector
	appName      string
	userID       string
	workspaceKey string
	modes        acp.ModeProvider
	config       acp.ConfigProvider
}

// NewSessionServiceLoader constructs one default session/load adapter.
func NewSessionServiceLoader(cfg SessionServiceLoaderConfig) *SessionServiceLoader {
	eventProjector := cfg.Projector
	if eventProjector == nil {
		eventProjector = projector.EventProjector{}
	}
	return &SessionServiceLoader{
		sessions:     cfg.Sessions,
		projector:    eventProjector,
		appName:      strings.TrimSpace(cfg.AppName),
		userID:       strings.TrimSpace(cfg.UserID),
		workspaceKey: strings.TrimSpace(cfg.WorkspaceKey),
		modes:        cfg.Modes,
		config:       cfg.Config,
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// LoadSession replays durable canonical history through session/update and
// returns optional mode/config metadata for the loaded session.
func (l *SessionServiceLoader) LoadSession(
	ctx context.Context,
	req schema.LoadSessionRequest,
	cb acp.PromptCallbacks,
) (schema.LoadSessionResponse, error) {
	ref := session.SessionRef{
		AppName:   l.appName,
		UserID:    l.userID,
		SessionID: strings.TrimSpace(req.SessionID),
	}
	loaded, err := l.sessions.LoadSession(ctx, session.LoadSessionRequest{
		SessionRef: ref,
	})
	if err != nil {
		return schema.LoadSessionResponse{}, err
	}

	if cb != nil {
		for _, event := range loaded.Events {
			if event == nil {
				continue
			}
			notifications, err := projector.ProjectSessionEventNotifications(eventstream.Envelope{
				SessionID: strings.TrimSpace(req.SessionID),
			}, event, l.projector)
			if err != nil {
				return schema.LoadSessionResponse{}, err
			}
			for _, notification := range notifications {
				if err := cb.SessionUpdate(ctx, notification); err != nil {
					return schema.LoadSessionResponse{}, err
				}
			}
		}
	}

	resp := schema.LoadSessionResponse{}
	if l.modes != nil {
		modes, err := l.modes.SessionModes(ctx, loaded.Session)
		if err != nil {
			return schema.LoadSessionResponse{}, err
		}
		resp.Modes = modes
	}
	if l.config != nil {
		options, err := l.config.SessionConfigOptions(ctx, loaded.Session)
		if err != nil {
			return schema.LoadSessionResponse{}, err
		}
		resp.ConfigOptions = options
	}
	return resp, nil
}

package loader

import (
	"context"
	"strings"

	"github.com/OnslaughtSnail/caelis/acp"
	"github.com/OnslaughtSnail/caelis/acp/schema"
	bridgeprojector "github.com/OnslaughtSnail/caelis/acpbridge/projector"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

// SessionServiceLoaderConfig configures one default ACP session/load adapter
// backed by the SDK session service.
type SessionServiceLoaderConfig struct {
	Sessions  sdksession.Service
	Projector acp.Projector
	AppName   string
	UserID    string
	Modes     acp.ModeProvider
	Config    acp.ConfigProvider
}

// SessionServiceLoader replays one durable SDK session through ACP
// session/update notifications.
type SessionServiceLoader struct {
	sessions  sdksession.Service
	projector acp.Projector
	appName   string
	userID    string
	modes     acp.ModeProvider
	config    acp.ConfigProvider
}

// NewSessionServiceLoader constructs one default session/load adapter.
func NewSessionServiceLoader(cfg SessionServiceLoaderConfig) *SessionServiceLoader {
	projector := cfg.Projector
	if projector == nil {
		projector = bridgeprojector.EventProjector{}
	}
	return &SessionServiceLoader{
		sessions:  cfg.Sessions,
		projector: projector,
		appName:   strings.TrimSpace(cfg.AppName),
		userID:    strings.TrimSpace(cfg.UserID),
		modes:     cfg.Modes,
		config:    cfg.Config,
	}
}

// LoadSession replays durable canonical history through session/update and
// returns optional mode/config metadata for the loaded session.
func (l *SessionServiceLoader) LoadSession(
	ctx context.Context,
	req schema.LoadSessionRequest,
	cb acp.PromptCallbacks,
) (schema.LoadSessionResponse, error) {
	ref := sdksession.SessionRef{
		AppName:   l.appName,
		UserID:    l.userID,
		SessionID: strings.TrimSpace(req.SessionID),
	}
	loaded, err := l.sessions.LoadSession(ctx, sdksession.LoadSessionRequest{
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
			notifications, err := l.projector.ProjectNotifications(event)
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

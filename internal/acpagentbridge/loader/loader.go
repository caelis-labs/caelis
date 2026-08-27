package loader

import (
	"context"
	"strings"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

type promptCallbacks interface {
	SessionUpdate(context.Context, schema.SessionNotification) error
	RequestPermission(context.Context, acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error)
}

type modeReader interface {
	SessionModes(context.Context, session.Session) (*acpsdk.SessionModeState, error)
}

type configReader interface {
	SessionConfigOptions(context.Context, session.Session) ([]acpsdk.SessionConfigOption, error)
}

// SessionServiceLoaderConfig configures one default ACP session/load adapter
// backed by the SDK session service.
type SessionServiceLoaderConfig struct {
	Sessions     session.Service
	AppName      string
	UserID       string
	WorkspaceKey string
	Modes        modeReader
	Config       configReader
}

// SessionServiceLoader replays one durable SDK session through ACP
// session/update notifications.
type SessionServiceLoader struct {
	sessions     session.Service
	appName      string
	userID       string
	workspaceKey string
	modes        modeReader
	config       configReader
}

// NewSessionServiceLoader constructs one default session/load adapter.
func NewSessionServiceLoader(cfg SessionServiceLoaderConfig) *SessionServiceLoader {
	return &SessionServiceLoader{
		sessions:     cfg.Sessions,
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
	req acpsdk.LoadSessionRequest,
	cb promptCallbacks,
) (acpsdk.LoadSessionResponse, error) {
	ref := session.SessionRef{
		AppName:   l.appName,
		UserID:    l.userID,
		SessionID: strings.TrimSpace(string(req.SessionId)),
	}
	loaded, err := l.sessions.LoadSession(ctx, session.LoadSessionRequest{
		SessionRef: ref,
	})
	if err != nil {
		return acpsdk.LoadSessionResponse{}, err
	}

	if cb != nil {
		spawnReplay := newSpawnReplayProjector(loaded.Events)
		for _, event := range loaded.Events {
			if event == nil {
				continue
			}
			base := projector.EnvelopeBaseFromSessionEvent(loaded.Session.SessionRef, event, projector.SessionEventTransport{})
			notifications, err := projectSessionEventNotifications(strings.TrimSpace(string(req.SessionId)), event)
			if err != nil {
				return acpsdk.LoadSessionResponse{}, err
			}
			for _, notification := range notifications {
				notification = spawnReplay.normalize(event, notification)
				if err := cb.SessionUpdate(ctx, notification); err != nil {
					return acpsdk.LoadSessionResponse{}, err
				}
				base.Kind = eventstream.KindSessionUpdate
				base.Update = notification.Update
				for _, parentClose := range spawnReplay.observedParentCloses(base, notification.SessionID) {
					if err := cb.SessionUpdate(ctx, parentClose); err != nil {
						return acpsdk.LoadSessionResponse{}, err
					}
				}
			}
		}
	}

	resp := acpsdk.LoadSessionResponse{}
	if l.modes != nil {
		modes, err := l.modes.SessionModes(ctx, loaded.Session)
		if err != nil {
			return acpsdk.LoadSessionResponse{}, err
		}
		resp.Modes = modes
	}
	if l.config != nil {
		options, err := l.config.SessionConfigOptions(ctx, loaded.Session)
		if err != nil {
			return acpsdk.LoadSessionResponse{}, err
		}
		resp.ConfigOptions = options
	}
	return resp, nil
}

package local

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	controller "github.com/caelis-labs/caelis/internal/acpagentbridge/controller"
)

// PresentationService projects protocol-neutral mode, config, model, and
// command state while all writes remain owned by focused Configuration
// commands. ACP wire mapping belongs outside this local Host adapter.
type PresentationService struct {
	sessions         session.Reader
	source           gatewayapp.PresentationSource
	controllerStatus func(context.Context, session.SessionRef) (controller.ControllerStatus, bool, error)
	modeWriteTarget  string
}

func newPresentationService(
	sessions session.Reader,
	source gatewayapp.PresentationSource,
	controllerStatus func(context.Context, session.SessionRef) (controller.ControllerStatus, bool, error),
	useModes bool,
) (*PresentationService, error) {
	if sessions == nil || source == nil || controllerStatus == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: presentation service dependencies are required")
	}
	modeWriteTarget := appserver.PresentationModeTargetApproval
	if useModes {
		modeWriteTarget = appserver.PresentationModeTargetApp
	}
	return &PresentationService{
		sessions: sessions, source: source, controllerStatus: controllerStatus, modeWriteTarget: modeWriteTarget,
	}, nil
}

func (s *PresentationService) PresentationSnapshot(ctx context.Context, principal appserver.Principal, req appserver.PresentationRequest) (appserver.PresentationSnapshot, error) {
	active, err := s.authorizedSession(ctx, principal, appserver.ActionSessionInspect, req.SessionID)
	if err != nil {
		return appserver.PresentationSnapshot{}, err
	}
	modes, err := s.source.SessionModes(ctx, active)
	if err != nil {
		return appserver.PresentationSnapshot{}, err
	}
	modeWriteTarget := s.modeWriteTarget
	if active.Controller.Kind == session.ControllerKindACP {
		remote, found, statusErr := s.controllerStatus(ctx, active.SessionRef)
		if statusErr != nil {
			return appserver.PresentationSnapshot{}, statusErr
		}
		if found && len(remote.ModeOptions) > 0 {
			modes = &appserver.PresentationModeState{CurrentModeID: strings.TrimSpace(remote.Mode)}
			for _, mode := range remote.ModeOptions {
				if id := strings.TrimSpace(mode.ID); id != "" {
					modes.AvailableModes = append(modes.AvailableModes, appserver.PresentationMode{
						ID: id, Name: strings.TrimSpace(mode.Name), Description: strings.TrimSpace(mode.Description),
					})
				}
			}
			modeWriteTarget = appserver.PresentationModeTargetController
		}
	}
	configs, err := s.source.SessionConfigOptions(ctx, active)
	if err != nil {
		return appserver.PresentationSnapshot{}, err
	}
	models, err := s.source.SessionModels(ctx, active)
	if err != nil {
		return appserver.PresentationSnapshot{}, err
	}
	commands, err := s.source.AvailableCommands(ctx, active.SessionID)
	if err != nil {
		return appserver.PresentationSnapshot{}, err
	}
	if modes != nil {
		cloned := *modes
		cloned.AvailableModes = append([]appserver.PresentationMode(nil), modes.AvailableModes...)
		cloned.Target = strings.TrimSpace(modeWriteTarget)
		modes = &cloned
	}
	return appserver.PresentationSnapshot{Modes: modes, ConfigOptions: configs, Models: models, Commands: commands}, nil
}

func (s *PresentationService) PresentationCapabilities(ctx context.Context, principal appserver.Principal) (appserver.PresentationCapabilities, error) {
	if strings.TrimSpace(principal.ID) == "" {
		return appserver.PresentationCapabilities{}, errors.New("app/gatewayapp/controladapter/local: principal ID is required")
	}
	caps, err := s.source.PromptCapabilities(ctx)
	if err != nil {
		return appserver.PresentationCapabilities{}, err
	}
	return caps, nil
}

func (s *PresentationService) authorizedSession(ctx context.Context, principal appserver.Principal, action appserver.Action, sessionID string) (session.Session, error) {
	if s == nil || s.sessions == nil || s.source == nil || s.controllerStatus == nil {
		return session.Session{}, errors.New("app/gatewayapp/controladapter/local: presentation service is unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if err := (appserver.SessionAuthorizer{Sessions: s.sessions}).Authorize(ctx, principal, action, sessionID); err != nil {
		return session.Session{}, err
	}
	return s.sessions.Session(ctx, session.SessionRef{SessionID: sessionID})
}

var _ appserver.PresentationService = (*PresentationService)(nil)

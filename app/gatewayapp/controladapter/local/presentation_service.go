package local

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/protocol/acp"
)

// PresentationService projects ACP-compatible mode, config, model, and command
// state while all writes remain owned by focused Configuration commands.
type PresentationService struct {
	host            *gatewayapp.Stack
	surface         gatewayapp.ACPPresentationService
	modeWriteTarget string
}

func NewPresentationService(host *gatewayapp.Stack, modes acp.ModeProvider, useModes bool, configs acp.ConfigProvider) (*PresentationService, error) {
	if host == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: host Stack is required")
	}
	modeWriteTarget := appserver.PresentationModeTargetApproval
	if useModes && modes != nil {
		modeWriteTarget = appserver.PresentationModeTargetApp
	}
	return &PresentationService{
		host: host, surface: host.ACPSurface(modes, useModes, configs), modeWriteTarget: modeWriteTarget,
	}, nil
}

func (s *PresentationService) PresentationSnapshot(ctx context.Context, principal appserver.Principal, req appserver.PresentationRequest) (appserver.PresentationSnapshot, error) {
	active, err := s.authorizedSession(ctx, principal, appserver.ActionSessionInspect, req.SessionID)
	if err != nil {
		return appserver.PresentationSnapshot{}, err
	}
	modes, err := s.surface.SessionModes(ctx, active)
	if err != nil {
		return appserver.PresentationSnapshot{}, err
	}
	modeWriteTarget := s.modeWriteTarget
	if active.Controller.Kind == session.ControllerKindACP {
		remote, found, statusErr := s.host.ACPControllerStatus(ctx, active.SessionRef)
		if statusErr != nil {
			return appserver.PresentationSnapshot{}, statusErr
		}
		if found && len(remote.ModeOptions) > 0 {
			modes = &acp.SessionModeState{CurrentModeID: strings.TrimSpace(remote.Mode)}
			for _, mode := range remote.ModeOptions {
				if id := strings.TrimSpace(mode.ID); id != "" {
					modes.AvailableModes = append(modes.AvailableModes, acp.SessionMode{
						ID: id, Name: strings.TrimSpace(mode.Name), Description: strings.TrimSpace(mode.Description),
					})
				}
			}
			modeWriteTarget = appserver.PresentationModeTargetController
		}
	}
	configs, err := s.surface.SessionConfigOptions(ctx, active)
	if err != nil {
		return appserver.PresentationSnapshot{}, err
	}
	models, err := s.surface.SessionModels(ctx, active)
	if err != nil {
		return appserver.PresentationSnapshot{}, err
	}
	commands, err := s.surface.AvailableCommands(ctx, active.SessionID)
	if err != nil {
		return appserver.PresentationSnapshot{}, err
	}
	return presentationSnapshot(modes, configs, models, commands, modeWriteTarget), nil
}

func (s *PresentationService) PresentationCapabilities(ctx context.Context, principal appserver.Principal) (appserver.PresentationCapabilities, error) {
	if strings.TrimSpace(principal.ID) == "" {
		return appserver.PresentationCapabilities{}, errors.New("app/gatewayapp/controladapter/local: principal ID is required")
	}
	caps, err := s.surface.PromptCapabilities(ctx)
	if err != nil {
		return appserver.PresentationCapabilities{}, err
	}
	return appserver.PresentationCapabilities{Audio: caps.Audio, EmbeddedContext: caps.EmbeddedContext, Image: caps.Image}, nil
}

func (s *PresentationService) authorizedSession(ctx context.Context, principal appserver.Principal, action appserver.Action, sessionID string) (session.Session, error) {
	if s == nil || s.host == nil || s.host.Sessions == nil || s.surface == nil {
		return session.Session{}, errors.New("app/gatewayapp/controladapter/local: presentation service is unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if err := (appserver.SessionAuthorizer{Sessions: s.host.Sessions}).Authorize(ctx, principal, action, sessionID); err != nil {
		return session.Session{}, err
	}
	return s.host.Sessions.Session(ctx, session.SessionRef{SessionID: sessionID})
}

func presentationSnapshot(
	modes *acp.SessionModeState,
	configs []acp.SessionConfigOption,
	models *acp.SessionModelState,
	commands []acp.AvailableCommand,
	modeWriteTarget string,
) appserver.PresentationSnapshot {
	result := appserver.PresentationSnapshot{ConfigOptions: presentationConfigOptions(configs)}
	if modes != nil {
		result.Modes = &appserver.PresentationModeState{Target: strings.TrimSpace(modeWriteTarget), CurrentModeID: modes.CurrentModeID}
		for _, mode := range modes.AvailableModes {
			result.Modes.AvailableModes = append(result.Modes.AvailableModes, appserver.PresentationMode{ID: mode.ID, Name: mode.Name, Description: mode.Description})
		}
	}
	if models != nil {
		result.Models = &appserver.PresentationModelState{CurrentModelID: models.CurrentModelID}
		for _, model := range models.AvailableModels {
			result.Models.AvailableModels = append(result.Models.AvailableModels, appserver.PresentationModel{ID: model.ModelID, Name: model.Name, Description: model.Description})
		}
	}
	for _, command := range commands {
		mapped := appserver.PresentationCommand{Name: command.Name, Description: command.Description}
		if command.Input != nil {
			mapped.Input = &appserver.PresentationCommandInput{Hint: command.Input.Hint}
		}
		result.Commands = append(result.Commands, mapped)
	}
	return result
}

func presentationConfigOptions(configs []acp.SessionConfigOption) []appserver.PresentationConfigOption {
	result := make([]appserver.PresentationConfigOption, 0, len(configs))
	for _, config := range configs {
		mapped := appserver.PresentationConfigOption{
			Type: config.Type, ID: config.ID, Name: config.Name, Description: config.Description,
			Category: config.Category, CurrentValue: config.CurrentValue,
		}
		for _, option := range config.Options {
			mapped.Options = append(mapped.Options, appserver.PresentationSelectOption{Value: option.Value, Name: option.Name, Description: option.Description})
		}
		result = append(result, mapped)
	}
	return result
}

var _ appserver.PresentationService = (*PresentationService)(nil)

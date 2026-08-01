package local

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/protocol/acp"
)

type acpPresentation interface {
	acp.ModeProvider
	acp.ConfigProvider
	acp.ModelProvider
	acp.CommandProvider
	acp.PromptCapabilitiesProvider
}

// PresentationService keeps ACP-compatible mode, config, model, and command
// assembly on the server side while exposing protocol-neutral Control DTOs.
type PresentationService struct {
	host    *gatewayapp.Stack
	surface acpPresentation
}

func NewPresentationService(host *gatewayapp.Stack, modes acp.ModeProvider, useModes bool, configs acp.ConfigProvider) (*PresentationService, error) {
	if host == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: host Stack is required")
	}
	return &PresentationService{host: host, surface: host.ACPSurface(modes, useModes, configs)}, nil
}

func (s *PresentationService) PresentationSnapshot(ctx context.Context, principal controlclient.Principal, req controlclient.PresentationRequest) (controlclient.PresentationSnapshot, error) {
	active, err := s.authorizedSession(ctx, principal, controlclient.ActionSessionInspect, req.SessionID)
	if err != nil {
		return controlclient.PresentationSnapshot{}, err
	}
	modes, err := s.surface.SessionModes(ctx, active)
	if err != nil {
		return controlclient.PresentationSnapshot{}, err
	}
	configs, err := s.surface.SessionConfigOptions(ctx, active)
	if err != nil {
		return controlclient.PresentationSnapshot{}, err
	}
	models, err := s.surface.SessionModels(ctx, active)
	if err != nil {
		return controlclient.PresentationSnapshot{}, err
	}
	commands, err := s.surface.AvailableCommands(ctx, active.SessionID)
	if err != nil {
		return controlclient.PresentationSnapshot{}, err
	}
	return presentationSnapshot(modes, configs, models, commands), nil
}

func (s *PresentationService) PresentationCapabilities(ctx context.Context, principal controlclient.Principal) (controlclient.PresentationCapabilities, error) {
	if strings.TrimSpace(principal.ID) == "" {
		return controlclient.PresentationCapabilities{}, errors.New("app/gatewayapp/controladapter/local: principal ID is required")
	}
	caps, err := s.surface.PromptCapabilities(ctx)
	if err != nil {
		return controlclient.PresentationCapabilities{}, err
	}
	return controlclient.PresentationCapabilities{Audio: caps.Audio, EmbeddedContext: caps.EmbeddedContext, Image: caps.Image}, nil
}

func (s *PresentationService) SetPresentationMode(ctx context.Context, principal controlclient.Principal, req controlclient.SetPresentationModeRequest) error {
	if _, err := s.authorizedSession(ctx, principal, controlclient.ActionSessionConfigure, req.SessionID); err != nil {
		return err
	}
	_, err := s.surface.SetSessionMode(ctx, acp.SetSessionModeRequest{SessionID: req.SessionID, ModeID: req.ModeID})
	return err
}

func (s *PresentationService) SetPresentationConfig(ctx context.Context, principal controlclient.Principal, req controlclient.SetPresentationConfigRequest) ([]controlclient.PresentationConfigOption, error) {
	if _, err := s.authorizedSession(ctx, principal, controlclient.ActionSessionConfigure, req.SessionID); err != nil {
		return nil, err
	}
	result, err := s.surface.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
		SessionID: req.SessionID, ConfigID: req.ConfigID, Type: req.Type, Value: req.Value,
	})
	if err != nil {
		return nil, err
	}
	return presentationConfigOptions(result.ConfigOptions), nil
}

func (s *PresentationService) SetPresentationModel(ctx context.Context, principal controlclient.Principal, req controlclient.SetPresentationModelRequest) error {
	if _, err := s.authorizedSession(ctx, principal, controlclient.ActionSessionConfigure, req.SessionID); err != nil {
		return err
	}
	_, err := s.surface.SetSessionModel(ctx, acp.SetSessionModelRequest{SessionID: req.SessionID, ModelID: req.ModelID})
	return err
}

func (s *PresentationService) authorizedSession(ctx context.Context, principal controlclient.Principal, action controlclient.Action, sessionID string) (session.Session, error) {
	if s == nil || s.host == nil || s.host.Sessions == nil || s.surface == nil {
		return session.Session{}, errors.New("app/gatewayapp/controladapter/local: presentation service is unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if err := (controlclient.SessionAuthorizer{Sessions: s.host.Sessions}).Authorize(ctx, principal, action, sessionID); err != nil {
		return session.Session{}, err
	}
	return s.host.Sessions.Session(ctx, session.SessionRef{SessionID: sessionID})
}

func presentationSnapshot(modes *acp.SessionModeState, configs []acp.SessionConfigOption, models *acp.SessionModelState, commands []acp.AvailableCommand) controlclient.PresentationSnapshot {
	result := controlclient.PresentationSnapshot{ConfigOptions: presentationConfigOptions(configs)}
	if modes != nil {
		result.Modes = &controlclient.PresentationModeState{CurrentModeID: modes.CurrentModeID}
		for _, mode := range modes.AvailableModes {
			result.Modes.AvailableModes = append(result.Modes.AvailableModes, controlclient.PresentationMode{ID: mode.ID, Name: mode.Name, Description: mode.Description})
		}
	}
	if models != nil {
		result.Models = &controlclient.PresentationModelState{CurrentModelID: models.CurrentModelID}
		for _, model := range models.AvailableModels {
			result.Models.AvailableModels = append(result.Models.AvailableModels, controlclient.PresentationModel{ID: model.ModelID, Name: model.Name, Description: model.Description})
		}
	}
	for _, command := range commands {
		mapped := controlclient.PresentationCommand{Name: command.Name, Description: command.Description}
		if command.Input != nil {
			mapped.Input = &controlclient.PresentationCommandInput{Hint: command.Input.Hint}
		}
		result.Commands = append(result.Commands, mapped)
	}
	return result
}

func presentationConfigOptions(configs []acp.SessionConfigOption) []controlclient.PresentationConfigOption {
	result := make([]controlclient.PresentationConfigOption, 0, len(configs))
	for _, config := range configs {
		mapped := controlclient.PresentationConfigOption{
			Type: config.Type, ID: config.ID, Name: config.Name, Description: config.Description,
			Category: config.Category, CurrentValue: config.CurrentValue,
		}
		for _, option := range config.Options {
			mapped.Options = append(mapped.Options, controlclient.PresentationSelectOption{Value: option.Value, Name: option.Name, Description: option.Description})
		}
		result = append(result, mapped)
	}
	return result
}

var _ controlclient.PresentationService = (*PresentationService)(nil)

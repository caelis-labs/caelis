package controlclient

import (
	"context"
	"errors"
	"strings"
)

// PresentationRequest addresses the ACP-compatible presentation snapshot for
// one Session without carrying ACP wire types into Control.
type PresentationRequest struct {
	SessionID string `json:"session_id"`
}

type PresentationMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type PresentationModeState struct {
	// Target identifies the semantic owner of a mode selection. App-owned ACP
	// modes and approval routing deliberately use separate write commands.
	Target         string             `json:"target,omitempty"`
	AvailableModes []PresentationMode `json:"available_modes"`
	CurrentModeID  string             `json:"current_mode_id"`
}

const (
	PresentationModeTargetApproval   = "approval"
	PresentationModeTargetApp        = "app"
	PresentationModeTargetController = "controller"
)

type PresentationSelectOption struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type PresentationConfigOption struct {
	Type         string                     `json:"type"`
	ID           string                     `json:"id"`
	Name         string                     `json:"name"`
	Description  string                     `json:"description,omitempty"`
	Category     string                     `json:"category,omitempty"`
	CurrentValue any                        `json:"current_value"`
	Options      []PresentationSelectOption `json:"options,omitempty"`
}

type PresentationModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type PresentationModelState struct {
	CurrentModelID  string              `json:"current_model_id"`
	AvailableModels []PresentationModel `json:"available_models"`
}

type PresentationCommandInput struct {
	Hint string `json:"hint,omitempty"`
}

type PresentationCommand struct {
	Name        string                    `json:"name"`
	Description string                    `json:"description,omitempty"`
	Input       *PresentationCommandInput `json:"input,omitempty"`
}

// PresentationSnapshot is refreshed on new/load/resume boundaries. It is not
// injected into an already running Turn, preserving the fixed Runtime prefix.
type PresentationSnapshot struct {
	Modes         *PresentationModeState     `json:"modes,omitempty"`
	ConfigOptions []PresentationConfigOption `json:"config_options,omitempty"`
	Models        *PresentationModelState    `json:"models,omitempty"`
	Commands      []PresentationCommand      `json:"commands,omitempty"`
}

type PresentationCapabilities struct {
	Audio           bool `json:"audio"`
	EmbeddedContext bool `json:"embedded_context"`
	Image           bool `json:"image"`
}

// PresentationService owns protocol-neutral session presentation state used
// by ACP and future surfaces.
type PresentationService interface {
	PresentationSnapshot(context.Context, Principal, PresentationRequest) (PresentationSnapshot, error)
	PresentationCapabilities(context.Context, Principal) (PresentationCapabilities, error)
}

type PresentationClient interface {
	PresentationSnapshot(context.Context, PresentationRequest) (PresentationSnapshot, error)
	PresentationCapabilities(context.Context) (PresentationCapabilities, error)
}

type boundPresentationClient struct {
	service   PresentationService
	principal Principal
}

func BindPresentationClient(service PresentationService, principal Principal) (PresentationClient, error) {
	if service == nil {
		return nil, errors.New("controlclient: presentation service is required")
	}
	principal.ID = strings.TrimSpace(principal.ID)
	if principal.ID == "" {
		return nil, errors.New("controlclient: principal ID is required")
	}
	principal.Roles = append([]string(nil), principal.Roles...)
	return &boundPresentationClient{service: service, principal: principal}, nil
}

func (c *boundPresentationClient) principalCopy() Principal {
	principal := c.principal
	principal.Roles = append([]string(nil), principal.Roles...)
	return principal
}

func (c *boundPresentationClient) PresentationSnapshot(ctx context.Context, req PresentationRequest) (PresentationSnapshot, error) {
	return c.service.PresentationSnapshot(ctx, c.principalCopy(), req)
}
func (c *boundPresentationClient) PresentationCapabilities(ctx context.Context) (PresentationCapabilities, error) {
	return c.service.PresentationCapabilities(ctx, c.principalCopy())
}

var _ PresentationClient = (*boundPresentationClient)(nil)

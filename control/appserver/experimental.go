package appserver

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/control/experimental"
)

// CapabilityExperimental advertises discovery, CAS writes and activation readback.
const CapabilityExperimental = "experimental-features-v1"

// ExperimentalRequest patches Host defaults through the ordinary operation ledger.
// ExpectedRevision is the Host configuration revision from discovery.
type ExperimentalRequest struct {
	WriteBase
	Values experimental.Values `json:"values"`
}

// RefreshMCPToolsRequest retries discovery on an already active Session catalog.
// It uses the ordinary Session authorization, revision checks and operation receipt.
type RefreshMCPToolsRequest struct {
	WriteBase
	PluginID   string `json:"plugin_id"`
	ServerName string `json:"server_name"`
}

// ExperimentalReader supplies desired configuration and authorized live readback.
type ExperimentalReader interface {
	ExperimentalFeatures(context.Context, Principal, string) (experimental.Snapshot, error)
}

// ExperimentalCommandService uses the shared durable mutation owner.
type ExperimentalCommandService interface {
	SetExperimentalFeatures(context.Context, Principal, ExperimentalRequest) (CommandResult, error)
	RefreshMCPTools(context.Context, Principal, RefreshMCPToolsRequest) (CommandResult, error)
}

// ExperimentalService combines discovery with the existing command owner.
type ExperimentalService struct {
	Reader   ExperimentalReader
	Commands ExperimentalCommandService
}

// ExperimentalClient is optional; callers negotiate CapabilityExperimental first.
type ExperimentalClient interface {
	ExperimentalFeatures(context.Context, string) (experimental.Snapshot, error)
	SetExperimentalFeatures(context.Context, ExperimentalRequest) (CommandResult, error)
	RefreshMCPTools(context.Context, RefreshMCPToolsRequest) (CommandResult, error)
}

type boundExperimentalClient struct {
	service   *ExperimentalService
	principal Principal
}

func (c *boundExperimentalClient) ExperimentalFeatures(ctx context.Context, id string) (experimental.Snapshot, error) {
	return c.service.Reader.ExperimentalFeatures(ctx, c.principal, id)
}
func (c *boundExperimentalClient) SetExperimentalFeatures(ctx context.Context, req ExperimentalRequest) (CommandResult, error) {
	return c.service.Commands.SetExperimentalFeatures(ctx, c.principal, req)
}

func (s *CommandService) SetExperimentalFeatures(ctx context.Context, p Principal, req ExperimentalRequest) (CommandResult, error) {
	return s.execute(ctx, p, ActionExperimental, req.WriteBase, "host/configuration/experimental", req)
}
func validateExperimentalRequest(req ExperimentalRequest) error {
	if err := validateHostModelWrite(req.WriteBase); err != nil {
		return err
	}
	if len(req.Values) == 0 {
		return errors.New("experimental values are required")
	}
	return experimental.Validate(req.Values)
}

func (c *boundExperimentalClient) RefreshMCPTools(ctx context.Context, req RefreshMCPToolsRequest) (CommandResult, error) {
	return c.service.Commands.RefreshMCPTools(ctx, c.principal, req)
}

// RefreshMCPTools retries one active directory through the shared mutation owner.
func (s *CommandService) RefreshMCPTools(ctx context.Context, p Principal, req RefreshMCPToolsRequest) (CommandResult, error) {
	return s.execute(ctx, p, ActionSessionMCPRefresh, req.WriteBase, req.SessionID, req)
}
func validateRefreshMCPToolsRequest(req RefreshMCPToolsRequest) error {
	if err := requireSession(req.SessionID); err != nil {
		return err
	}
	if strings.TrimSpace(req.PluginID) == "" || strings.TrimSpace(req.ServerName) == "" {
		return errors.New("plugin_id and server_name are required")
	}
	return nil
}

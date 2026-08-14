package controlclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// PluginCommandService is the principal-aware Host plugin and marketplace
// mutation capability implemented by the shared Control command executor.
type PluginCommandService interface {
	AddMarketplace(context.Context, Principal, AddMarketplaceRequest) (CommandResult, error)
	UpdateMarketplace(context.Context, Principal, UpdateMarketplaceRequest) (CommandResult, error)
	RemoveMarketplace(context.Context, Principal, RemoveMarketplaceRequest) (CommandResult, error)
	AddPluginPath(context.Context, Principal, AddPluginPathRequest) (CommandResult, error)
	InstallPlugin(context.Context, Principal, InstallPluginRequest) (CommandResult, error)
	EnablePlugin(context.Context, Principal, EnablePluginRequest) (CommandResult, error)
	DisablePlugin(context.Context, Principal, DisablePluginRequest) (CommandResult, error)
	RemovePlugin(context.Context, Principal, RemovePluginRequest) (CommandResult, error)
}

func (s *CommandService) AddMarketplace(ctx context.Context, principal Principal, req AddMarketplaceRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionPluginMarketplaceAdd, req.WriteBase, pluginSourceTarget("host/configuration/plugins/marketplace/source", req.Source), req)
}

func (s *CommandService) UpdateMarketplace(ctx context.Context, principal Principal, req UpdateMarketplaceRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionPluginMarketplaceUpdate, req.WriteBase, "host/configuration/plugins/marketplace/"+normalizePluginToken(req.Name), req)
}

func (s *CommandService) RemoveMarketplace(ctx context.Context, principal Principal, req RemoveMarketplaceRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionPluginMarketplaceRemove, req.WriteBase, "host/configuration/plugins/marketplace/"+normalizePluginToken(req.Name), req)
}

func (s *CommandService) AddPluginPath(ctx context.Context, principal Principal, req AddPluginPathRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionPluginAddPath, req.WriteBase, "host/configuration/plugins/path/"+strings.TrimSpace(req.Path), req)
}

func (s *CommandService) InstallPlugin(ctx context.Context, principal Principal, req InstallPluginRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionPluginInstall, req.WriteBase, pluginSourceTarget("host/configuration/plugins/install", req.Source), req)
}

func (s *CommandService) EnablePlugin(ctx context.Context, principal Principal, req EnablePluginRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionPluginEnable, req.WriteBase, "host/configuration/plugins/id/"+normalizePluginToken(req.ID), req)
}

func (s *CommandService) DisablePlugin(ctx context.Context, principal Principal, req DisablePluginRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionPluginDisable, req.WriteBase, "host/configuration/plugins/id/"+normalizePluginToken(req.ID), req)
}

func (s *CommandService) RemovePlugin(ctx context.Context, principal Principal, req RemovePluginRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionPluginRemove, req.WriteBase, "host/configuration/plugins/id/"+normalizePluginToken(req.ID), req)
}

func validateHostPluginWrite(base WriteBase, capability string) error {
	if strings.TrimSpace(base.SessionID) != "" {
		return fmt.Errorf("controlclient: Host plugin %s must not address a Session", capability)
	}
	if base.ExpectedRevision == nil {
		return fmt.Errorf("controlclient: Host plugin %s expected_revision is required", capability)
	}
	if strings.TrimSpace(base.ExpectedControllerEpoch) != "" {
		return fmt.Errorf("controlclient: Host plugin %s must not address a controller epoch", capability)
	}
	return nil
}

func validateAddMarketplaceRequest(action Action, req AddMarketplaceRequest) error {
	if action != ActionPluginMarketplaceAdd {
		return fmt.Errorf("controlclient: unsupported plugin marketplace add action %q", action)
	}
	if err := validateHostPluginWrite(req.WriteBase, "marketplace add"); err != nil {
		return err
	}
	if strings.TrimSpace(req.Source) == "" {
		return errors.New("controlclient: marketplace source is required")
	}
	return validatePluginSourceCredentials(req.Source)
}

func validateUpdateMarketplaceRequest(action Action, req UpdateMarketplaceRequest) error {
	if action != ActionPluginMarketplaceUpdate {
		return fmt.Errorf("controlclient: unsupported plugin marketplace update action %q", action)
	}
	if err := validateHostPluginWrite(req.WriteBase, "marketplace update"); err != nil {
		return err
	}
	if strings.TrimSpace(req.Name) == "" {
		return errors.New("controlclient: marketplace name is required")
	}
	return nil
}

func validateRemoveMarketplaceRequest(action Action, req RemoveMarketplaceRequest) error {
	if action != ActionPluginMarketplaceRemove {
		return fmt.Errorf("controlclient: unsupported plugin marketplace remove action %q", action)
	}
	if err := validateHostPluginWrite(req.WriteBase, "marketplace remove"); err != nil {
		return err
	}
	if strings.TrimSpace(req.Name) == "" {
		return errors.New("controlclient: marketplace name is required")
	}
	return nil
}

func validateAddPluginPathRequest(action Action, req AddPluginPathRequest) error {
	if action != ActionPluginAddPath {
		return fmt.Errorf("controlclient: unsupported plugin add-path action %q", action)
	}
	if err := validateHostPluginWrite(req.WriteBase, "add-path"); err != nil {
		return err
	}
	if strings.TrimSpace(req.Path) == "" {
		return errors.New("controlclient: plugin path is required")
	}
	return nil
}

func validateInstallPluginRequest(action Action, req InstallPluginRequest) error {
	if action != ActionPluginInstall {
		return fmt.Errorf("controlclient: unsupported plugin install action %q", action)
	}
	if err := validateHostPluginWrite(req.WriteBase, "install"); err != nil {
		return err
	}
	if strings.TrimSpace(req.Source) == "" {
		return errors.New("controlclient: plugin install source is required")
	}
	return validatePluginSourceCredentials(req.Source)
}

func validateEnablePluginRequest(action Action, req EnablePluginRequest) error {
	if action != ActionPluginEnable {
		return fmt.Errorf("controlclient: unsupported plugin enable action %q", action)
	}
	if err := validateHostPluginWrite(req.WriteBase, "enable"); err != nil {
		return err
	}
	if strings.TrimSpace(req.ID) == "" {
		return errors.New("controlclient: plugin id is required")
	}
	return nil
}

func validateDisablePluginRequest(action Action, req DisablePluginRequest) error {
	if action != ActionPluginDisable {
		return fmt.Errorf("controlclient: unsupported plugin disable action %q", action)
	}
	if err := validateHostPluginWrite(req.WriteBase, "disable"); err != nil {
		return err
	}
	if strings.TrimSpace(req.ID) == "" {
		return errors.New("controlclient: plugin id is required")
	}
	return nil
}

func validateRemovePluginRequest(action Action, req RemovePluginRequest) error {
	if action != ActionPluginRemove {
		return fmt.Errorf("controlclient: unsupported plugin remove action %q", action)
	}
	if err := validateHostPluginWrite(req.WriteBase, "remove"); err != nil {
		return err
	}
	if strings.TrimSpace(req.ID) == "" {
		return errors.New("controlclient: plugin id is required")
	}
	return nil
}

func normalizePluginToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func pluginSourceTarget(prefix, source string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(source)))
	return strings.TrimSuffix(strings.TrimSpace(prefix), "/") + "/sha256/" + hex.EncodeToString(sum[:])
}

// validatePluginSourceCredentials runs before the operation ledger is opened.
// It prevents credential-bearing remote sources from entering durable intent
// metadata even when a later Plugin-layer source validation would reject them.
func validatePluginSourceCredentials(source string) error {
	source = strings.TrimSpace(source)
	if !strings.Contains(source, "://") {
		return nil
	}
	parsed, _ := url.Parse(source)
	if parsed == nil {
		// The Plugin owner reports malformed/unsupported source syntax. The
		// operation target is already opaque, so parse errors cannot persist it.
		return nil
	}
	if parsed.User != nil {
		if _, hasPassword := parsed.User.Password(); hasPassword {
			return errors.New("controlclient: plugin source must not contain a password")
		}
		if strings.EqualFold(strings.TrimSpace(parsed.Scheme), "https") {
			return errors.New("controlclient: HTTPS plugin source must not contain userinfo")
		}
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("controlclient: remote plugin source must not contain query or fragment credentials")
	}
	return nil
}

var _ PluginCommandService = (*CommandService)(nil)

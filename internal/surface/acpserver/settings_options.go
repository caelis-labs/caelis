package acpserver

import (
	"context"
	"fmt"
	"strings"

	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func (s *Server) setSettingsConfigOption(ctx context.Context, req schema.SetSessionConfigOptionRequest) (bool, error) {
	id := strings.TrimSpace(req.ConfigID)
	switch id {
	case acpConfigSkillLoadingID, acpConfigAutoCompactionID, acpConfigSandboxBackendID:
	default:
		return false, nil
	}
	if !s.services.Settings().Configured() {
		return true, fmt.Errorf("surface/acpserver: settings service is not configured")
	}
	value, ok := req.Value.(string)
	if !ok {
		return true, fmt.Errorf("surface/acpserver: %s value must be a string", id)
	}
	switch id {
	case acpConfigSkillLoadingID:
		return true, s.setSkillLoadingMode(ctx, value)
	case acpConfigAutoCompactionID:
		return true, s.setAutoCompactionMode(ctx, value)
	case acpConfigSandboxBackendID:
		_, err := s.services.Settings().SetSandboxBackend(ctx, value)
		return true, err
	default:
		return false, nil
	}
}

func (s *Server) setSkillLoadingMode(ctx context.Context, value string) error {
	mode, ok := normalizeSkillLoadingConfigValue(value)
	if !ok {
		return fmt.Errorf("surface/acpserver: unsupported skill loading mode %q", value)
	}
	doc, err := s.services.Settings().Document(ctx)
	if err != nil {
		return err
	}
	policy := appsettings.NormalizeSkillPolicy(doc.Skills)
	policy.LoadingMode = mode
	_, err = s.services.Settings().SetSkillPolicy(ctx, policy)
	return err
}

func (s *Server) setAutoCompactionMode(ctx context.Context, value string) error {
	mode, ok := normalizeAutoCompactionConfigValue(value)
	if !ok {
		return fmt.Errorf("surface/acpserver: unsupported auto compaction mode %q", value)
	}
	doc, err := s.services.Settings().Document(ctx)
	if err != nil {
		return err
	}
	policy := appsettings.NormalizeCompactionPolicy(doc.Compaction)
	policy.Auto.Mode = mode
	_, err = s.services.Settings().SetCompaction(ctx, policy)
	return err
}

func (s *Server) sessionSettingsConfigOptions(ctx context.Context) ([]schema.SessionConfigOption, error) {
	if !s.services.Settings().Configured() {
		return nil, nil
	}
	view, err := s.services.Settings().View(ctx)
	if err != nil {
		return nil, err
	}
	return []schema.SessionConfigOption{
		{
			Type:         "select",
			ID:           acpConfigSkillLoadingID,
			Name:         "Skill Loading",
			Description:  "Choose how Caelis exposes and expands skills for prompts",
			Category:     "prompt",
			CurrentValue: firstNonEmpty(view.Skills.LoadingMode, appsettings.SkillLoadingModeExplicit),
			Options: []schema.SessionConfigSelectOption{
				{Value: appsettings.SkillLoadingModeExplicit, Name: "Explicit", Description: "Expose skill metadata and expand explicitly referenced skills"},
				{Value: appsettings.SkillLoadingModeMetadataOnly, Name: "Metadata Only", Description: "Expose skill metadata without loading skill files into turns"},
				{Value: appsettings.SkillLoadingModeDisabled, Name: "Disabled", Description: "Hide skill metadata and disable skill expansion"},
			},
		},
		{
			Type:         "select",
			ID:           acpConfigAutoCompactionID,
			Name:         "Auto Compaction",
			Description:  "Choose whether Caelis compacts context automatically near the model limit",
			Category:     "context",
			CurrentValue: currentAutoCompactionConfigValue(view.Compaction.AutoMode),
			Options: []schema.SessionConfigSelectOption{
				{Value: "enabled", Name: "Enabled", Description: "Compact automatically when context crosses the configured watermark"},
				{Value: "disabled", Name: "Disabled", Description: "Only compact when requested explicitly"},
			},
		},
		{
			Type:         "select",
			ID:           acpConfigSandboxBackendID,
			Name:         "Sandbox Backend",
			Description:  "Choose the requested sandbox backend for local tool execution",
			Category:     "sandbox",
			CurrentValue: firstNonEmpty(view.Sandbox.Backend, "auto"),
			Options: []schema.SessionConfigSelectOption{
				{Value: "auto", Name: "Auto", Description: "Use the platform default sandbox route"},
				{Value: "host", Name: "Host", Description: "Run commands directly on the host"},
				{Value: "seatbelt", Name: "Seatbelt", Description: "Use the macOS Seatbelt backend when available"},
				{Value: "bwrap", Name: "Bubblewrap", Description: "Use the Linux Bubblewrap backend when available"},
				{Value: "landlock", Name: "Landlock", Description: "Use the Linux Landlock backend when available"},
				{Value: "windows", Name: "Windows", Description: "Use the Windows restricted-token backend when available"},
			},
		},
	}, nil
}

func normalizeSkillLoadingConfigValue(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case appsettings.SkillLoadingModeExplicit, "enabled", "enable", "on", "true", "yes":
		return appsettings.SkillLoadingModeExplicit, true
	case appsettings.SkillLoadingModeMetadataOnly, "metadata-only", "metadata", "meta":
		return appsettings.SkillLoadingModeMetadataOnly, true
	case appsettings.SkillLoadingModeDisabled, "disable", "off", "false", "no":
		return appsettings.SkillLoadingModeDisabled, true
	default:
		return "", false
	}
}

func normalizeAutoCompactionConfigValue(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "enabled", "enable", "on", "true", "yes":
		return "enabled", true
	case "disabled", "disable", "off", "false", "no":
		return "disabled", true
	default:
		return "", false
	}
}

func currentAutoCompactionConfigValue(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "disabled":
		return "disabled"
	default:
		return "enabled"
	}
}

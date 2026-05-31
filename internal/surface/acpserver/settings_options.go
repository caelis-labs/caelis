package acpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func (s *Server) setSettingsConfigOption(ctx context.Context, req schema.SetSessionConfigOptionRequest) (bool, error) {
	id := strings.TrimSpace(req.ConfigID)
	fieldID, ok := settingsConfigFieldID(id)
	if !ok {
		return false, nil
	}
	if !s.services.Settings().Configured() {
		return true, fmt.Errorf("surface/acpserver: settings service is not configured")
	}
	value, ok := acpConfigValueString(req.Value)
	if !ok {
		return true, fmt.Errorf("surface/acpserver: %s value must be a string or number", id)
	}
	_, err := s.services.Settings().SetPanelField(ctx, appservices.SettingsPanelFieldUpdateRequest{
		SessionRef: s.sessionRef(req.SessionID),
		FieldID:    fieldID,
		Value:      value,
	})
	return true, err
}

func settingsConfigFieldID(id string) (string, bool) {
	switch id {
	case acpConfigSkillLoadingID:
		return "skills.loading_mode", true
	case acpConfigSkillBudgetID:
		return "skills.max_expansion_chars", true
	case acpConfigAutoCompactionID:
		return "compaction.auto_mode", true
	case acpConfigCompactionWatermarkID:
		return "compaction.watermark", true
	case acpConfigCompactionMaxSourceID:
		return "compaction.max_source_chars", true
	case acpConfigSandboxBackendID:
		return "sandbox.backend", true
	case acpConfigSandboxNetworkID:
		return "sandbox.network", true
	default:
		return "", false
	}
}

func acpConfigValueString(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed), true
	case int:
		return strconv.Itoa(typed), true
	case int64:
		return strconv.FormatInt(typed, 10), true
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32), true
	case json.Number:
		return typed.String(), true
	default:
		return "", false
	}
}

func (s *Server) sessionSettingsConfigOptions(ctx context.Context) ([]schema.SessionConfigOption, error) {
	if !s.services.Settings().Configured() {
		return nil, nil
	}
	view, err := s.services.Settings().View(ctx)
	if err != nil {
		return nil, err
	}
	doc, err := s.services.Settings().Document(ctx)
	if err != nil {
		return nil, err
	}
	skillPolicy := appsettings.NormalizeSkillPolicy(doc.Skills)
	skillBudget := skillPolicy.MaxExpansionChars
	if skillBudget <= 0 {
		skillBudget = appsettings.DefaultSkillExpansionChars
	}
	compactionPolicy := appsettings.NormalizeCompactionPolicy(doc.Compaction)
	return []schema.SessionConfigOption{
		{
			Type:         "select",
			ID:           acpConfigSkillLoadingID,
			Name:         "Skill Loading",
			Description:  "Choose how Caelis exposes and expands skills for prompts",
			Category:     "prompt",
			CurrentValue: appsettings.SkillLoadingMode(skillPolicy),
			Options: []schema.SessionConfigSelectOption{
				{Value: appsettings.SkillLoadingModeExplicit, Name: "Explicit", Description: "Expose skill metadata and expand explicitly referenced skills"},
				{Value: appsettings.SkillLoadingModeMetadataOnly, Name: "Metadata Only", Description: "Expose skill metadata without loading skill files into turns"},
				{Value: appsettings.SkillLoadingModeDisabled, Name: "Disabled", Description: "Hide skill metadata and disable skill expansion"},
			},
		},
		{
			Type:         "number",
			ID:           acpConfigSkillBudgetID,
			Name:         "Skill Expansion Budget",
			Description:  "Maximum characters loaded for explicitly referenced skills",
			Category:     "prompt",
			CurrentValue: skillBudget,
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
			Type:         "number",
			ID:           acpConfigCompactionWatermarkID,
			Name:         "Compaction Watermark",
			Description:  "Context usage ratio that triggers automatic compaction",
			Category:     "context",
			CurrentValue: compactionPolicy.Auto.WatermarkRatio,
		},
		{
			Type:         "number",
			ID:           acpConfigCompactionMaxSourceID,
			Name:         "Compaction Source Limit",
			Description:  "Maximum source characters included in compaction input",
			Category:     "context",
			CurrentValue: compactionPolicy.MaxSourceChars,
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
		{
			Type:         "select",
			ID:           acpConfigSandboxNetworkID,
			Name:         "Sandbox Network",
			Description:  "Choose network policy for sandboxed local tool execution",
			Category:     "sandbox",
			CurrentValue: firstNonEmpty(view.Sandbox.Network, "inherit"),
			Options: []schema.SessionConfigSelectOption{
				{Value: "inherit", Name: "Inherit", Description: "Use the backend default network behavior"},
				{Value: "enabled", Name: "Enabled", Description: "Allow network access when supported by the backend"},
				{Value: "disabled", Name: "Disabled", Description: "Disable network access when supported by the backend"},
			},
		},
	}, nil
}

func currentAutoCompactionConfigValue(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "disabled":
		return "disabled"
	default:
		return "enabled"
	}
}

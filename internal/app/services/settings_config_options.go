package services

import (
	"context"
	"strconv"
	"strings"

	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

func (s SettingsService) ConfigOptions(ctx context.Context) ([]appviewmodel.SettingsConfigOption, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	doc, err := s.Document(ctx)
	if err != nil {
		return nil, err
	}
	return settingsConfigOptionsFromDocument(s.Configured(), doc), nil
}

type settingsConfigOptionSpec struct {
	ID           string
	FieldID      string
	Type         string
	Name         string
	Description  string
	Category     string
	DefaultValue string
	NumberKind   string
	Options      []appviewmodel.SettingsConfigOptionChoice
}

var settingsConfigOptionSpecs = []settingsConfigOptionSpec{
	{
		ID:          "skill_loading_mode",
		FieldID:     "skills.loading_mode",
		Type:        "select",
		Name:        "Skill Loading",
		Description: "Choose how Caelis exposes and expands skills for prompts",
		Category:    "prompt",
		Options: []appviewmodel.SettingsConfigOptionChoice{
			{Value: appsettings.SkillLoadingModeExplicit, Name: "Explicit", Description: "Expose skill metadata and expand explicitly referenced skills"},
			{Value: appsettings.SkillLoadingModeMetadataOnly, Name: "Metadata Only", Description: "Expose skill metadata without loading skill files into turns"},
			{Value: appsettings.SkillLoadingModeDisabled, Name: "Disabled", Description: "Hide skill metadata and disable skill expansion"},
		},
	},
	{
		ID:          "skill_max_expansion_chars",
		FieldID:     "skills.max_expansion_chars",
		Type:        "number",
		Name:        "Skill Expansion Budget",
		Description: "Maximum characters loaded for explicitly referenced skills",
		Category:    "prompt",
		NumberKind:  "int",
	},
	{
		ID:          "prompt_agent_instructions",
		FieldID:     "prompt.agent_instructions",
		Type:        "select",
		Name:        "Agent Instructions",
		Description: "Choose which AGENTS.md instruction scopes are included in the model prompt",
		Category:    "prompt",
		Options: []appviewmodel.SettingsConfigOptionChoice{
			{Value: appsettings.PromptAgentInstructionsAll, Name: "All", Description: "Include session, workspace, and global instructions"},
			{Value: appsettings.PromptAgentInstructionsWorkspaceOnly, Name: "Workspace Only", Description: "Include session and workspace instructions, but omit global instructions"},
			{Value: appsettings.PromptAgentInstructionsDisabled, Name: "Disabled", Description: "Do not include AGENTS.md instruction content"},
		},
	},
	{
		ID:          "prompt_plugin_prompts",
		FieldID:     "prompt.plugin_prompts",
		Type:        "select",
		Name:        "Plugin Prompts",
		Description: "Choose whether plugin prompt fragments are included in the model prompt",
		Category:    "prompt",
		Options: []appviewmodel.SettingsConfigOptionChoice{
			{Value: appsettings.PromptPluginPromptsEnabled, Name: "Enabled"},
			{Value: appsettings.PromptPluginPromptsDisabled, Name: "Disabled"},
		},
	},
	{
		ID:          "prompt_environment",
		FieldID:     "prompt.environment",
		Type:        "select",
		Name:        "Environment Context",
		Description: "Choose whether cwd, OS, and shell context are included in the model prompt",
		Category:    "prompt",
		Options: []appviewmodel.SettingsConfigOptionChoice{
			{Value: appsettings.PromptEnvironmentEnabled, Name: "Enabled"},
			{Value: appsettings.PromptEnvironmentDisabled, Name: "Disabled"},
		},
	},
	{
		ID:           "auto_compaction",
		FieldID:      "compaction.auto_mode",
		Type:         "select",
		Name:         "Auto Compaction",
		Description:  "Choose whether Caelis compacts context automatically near the model limit",
		Category:     "context",
		DefaultValue: "enabled",
		Options: []appviewmodel.SettingsConfigOptionChoice{
			{Value: "enabled", Name: "Enabled", Description: "Compact automatically when context crosses the configured watermark"},
			{Value: "disabled", Name: "Disabled", Description: "Only compact when requested explicitly"},
		},
	},
	{
		ID:          "auto_compaction_watermark",
		FieldID:     "compaction.watermark",
		Type:        "number",
		Name:        "Compaction Watermark",
		Description: "Context usage ratio that triggers automatic compaction",
		Category:    "context",
		NumberKind:  "float",
	},
	{
		ID:          "compaction_max_source_chars",
		FieldID:     "compaction.max_source_chars",
		Type:        "number",
		Name:        "Compaction Source Limit",
		Description: "Maximum source characters included in compaction input",
		Category:    "context",
		NumberKind:  "int",
	},
	{
		ID:          "compaction_task_index_limit",
		FieldID:     "compaction.retention.task_index_limit",
		Type:        "number",
		Name:        "Compaction Task History",
		Description: "Completed task records retained in compact checkpoints",
		Category:    "context",
		NumberKind:  "int",
	},
	{
		ID:          "compaction_controller_index_limit",
		FieldID:     "compaction.retention.controller_index_limit",
		Type:        "number",
		Name:        "Compaction Controller History",
		Description: "Completed controller records retained in compact checkpoints",
		Category:    "context",
		NumberKind:  "int",
	},
	{
		ID:           "sandbox_backend",
		FieldID:      "sandbox.backend",
		Type:         "select",
		Name:         "Sandbox Backend",
		Description:  "Choose the requested sandbox backend for local tool execution",
		Category:     "sandbox",
		DefaultValue: "auto",
	},
	{
		ID:           "sandbox_network",
		FieldID:      "sandbox.network",
		Type:         "select",
		Name:         "Sandbox Network",
		Description:  "Choose network policy for sandboxed local tool execution",
		Category:     "sandbox",
		DefaultValue: "inherit",
	},
}

func settingsPanelFieldConfigMeta(fieldID string) (settingsConfigOptionSpec, bool) {
	fieldID = strings.ToLower(strings.TrimSpace(fieldID))
	for _, spec := range settingsConfigOptionSpecs {
		if spec.FieldID == fieldID {
			return spec, true
		}
	}
	return settingsConfigOptionSpec{}, false
}

func decorateSettingsPanelField(field appviewmodel.SettingsPanelField) appviewmodel.SettingsPanelField {
	fieldID := strings.ToLower(strings.TrimSpace(field.ID))
	if field.Editable && fieldID != "" && strings.TrimSpace(field.Command) == "" {
		field.Command = "/settings set " + fieldID + " "
	}
	spec, ok := settingsPanelFieldConfigMeta(field.ID)
	if !ok {
		return field
	}
	field.ConfigID = spec.ID
	field.Category = spec.Category
	field.Description = spec.Description
	return field
}

func settingsConfigOptionsFromPanel(panel appviewmodel.SettingsPanelView) []appviewmodel.SettingsConfigOption {
	if !panel.Configured {
		return nil
	}
	fields := settingsPanelFieldIndex(panel.Sections)
	out := make([]appviewmodel.SettingsConfigOption, 0, len(settingsConfigOptionSpecs))
	for _, spec := range settingsConfigOptionSpecs {
		field, ok := fields[spec.FieldID]
		if !ok || !field.Editable {
			continue
		}
		options := cloneSettingsConfigOptionChoices(spec.Options)
		if len(options) == 0 {
			options = settingsConfigChoicesFromField(field.Options)
		}
		out = append(out, appviewmodel.SettingsConfigOption{
			Type:         spec.Type,
			ID:           spec.ID,
			FieldID:      spec.FieldID,
			Name:         spec.Name,
			Description:  spec.Description,
			Category:     spec.Category,
			CurrentValue: settingsConfigCurrentValue(spec, field.Value),
			Options:      options,
		})
	}
	return out
}

func settingsConfigOptionsFromDocument(configured bool, doc appsettings.Document) []appviewmodel.SettingsConfigOption {
	panel := appviewmodel.SettingsPanelView{
		Configured: configured,
		Settings:   settingsViewFromDocument(doc),
	}
	panel.Sections = settingsPanelSections(panel, doc)
	return settingsConfigOptionsFromPanel(panel)
}

func settingsPanelFieldIndex(sections []appviewmodel.SettingsPanelSection) map[string]appviewmodel.SettingsPanelField {
	out := map[string]appviewmodel.SettingsPanelField{}
	for _, section := range sections {
		for _, field := range section.Fields {
			id := strings.ToLower(strings.TrimSpace(field.ID))
			if id != "" {
				out[id] = field
			}
		}
	}
	return out
}

func settingsConfigCurrentValue(spec settingsConfigOptionSpec, raw string) any {
	value := strings.TrimSpace(raw)
	if value == "" && spec.DefaultValue != "" {
		value = spec.DefaultValue
	}
	if spec.ID == "auto_compaction" {
		if strings.EqualFold(value, "disabled") {
			return "disabled"
		}
		return "enabled"
	}
	if spec.ID == "sandbox_backend" {
		if backend, err := normalizeSettingsSandboxBackend(value); err == nil {
			return backend
		}
	}
	if spec.ID == "sandbox_network" {
		if network, err := normalizeSettingsSandboxNetwork(value); err == nil {
			return network
		}
	}
	if spec.Type != "number" {
		return value
	}
	switch spec.NumberKind {
	case "float":
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return float64(0)
		}
		return parsed
	default:
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return 0
		}
		return parsed
	}
}

func settingsConfigChoicesFromField(in []appviewmodel.SettingsPanelFieldOption) []appviewmodel.SettingsConfigOptionChoice {
	if len(in) == 0 {
		return nil
	}
	out := make([]appviewmodel.SettingsConfigOptionChoice, 0, len(in))
	for _, option := range in {
		value := strings.TrimSpace(option.Value)
		if value == "" {
			continue
		}
		out = append(out, appviewmodel.SettingsConfigOptionChoice{
			Value:       value,
			Name:        firstNonEmpty(option.Label, value),
			Description: strings.TrimSpace(option.Description),
		})
	}
	return out
}

func cloneSettingsConfigOptionChoices(in []appviewmodel.SettingsConfigOptionChoice) []appviewmodel.SettingsConfigOptionChoice {
	if len(in) == 0 {
		return nil
	}
	out := make([]appviewmodel.SettingsConfigOptionChoice, len(in))
	copy(out, in)
	return out
}

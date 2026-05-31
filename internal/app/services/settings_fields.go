package services

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/config"
	"github.com/OnslaughtSnail/caelis/core/session"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

type SettingsPanelFieldUpdateRequest struct {
	SessionRef session.Ref `json:"session_ref,omitempty"`
	FieldID    string      `json:"field_id,omitempty"`
	Value      string      `json:"value,omitempty"`
}

func (s SettingsService) SetPanelField(ctx context.Context, req SettingsPanelFieldUpdateRequest) (appviewmodel.SettingsPanelView, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return appviewmodel.SettingsPanelView{}, err
	}
	fieldID := strings.ToLower(strings.TrimSpace(req.FieldID))
	if fieldID == "" {
		return appviewmodel.SettingsPanelView{}, errors.New("app/services: settings field id is required")
	}
	if err := s.setPanelFieldValue(ctx, fieldID, req.Value); err != nil {
		return appviewmodel.SettingsPanelView{}, err
	}
	return s.Panel(ctx, SettingsPanelRequest{SessionRef: req.SessionRef})
}

func (s SettingsService) setPanelFieldValue(ctx context.Context, fieldID string, value string) error {
	switch fieldID {
	case "runtime.workspace_key", "runtime.workspace_cwd", "runtime.model":
		_, err := s.updateRuntime(ctx, func(runtime config.Runtime) config.Runtime {
			switch fieldID {
			case "runtime.workspace_key":
				runtime.WorkspaceKey = value
			case "runtime.workspace_cwd":
				runtime.WorkspaceCWD = value
			case "runtime.model":
				runtime.Model = value
			}
			return runtime
		})
		return err
	case "store.backend", "store.uri":
		if fieldID == "store.backend" {
			backend, err := normalizeSettingsStoreBackend(value)
			if err != nil {
				return err
			}
			value = backend
		}
		_, err := s.updateRuntime(ctx, func(runtime config.Runtime) config.Runtime {
			switch fieldID {
			case "store.backend":
				runtime.Store.Backend = value
			case "store.uri":
				runtime.Store.URI = value
			}
			return runtime
		})
		return err
	case "sandbox.backend":
		_, err := s.SetSandboxBackend(ctx, value)
		return err
	case "sandbox.network", "sandbox.readable_roots", "sandbox.writable_roots", "sandbox.helper_path":
		if fieldID == "sandbox.network" {
			network, err := normalizeSettingsSandboxNetwork(value)
			if err != nil {
				return err
			}
			value = network
		}
		_, err := s.updateRuntime(ctx, func(runtime config.Runtime) config.Runtime {
			switch fieldID {
			case "sandbox.network":
				runtime.Sandbox.Network = value
			case "sandbox.readable_roots":
				runtime.Sandbox.ReadableRoots = parseSettingsPathList(value)
			case "sandbox.writable_roots":
				runtime.Sandbox.WritableRoots = parseSettingsPathList(value)
			case "sandbox.helper_path":
				runtime.Sandbox.HelperPath = value
			}
			return runtime
		})
		return err
	case "compaction.auto_mode", "compaction.watermark", "compaction.max_source_chars", "compaction.prompt":
		doc, err := s.Document(ctx)
		if err != nil {
			return err
		}
		policy := appsettings.NormalizeCompactionPolicy(doc.Compaction)
		switch fieldID {
		case "compaction.auto_mode":
			mode, err := normalizeSettingsAutoCompactionMode(value)
			if err != nil {
				return err
			}
			policy.Auto.Mode = mode
		case "compaction.watermark":
			ratio, err := parseSettingsWatermarkRatio(value)
			if err != nil {
				return err
			}
			policy.Auto.WatermarkRatio = ratio
		case "compaction.max_source_chars":
			maxChars, err := parseSettingsNonNegativeInt(value, "compaction max source chars")
			if err != nil {
				return err
			}
			policy.MaxSourceChars = maxChars
		case "compaction.prompt":
			policy.Prompt = value
		}
		_, err = s.SetCompaction(ctx, policy)
		return err
	case "skills.loading_mode", "skills.max_expansion_chars":
		doc, err := s.Document(ctx)
		if err != nil {
			return err
		}
		policy := appsettings.NormalizeSkillPolicy(doc.Skills)
		switch fieldID {
		case "skills.loading_mode":
			mode, err := normalizeSettingsSkillLoadingMode(value)
			if err != nil {
				return err
			}
			policy.LoadingMode = mode
		case "skills.max_expansion_chars":
			budget, err := parseSettingsNonNegativeInt(value, "skill expansion budget")
			if err != nil {
				return err
			}
			policy.MaxExpansionChars = budget
		}
		_, err = s.SetSkillPolicy(ctx, policy)
		return err
	default:
		return fmt.Errorf("app/services: settings field %q is not editable", fieldID)
	}
}

func parseSettingsPathList(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" || value == "-" {
		return nil
	}
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n'
	})
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		out = append(out, part)
	}
	return out
}

func normalizeSettingsStoreBackend(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "jsonl":
		return "jsonl", nil
	case "sqlite":
		return "sqlite", nil
	case "memory":
		return "memory", nil
	default:
		return "", fmt.Errorf("app/services: unsupported store backend %q", strings.TrimSpace(value))
	}
}

func normalizeSettingsSandboxNetwork(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "inherit", "default":
		return "inherit", nil
	case "enabled", "enable", "on", "true", "yes":
		return "enabled", nil
	case "disabled", "disable", "off", "false", "no":
		return "disabled", nil
	default:
		return "", fmt.Errorf("app/services: unsupported sandbox network mode %q", strings.TrimSpace(value))
	}
}

func normalizeSettingsAutoCompactionMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "default":
		return "", nil
	case "enabled", "enable", "on", "true", "yes":
		return "enabled", nil
	case "disabled", "disable", "off", "false", "no":
		return "disabled", nil
	default:
		return "", fmt.Errorf("app/services: unsupported auto compaction mode %q", strings.TrimSpace(value))
	}
}

func normalizeSettingsSkillLoadingMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "default", appsettings.SkillLoadingModeExplicit, "expand", "expanded", "enabled", "enable", "on", "true", "yes":
		return appsettings.SkillLoadingModeExplicit, nil
	case appsettings.SkillLoadingModeMetadataOnly, "metadata-only", "metadata", "meta":
		return appsettings.SkillLoadingModeMetadataOnly, nil
	case appsettings.SkillLoadingModeDisabled, "disable", "off", "false", "no":
		return appsettings.SkillLoadingModeDisabled, nil
	default:
		return "", fmt.Errorf("app/services: unsupported skill loading mode %q", strings.TrimSpace(value))
	}
}

func parseSettingsNonNegativeInt(value string, label string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "-" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("app/services: %s must be a non-negative integer", label)
	}
	return parsed, nil
}

func parseSettingsWatermarkRatio(value string) (float64, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "-" {
		return 0, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed < 0 || parsed > 1 {
		return 0, fmt.Errorf("app/services: compaction watermark must be a number between 0 and 1")
	}
	return parsed, nil
}

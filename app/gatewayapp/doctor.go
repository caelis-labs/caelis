package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	sdkproviders "github.com/OnslaughtSnail/caelis/sdk/model/providers"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

type DoctorRequest struct {
	SessionRef sdksession.SessionRef
	SessionID  string
	BindingKey string
}

type DoctorReport struct {
	GoVersion               string   `json:"go_version,omitempty"`
	GOOS                    string   `json:"goos,omitempty"`
	GOARCH                  string   `json:"goarch,omitempty"`
	StoreDir                string   `json:"store_dir,omitempty"`
	ConfigPath              string   `json:"config_path,omitempty"`
	ConfigDirMode           string   `json:"config_dir_mode,omitempty"`
	ConfigFileMode          string   `json:"config_file_mode,omitempty"`
	ConfigDirSecure         bool     `json:"config_dir_secure,omitempty"`
	ConfigFileSecure        bool     `json:"config_file_secure,omitempty"`
	ConfigPermissionsSecure bool     `json:"config_permissions_secure,omitempty"`
	SessionID               string   `json:"session_id,omitempty"`
	SessionMode             string   `json:"session_mode,omitempty"`
	ActiveModelAlias        string   `json:"active_model_alias,omitempty"`
	ActiveProvider          string   `json:"active_provider,omitempty"`
	ActiveModel             string   `json:"active_model,omitempty"`
	MissingAPIKey           bool     `json:"missing_api_key,omitempty"`
	TokenSource             string   `json:"token_source,omitempty"`
	PersistedPlaintextToken bool     `json:"persisted_plaintext_token,omitempty"`
	SandboxRequestedBackend string   `json:"sandbox_requested_backend,omitempty"`
	SandboxResolvedBackend  string   `json:"sandbox_resolved_backend,omitempty"`
	SandboxRoute            string   `json:"sandbox_route,omitempty"`
	SandboxFallbackReason   string   `json:"sandbox_fallback_reason,omitempty"`
	SandboxSecuritySummary  string   `json:"sandbox_security_summary,omitempty"`
	HostExecution           bool     `json:"host_execution,omitempty"`
	FullAccessMode          bool     `json:"full_access_mode,omitempty"`
	HasActiveTurn           bool     `json:"has_active_turn,omitempty"`
	ActiveTurnCount         int      `json:"active_turn_count,omitempty"`
	ActiveTurnSessions      []string `json:"active_turn_sessions,omitempty"`
	Warnings                []string `json:"warnings,omitempty"`
}

func (s *Stack) Doctor(ctx context.Context, req DoctorRequest) (DoctorReport, error) {
	if s == nil {
		return DoctorReport{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	report := DoctorReport{
		GoVersion: runtime.Version(),
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
		StoreDir:  strings.TrimSpace(s.storeDir),
	}
	if s.store != nil {
		report.ConfigPath = strings.TrimSpace(s.store.path)
	}
	dirMode, dirSecure, fileMode, fileSecure := checkConfigPermissions(report.ConfigPath)
	report.ConfigDirMode = dirMode
	report.ConfigDirSecure = dirSecure
	report.ConfigFileMode = fileMode
	report.ConfigFileSecure = fileSecure
	report.ConfigPermissionsSecure = dirSecure && fileSecure

	ref := s.resolveDoctorSessionRef(ctx, req)
	report.SessionID = strings.TrimSpace(ref.SessionID)
	report.SessionMode = "default"
	alias := ""
	if strings.TrimSpace(ref.SessionID) != "" {
		state, err := s.SessionRuntimeState(ctx, ref)
		if err != nil {
			return DoctorReport{}, err
		}
		if strings.TrimSpace(state.SessionMode) != "" {
			report.SessionMode = strings.TrimSpace(state.SessionMode)
		}
		alias = strings.TrimSpace(state.ModelAlias)
	}
	if alias == "" && s.lookup != nil {
		alias = strings.TrimSpace(s.lookup.DefaultAlias())
	}
	report.ActiveModelAlias = alias
	if cfg, ok := s.modelConfigForAlias(alias); ok {
		report.ActiveProvider = strings.TrimSpace(cfg.Provider)
		report.ActiveModel = strings.TrimSpace(cfg.Model)
		report.MissingAPIKey = modelConfigMissingAPIKey(cfg)
		report.TokenSource = modelConfigTokenSource(cfg)
		report.PersistedPlaintextToken = cfg.PersistToken && strings.TrimSpace(cfg.Token) != ""
		if report.PersistedPlaintextToken {
			report.Warnings = append(report.Warnings, "plaintext token persistence is enabled; prefer token_env")
		}
	}

	sandbox := s.SandboxStatus()
	report.SandboxRequestedBackend = strings.TrimSpace(sandbox.RequestedBackend)
	report.SandboxResolvedBackend = strings.TrimSpace(sandbox.ResolvedBackend)
	report.SandboxRoute = strings.TrimSpace(sandbox.Route)
	report.SandboxFallbackReason = strings.TrimSpace(sandbox.FallbackReason)
	report.SandboxSecuritySummary = strings.TrimSpace(sandbox.SecuritySummary)
	report.HostExecution = strings.EqualFold(report.SandboxRoute, "host") || strings.EqualFold(report.SandboxResolvedBackend, "host")
	report.FullAccessMode = strings.EqualFold(report.SessionMode, "full_access")
	if report.FullAccessMode {
		report.Warnings = append(report.Warnings, "session is running in full_access mode")
	}
	if report.HostExecution {
		report.Warnings = append(report.Warnings, "sandbox execution is using host route")
	}
	if report.MissingAPIKey {
		report.Warnings = append(report.Warnings, "active model configuration is missing an API key")
	}
	if !report.ConfigPermissionsSecure && strings.TrimSpace(report.ConfigPath) != "" {
		report.Warnings = append(report.Warnings, "config file permissions are not secure")
	}

	if s.Gateway != nil {
		active := s.Gateway.ActiveTurns()
		report.ActiveTurnCount = len(active)
		report.HasActiveTurn = len(active) > 0
		sessions := make([]string, 0, len(active))
		for _, item := range active {
			if sessionID := strings.TrimSpace(item.SessionRef.SessionID); sessionID != "" {
				sessions = append(sessions, sessionID)
			}
		}
		sort.Strings(sessions)
		report.ActiveTurnSessions = dedupeNonEmptyStrings(sessions)
	}

	return report, nil
}

func FormatDoctorText(report DoctorReport) string {
	lines := []string{
		fmt.Sprintf("go_version: %s", firstNonEmpty(strings.TrimSpace(report.GoVersion), "unknown")),
		fmt.Sprintf("platform: %s/%s", firstNonEmpty(strings.TrimSpace(report.GOOS), "unknown"), firstNonEmpty(strings.TrimSpace(report.GOARCH), "unknown")),
		fmt.Sprintf("store_dir: %s", firstNonEmpty(strings.TrimSpace(report.StoreDir), "-")),
		fmt.Sprintf("config_path: %s", firstNonEmpty(strings.TrimSpace(report.ConfigPath), "-")),
		fmt.Sprintf("config_permissions_secure: %t", report.ConfigPermissionsSecure),
		fmt.Sprintf("config_dir_mode: %s", firstNonEmpty(strings.TrimSpace(report.ConfigDirMode), "-")),
		fmt.Sprintf("config_file_mode: %s", firstNonEmpty(strings.TrimSpace(report.ConfigFileMode), "-")),
		fmt.Sprintf("session_id: %s", firstNonEmpty(strings.TrimSpace(report.SessionID), "-")),
		fmt.Sprintf("session_mode: %s", firstNonEmpty(strings.TrimSpace(report.SessionMode), "default")),
		fmt.Sprintf("active_model_alias: %s", firstNonEmpty(strings.TrimSpace(report.ActiveModelAlias), "-")),
		fmt.Sprintf("active_provider: %s", firstNonEmpty(strings.TrimSpace(report.ActiveProvider), "-")),
		fmt.Sprintf("active_model: %s", firstNonEmpty(strings.TrimSpace(report.ActiveModel), "-")),
		fmt.Sprintf("missing_api_key: %t", report.MissingAPIKey),
		fmt.Sprintf("token_source: %s", firstNonEmpty(strings.TrimSpace(report.TokenSource), "-")),
		fmt.Sprintf("persisted_plaintext_token: %t", report.PersistedPlaintextToken),
		fmt.Sprintf("sandbox_requested_backend: %s", firstNonEmpty(strings.TrimSpace(report.SandboxRequestedBackend), "-")),
		fmt.Sprintf("sandbox_resolved_backend: %s", firstNonEmpty(strings.TrimSpace(report.SandboxResolvedBackend), "-")),
		fmt.Sprintf("sandbox_route: %s", firstNonEmpty(strings.TrimSpace(report.SandboxRoute), "-")),
		fmt.Sprintf("sandbox_fallback_reason: %s", firstNonEmpty(strings.TrimSpace(report.SandboxFallbackReason), "-")),
		fmt.Sprintf("sandbox_security_summary: %s", firstNonEmpty(strings.TrimSpace(report.SandboxSecuritySummary), "-")),
		fmt.Sprintf("host_execution: %t", report.HostExecution),
		fmt.Sprintf("full_access_mode: %t", report.FullAccessMode),
		fmt.Sprintf("has_active_turn: %t", report.HasActiveTurn),
		fmt.Sprintf("active_turn_count: %d", report.ActiveTurnCount),
		fmt.Sprintf("active_turn_sessions: %s", firstNonEmpty(strings.Join(report.ActiveTurnSessions, ", "), "-")),
	}
	for _, warning := range report.Warnings {
		if strings.TrimSpace(warning) == "" {
			continue
		}
		lines = append(lines, "warning: "+strings.TrimSpace(warning))
	}
	return strings.Join(lines, "\n")
}

func (r DoctorReport) MarshalJSON() ([]byte, error) {
	type reportAlias DoctorReport
	out := reportAlias(r)
	return json.Marshal(out)
}

func (s *Stack) resolveDoctorSessionRef(ctx context.Context, req DoctorRequest) sdksession.SessionRef {
	if s == nil {
		return sdksession.SessionRef{}
	}
	if strings.TrimSpace(req.SessionRef.SessionID) != "" {
		return req.SessionRef
	}
	if strings.TrimSpace(req.SessionID) != "" {
		return sdksession.SessionRef{
			AppName:      s.AppName,
			UserID:       s.UserID,
			SessionID:    strings.TrimSpace(req.SessionID),
			WorkspaceKey: s.Workspace.Key,
		}
	}
	if strings.TrimSpace(req.BindingKey) != "" && s.Gateway != nil {
		if ref, ok := s.Gateway.CurrentSession(req.BindingKey); ok {
			return ref
		}
	}
	_ = ctx
	return sdksession.SessionRef{}
}

func (s *Stack) modelConfigForAlias(alias string) (ModelConfig, bool) {
	if s == nil || s.lookup == nil {
		return ModelConfig{}, false
	}
	return s.lookup.Config(alias)
}

func modelConfigMissingAPIKey(cfg ModelConfig) bool {
	cfg = normalizeModelConfig(cfg)
	if cfg.Provider == "" || cfg.Model == "" {
		return false
	}
	if cfg.AuthType == "" || cfg.AuthType == sdkproviders.AuthNone {
		return false
	}
	if strings.TrimSpace(cfg.Token) != "" {
		return false
	}
	if env := strings.TrimSpace(cfg.TokenEnv); env != "" {
		return strings.TrimSpace(os.Getenv(env)) == ""
	}
	return true
}

func modelConfigTokenSource(cfg ModelConfig) string {
	switch {
	case strings.TrimSpace(cfg.TokenEnv) != "":
		return "env:" + strings.TrimSpace(cfg.TokenEnv)
	case strings.TrimSpace(cfg.Token) != "":
		if cfg.PersistToken {
			return "plaintext_config"
		}
		return "memory"
	default:
		return ""
	}
}

func checkConfigPermissions(path string) (dirMode string, dirSecure bool, fileMode string, fileSecure bool) {
	if strings.TrimSpace(path) == "" {
		return "", true, "", true
	}
	dirMode, dirSecure = securePathMode(filepath.Dir(path), 0o700, true)
	fileMode, fileSecure = securePathMode(path, 0o600, false)
	return dirMode, dirSecure, fileMode, fileSecure
}

func securePathMode(path string, maxMode os.FileMode, isDir bool) (string, bool) {
	if runtime.GOOS == "windows" {
		return "", true
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", true
		}
		return "", false
	}
	mode := info.Mode().Perm()
	if isDir && !info.IsDir() {
		return mode.String(), false
	}
	return fmt.Sprintf("%#o", mode), mode&^maxMode == 0
}

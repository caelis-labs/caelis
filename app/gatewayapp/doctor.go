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
	"time"

	"github.com/OnslaughtSnail/caelis/impl/model/providers"
	sandboxport "github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

type DoctorRequest struct {
	SessionRef session.SessionRef
	SessionID  string
	BindingKey string
}

type DoctorReport struct {
	GoVersion                       string                   `json:"go_version,omitempty"`
	GOOS                            string                   `json:"goos,omitempty"`
	GOARCH                          string                   `json:"goarch,omitempty"`
	StoreDir                        string                   `json:"store_dir,omitempty"`
	ConfigPath                      string                   `json:"config_path,omitempty"`
	ConfigDirMode                   string                   `json:"config_dir_mode,omitempty"`
	ConfigFileMode                  string                   `json:"config_file_mode,omitempty"`
	ConfigDirSecure                 bool                     `json:"config_dir_secure,omitempty"`
	ConfigFileSecure                bool                     `json:"config_file_secure,omitempty"`
	ConfigPermissionsSecure         bool                     `json:"config_permissions_secure,omitempty"`
	SessionID                       string                   `json:"session_id,omitempty"`
	SessionMode                     string                   `json:"session_mode,omitempty"`
	ActiveModelAlias                string                   `json:"active_model_alias,omitempty"`
	ActiveProvider                  string                   `json:"active_provider,omitempty"`
	ActiveModel                     string                   `json:"active_model,omitempty"`
	MissingAPIKey                   bool                     `json:"missing_api_key,omitempty"`
	TokenSource                     string                   `json:"token_source,omitempty"`
	PersistedPlaintextToken         bool                     `json:"persisted_plaintext_token,omitempty"`
	SandboxRequestedBackend         string                   `json:"sandbox_requested_backend,omitempty"`
	SandboxResolvedBackend          string                   `json:"sandbox_resolved_backend,omitempty"`
	SandboxRoute                    string                   `json:"sandbox_route,omitempty"`
	SandboxFallbackReason           string                   `json:"sandbox_fallback_reason,omitempty"`
	SandboxInstallHint              string                   `json:"sandbox_install_hint,omitempty"`
	SandboxSetup                    *sandboxport.SetupStatus `json:"sandbox_setup,omitempty"`
	SandboxSetupRequired            bool                     `json:"sandbox_setup_required,omitempty"`
	SandboxSetupError               string                   `json:"sandbox_setup_error,omitempty"`
	SandboxSetupVersion             int                      `json:"sandbox_setup_version,omitempty"`
	SandboxSetupMarkerCurrent       bool                     `json:"sandbox_setup_marker_current,omitempty"`
	SandboxSetupMarkerReason        string                   `json:"sandbox_setup_marker_reason,omitempty"`
	SandboxSetupRunnerHash          string                   `json:"sandbox_setup_runner_hash,omitempty"`
	SandboxSetupPolicyHash          string                   `json:"sandbox_setup_policy_hash,omitempty"`
	SandboxSetupOfflineUser         string                   `json:"sandbox_setup_offline_user,omitempty"`
	SandboxSetupOnlineUser          string                   `json:"sandbox_setup_online_user,omitempty"`
	SandboxSetupOwnerUser           string                   `json:"sandbox_setup_owner_user,omitempty"`
	SandboxSetupReadRoots           int                      `json:"sandbox_setup_read_roots,omitempty"`
	SandboxSetupWriteRoots          int                      `json:"sandbox_setup_write_roots,omitempty"`
	SandboxSetupDenyRead            int                      `json:"sandbox_setup_deny_read,omitempty"`
	SandboxSetupDenyWrite           int                      `json:"sandbox_setup_deny_write,omitempty"`
	SandboxSecuritySummary          string                   `json:"sandbox_security_summary,omitempty"`
	SandboxGlobalSetupCurrent       bool                     `json:"sandbox_global_setup_current,omitempty"`
	SandboxGlobalSetupRequired      bool                     `json:"sandbox_global_setup_required,omitempty"`
	SandboxGlobalSetupReason        string                   `json:"sandbox_global_setup_reason,omitempty"`
	SandboxWorkspaceSetupCurrent    bool                     `json:"sandbox_workspace_setup_current,omitempty"`
	SandboxWorkspaceSetupRequired   bool                     `json:"sandbox_workspace_setup_required,omitempty"`
	SandboxWorkspaceSetupReason     string                   `json:"sandbox_workspace_setup_reason,omitempty"`
	SandboxWorkspaceSetupRoot       string                   `json:"sandbox_workspace_setup_root,omitempty"`
	SandboxWorkspaceSetupWriteRoots int                      `json:"sandbox_workspace_setup_write_roots,omitempty"`
	SandboxWorkspaceSetupPolicyHash string                   `json:"sandbox_workspace_setup_policy_hash,omitempty"`
	SandboxWorkspaceSetupUpdatedAt  time.Time                `json:"sandbox_workspace_setup_updated_at,omitempty"`
	HostExecution                   bool                     `json:"host_execution,omitempty"`
	FullAccessMode                  bool                     `json:"full_access_mode,omitempty"`
	PermissionGrantCount            int                      `json:"permission_grant_count,omitempty"`
	PermissionGrantNetwork          bool                     `json:"permission_grant_network,omitempty"`
	PermissionReadRootCount         int                      `json:"permission_read_root_count,omitempty"`
	PermissionWriteRootCount        int                      `json:"permission_write_root_count,omitempty"`
	HasActiveTurn                   bool                     `json:"has_active_turn,omitempty"`
	ActiveTurnCount                 int                      `json:"active_turn_count,omitempty"`
	ActiveTurnSessions              []string                 `json:"active_turn_sessions,omitempty"`
	Warnings                        []string                 `json:"warnings,omitempty"`
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
	s.mu.RLock()
	report.SessionMode = policyMode(s.runtime.PermissionMode)
	s.mu.RUnlock()
	alias := ""
	modelRef := ""
	if strings.TrimSpace(ref.SessionID) != "" {
		state, err := s.SessionRuntimeState(ctx, ref)
		if err != nil {
			return DoctorReport{}, err
		}
		if strings.TrimSpace(state.SessionMode) != "" {
			report.SessionMode = strings.TrimSpace(state.SessionMode)
		}
		alias = strings.TrimSpace(state.ModelAlias)
		modelRef = strings.TrimSpace(firstNonEmpty(state.ModelID, state.ModelAlias))
	}
	if alias == "" && s.lookup != nil {
		alias = strings.TrimSpace(s.lookup.DefaultAlias())
		modelRef = strings.TrimSpace(s.lookup.DefaultID())
	}
	report.ActiveModelAlias = alias
	if cfg, ok := s.modelConfigForAlias(firstNonEmpty(modelRef, alias)); ok {
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
	report.SandboxInstallHint = strings.TrimSpace(sandbox.InstallHint)
	if setup := sandboxport.CloneSetupStatus(sandbox.Setup); !setupStatusEmpty(setup) {
		report.SandboxSetup = &setup
	}
	report.SandboxSetupRequired = sandbox.SetupRequired
	report.SandboxSetupError = strings.TrimSpace(sandbox.SetupError)
	report.SandboxSetupVersion = sandbox.SetupVersion
	report.SandboxSetupMarkerCurrent = sandbox.SetupMarkerCurrent
	report.SandboxSetupMarkerReason = strings.TrimSpace(sandbox.SetupMarkerReason)
	report.SandboxSetupRunnerHash = strings.TrimSpace(sandbox.SetupRunnerHash)
	report.SandboxSetupPolicyHash = strings.TrimSpace(sandbox.SetupPolicyHash)
	report.SandboxSetupOfflineUser = strings.TrimSpace(sandbox.SetupOfflineUser)
	report.SandboxSetupOnlineUser = strings.TrimSpace(sandbox.SetupOnlineUser)
	report.SandboxSetupOwnerUser = strings.TrimSpace(sandbox.SetupOwnerUser)
	report.SandboxSetupReadRoots = sandbox.SetupReadRoots
	report.SandboxSetupWriteRoots = sandbox.SetupWriteRoots
	report.SandboxSetupDenyRead = sandbox.SetupDenyRead
	report.SandboxSetupDenyWrite = sandbox.SetupDenyWrite
	report.SandboxSecuritySummary = strings.TrimSpace(sandbox.SecuritySummary)
	report.SandboxGlobalSetupCurrent = sandbox.GlobalSetupCurrent
	report.SandboxGlobalSetupRequired = sandbox.GlobalSetupRequired
	report.SandboxGlobalSetupReason = strings.TrimSpace(sandbox.GlobalSetupReason)
	report.SandboxWorkspaceSetupCurrent = sandbox.WorkspaceSetupCurrent
	report.SandboxWorkspaceSetupRequired = sandbox.WorkspaceSetupRequired
	report.SandboxWorkspaceSetupReason = strings.TrimSpace(sandbox.WorkspaceSetupReason)
	report.SandboxWorkspaceSetupRoot = strings.TrimSpace(sandbox.WorkspaceSetupRoot)
	report.SandboxWorkspaceSetupWriteRoots = sandbox.WorkspaceSetupWriteRoots
	report.SandboxWorkspaceSetupPolicyHash = strings.TrimSpace(sandbox.WorkspaceSetupPolicyHash)
	report.SandboxWorkspaceSetupUpdatedAt = sandbox.WorkspaceSetupUpdatedAt
	report.HostExecution = strings.EqualFold(report.SandboxRoute, "host") || strings.EqualFold(report.SandboxResolvedBackend, "host")
	report.FullAccessMode = false
	if report.HostExecution {
		report.Warnings = append(report.Warnings, "sandbox execution is using host route")
	}
	if report.SandboxInstallHint != "" {
		report.Warnings = append(report.Warnings, report.SandboxInstallHint)
	}
	if report.SandboxSetupError != "" {
		report.Warnings = append(report.Warnings, "Windows sandbox setup error: "+report.SandboxSetupError)
	}
	if report.SandboxWorkspaceSetupRequired && report.SandboxWorkspaceSetupReason != "" {
		report.Warnings = append(report.Warnings, "Windows sandbox workspace setup required: "+report.SandboxWorkspaceSetupReason)
	}
	if s.engine != nil && strings.TrimSpace(ref.SessionID) != "" {
		grants := s.engine.PermissionGrantSnapshot(ref)
		report.PermissionGrantCount = grants.Count
		report.PermissionGrantNetwork = grants.NetworkGranted
		report.PermissionReadRootCount = grants.ReadRootCount
		report.PermissionWriteRootCount = grants.WriteRootCount
	}
	if report.MissingAPIKey {
		report.Warnings = append(report.Warnings, "active model configuration is missing an API key")
	}
	if !report.ConfigPermissionsSecure && strings.TrimSpace(report.ConfigPath) != "" {
		report.Warnings = append(report.Warnings, "config file permissions are not secure")
	}

	if gw := s.CurrentGateway(); gw != nil {
		active := gw.ActiveTurns()
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
		fmt.Sprintf("session_mode: %s", firstNonEmpty(strings.TrimSpace(report.SessionMode), "auto-review")),
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
		fmt.Sprintf("sandbox_install_hint: %s", firstNonEmpty(strings.TrimSpace(report.SandboxInstallHint), "-")),
		fmt.Sprintf("sandbox_setup_required: %t", report.SandboxSetupRequired),
		fmt.Sprintf("sandbox_setup_error: %s", firstNonEmpty(strings.TrimSpace(report.SandboxSetupError), "-")),
		fmt.Sprintf("sandbox_setup_version: %d", report.SandboxSetupVersion),
		fmt.Sprintf("sandbox_setup_marker_current: %t", report.SandboxSetupMarkerCurrent),
		fmt.Sprintf("sandbox_setup_marker_reason: %s", firstNonEmpty(strings.TrimSpace(report.SandboxSetupMarkerReason), "-")),
		fmt.Sprintf("sandbox_setup_runner_hash: %s", firstNonEmpty(shortHash(report.SandboxSetupRunnerHash), "-")),
		fmt.Sprintf("sandbox_setup_policy_hash: %s", firstNonEmpty(shortHash(report.SandboxSetupPolicyHash), "-")),
		fmt.Sprintf("sandbox_setup_offline_user: %s", firstNonEmpty(strings.TrimSpace(report.SandboxSetupOfflineUser), "-")),
		fmt.Sprintf("sandbox_setup_online_user: %s", firstNonEmpty(strings.TrimSpace(report.SandboxSetupOnlineUser), "-")),
		fmt.Sprintf("sandbox_setup_owner_user: %s", firstNonEmpty(strings.TrimSpace(report.SandboxSetupOwnerUser), "-")),
		fmt.Sprintf("sandbox_setup_read_roots: %d", report.SandboxSetupReadRoots),
		fmt.Sprintf("sandbox_setup_write_roots: %d", report.SandboxSetupWriteRoots),
		fmt.Sprintf("sandbox_setup_deny_read: %d", report.SandboxSetupDenyRead),
		fmt.Sprintf("sandbox_setup_deny_write: %d", report.SandboxSetupDenyWrite),
		fmt.Sprintf("sandbox_security_summary: %s", firstNonEmpty(strings.TrimSpace(report.SandboxSecuritySummary), "-")),
		fmt.Sprintf("sandbox_global_setup_current: %t", report.SandboxGlobalSetupCurrent),
		fmt.Sprintf("sandbox_global_setup_required: %t", report.SandboxGlobalSetupRequired),
		fmt.Sprintf("sandbox_global_setup_reason: %s", firstNonEmpty(strings.TrimSpace(report.SandboxGlobalSetupReason), "-")),
		fmt.Sprintf("sandbox_workspace_setup_current: %t", report.SandboxWorkspaceSetupCurrent),
		fmt.Sprintf("sandbox_workspace_setup_required: %t", report.SandboxWorkspaceSetupRequired),
		fmt.Sprintf("sandbox_workspace_setup_reason: %s", firstNonEmpty(strings.TrimSpace(report.SandboxWorkspaceSetupReason), "-")),
		fmt.Sprintf("sandbox_workspace_setup_root: %s", firstNonEmpty(strings.TrimSpace(report.SandboxWorkspaceSetupRoot), "-")),
		fmt.Sprintf("sandbox_workspace_setup_write_roots: %d", report.SandboxWorkspaceSetupWriteRoots),
		fmt.Sprintf("sandbox_workspace_setup_policy_hash: %s", firstNonEmpty(shortHash(report.SandboxWorkspaceSetupPolicyHash), "-")),
		fmt.Sprintf("sandbox_workspace_setup_updated_at: %s", formatDoctorTime(report.SandboxWorkspaceSetupUpdatedAt)),
		fmt.Sprintf("host_execution: %t", report.HostExecution),
		fmt.Sprintf("full_access_mode: %t", report.FullAccessMode),
		fmt.Sprintf("permission_grant_count: %d", report.PermissionGrantCount),
		fmt.Sprintf("permission_grant_network: %t", report.PermissionGrantNetwork),
		fmt.Sprintf("permission_read_root_count: %d", report.PermissionReadRootCount),
		fmt.Sprintf("permission_write_root_count: %d", report.PermissionWriteRootCount),
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

func (s *Stack) resolveDoctorSessionRef(ctx context.Context, req DoctorRequest) session.SessionRef {
	if s == nil {
		return session.SessionRef{}
	}
	if strings.TrimSpace(req.SessionRef.SessionID) != "" {
		return req.SessionRef
	}
	if strings.TrimSpace(req.SessionID) != "" {
		return session.SessionRef{
			AppName:      s.AppName,
			UserID:       s.UserID,
			SessionID:    strings.TrimSpace(req.SessionID),
			WorkspaceKey: s.Workspace.Key,
		}
	}
	if strings.TrimSpace(req.BindingKey) != "" {
		if gw := s.CurrentGateway(); gw != nil {
			if ref, ok := gw.CurrentSession(req.BindingKey); ok {
				return ref
			}
		}
	}
	_ = ctx
	return session.SessionRef{}
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
	if cfg.AuthType == "" || cfg.AuthType == providers.AuthNone {
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

func shortHash(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

func formatDoctorTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func setupStatusEmpty(status sandboxport.SetupStatus) bool {
	return !status.Required &&
		strings.TrimSpace(status.Error) == "" &&
		len(status.Details) == 0 &&
		len(status.Counts) == 0 &&
		len(status.Checks) == 0
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

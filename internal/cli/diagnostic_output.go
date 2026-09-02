package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/internal/productpaths"
)

// doctorResult preserves the established CLI output contract while the data
// source remains the canonical Control status snapshot.
type doctorResult struct {
	GoVersion                       string                            `json:"go_version,omitempty"`
	GOOS                            string                            `json:"goos,omitempty"`
	GOARCH                          string                            `json:"goarch,omitempty"`
	StoreDir                        string                            `json:"store_dir,omitempty"`
	ConfigPath                      string                            `json:"config_path,omitempty"`
	ConfigDirMode                   string                            `json:"config_dir_mode,omitempty"`
	ConfigFileMode                  string                            `json:"config_file_mode,omitempty"`
	ConfigDirSecure                 bool                              `json:"config_dir_secure,omitempty"`
	ConfigFileSecure                bool                              `json:"config_file_secure,omitempty"`
	ConfigPermissionsSecure         bool                              `json:"config_permissions_secure,omitempty"`
	ServiceState                    string                            `json:"service_state,omitempty"`
	ServiceError                    string                            `json:"service_error,omitempty"`
	ServiceLogPath                  string                            `json:"service_log_path,omitempty"`
	SessionID                       string                            `json:"session_id,omitempty"`
	SessionMode                     string                            `json:"session_mode,omitempty"`
	PolicyProfile                   string                            `json:"policy_profile,omitempty"`
	ActiveModelAlias                string                            `json:"active_model_alias,omitempty"`
	ActiveProvider                  string                            `json:"active_provider,omitempty"`
	ActiveModel                     string                            `json:"active_model,omitempty"`
	MissingAPIKey                   bool                              `json:"missing_api_key,omitempty"`
	TokenSource                     string                            `json:"token_source,omitempty"`
	PersistedPlaintextToken         bool                              `json:"persisted_plaintext_token,omitempty"`
	SandboxRequestedBackend         string                            `json:"sandbox_requested_backend,omitempty"`
	SandboxResolvedBackend          string                            `json:"sandbox_resolved_backend,omitempty"`
	SandboxRoute                    string                            `json:"sandbox_route,omitempty"`
	SandboxFallbackReason           string                            `json:"sandbox_fallback_reason,omitempty"`
	SandboxInstallHint              string                            `json:"sandbox_install_hint,omitempty"`
	SandboxSetup                    *controlstatus.SandboxSetupStatus `json:"sandbox_setup,omitempty"`
	SandboxSetupRequired            bool                              `json:"sandbox_setup_required,omitempty"`
	SandboxSetupError               string                            `json:"sandbox_setup_error,omitempty"`
	SandboxSetupVersion             int                               `json:"sandbox_setup_version,omitempty"`
	SandboxSetupMarkerCurrent       bool                              `json:"sandbox_setup_marker_current,omitempty"`
	SandboxSetupMarkerReason        string                            `json:"sandbox_setup_marker_reason,omitempty"`
	SandboxSetupRunnerHash          string                            `json:"sandbox_setup_runner_hash,omitempty"`
	SandboxSetupPolicyHash          string                            `json:"sandbox_setup_policy_hash,omitempty"`
	SandboxSetupOfflineUser         string                            `json:"sandbox_setup_offline_user,omitempty"`
	SandboxSetupOnlineUser          string                            `json:"sandbox_setup_online_user,omitempty"`
	SandboxSetupOwnerUser           string                            `json:"sandbox_setup_owner_user,omitempty"`
	SandboxSetupReadRoots           int                               `json:"sandbox_setup_read_roots,omitempty"`
	SandboxSetupWriteRoots          int                               `json:"sandbox_setup_write_roots,omitempty"`
	SandboxSetupDenyRead            int                               `json:"sandbox_setup_deny_read,omitempty"`
	SandboxSetupDenyWrite           int                               `json:"sandbox_setup_deny_write,omitempty"`
	SandboxSecuritySummary          string                            `json:"sandbox_security_summary,omitempty"`
	SandboxGlobalSetupCurrent       bool                              `json:"sandbox_global_setup_current,omitempty"`
	SandboxGlobalSetupRequired      bool                              `json:"sandbox_global_setup_required,omitempty"`
	SandboxGlobalSetupReason        string                            `json:"sandbox_global_setup_reason,omitempty"`
	SandboxWorkspaceSetupCurrent    bool                              `json:"sandbox_workspace_setup_current,omitempty"`
	SandboxWorkspaceSetupRequired   bool                              `json:"sandbox_workspace_setup_required,omitempty"`
	SandboxWorkspaceSetupReason     string                            `json:"sandbox_workspace_setup_reason,omitempty"`
	SandboxWorkspaceSetupRoot       string                            `json:"sandbox_workspace_setup_root,omitempty"`
	SandboxWorkspaceSetupWriteRoots int                               `json:"sandbox_workspace_setup_write_roots,omitempty"`
	SandboxWorkspaceSetupPolicyHash string                            `json:"sandbox_workspace_setup_policy_hash,omitempty"`
	SandboxWorkspaceSetupUpdatedAt  time.Time                         `json:"sandbox_workspace_setup_updated_at,omitempty"`
	HostExecution                   bool                              `json:"host_execution,omitempty"`
	FullAccessMode                  bool                              `json:"full_access_mode,omitempty"`
	HasActiveTurn                   bool                              `json:"has_active_turn,omitempty"`
	ActiveTurnCount                 int                               `json:"active_turn_count,omitempty"`
	ActiveTurnSessions              []string                          `json:"active_turn_sessions,omitempty"`
	Warnings                        []string                          `json:"warnings,omitempty"`
}

func doctorResultFromStartupFailure(storeDir string, managed bool, err error) doctorResult {
	result := doctorResult{
		GoVersion:    runtime.Version(),
		GOOS:         runtime.GOOS,
		GOARCH:       runtime.GOARCH,
		StoreDir:     filepath.Clean(storeDir),
		ConfigPath:   filepath.Join(storeDir, "config.json"),
		ServiceState: "unavailable",
	}
	if managed {
		result.ServiceError = managedStartupDoctorCause(err)
		result.ServiceLogPath = filepath.Join(productpaths.ServiceLogDir(storeDir), localHostLogFilename)
		return result
	}
	if cause := userFacingHostBlocker(err); cause != "" {
		result.ServiceError = cause
	} else {
		result.ServiceError = "configured Control Host is unavailable"
	}
	return result
}

// sandboxStatusResult mirrors the established CamelCase sandbox command JSON
// shape while deriving every field from Control's normalized status model.
type sandboxStatusResult struct {
	RequestedBackend         string
	ResolvedBackend          string
	Route                    string
	FallbackReason           string
	InstallHint              string
	Setup                    controlstatus.SandboxSetupStatus
	SetupRequired            bool
	SetupError               string
	SetupVersion             int
	SetupMarkerCurrent       bool
	SetupMarkerReason        string
	SetupRunnerHash          string
	SetupPolicyHash          string
	SetupOfflineUser         string
	SetupOnlineUser          string
	SetupOwnerUser           string
	SetupReadRoots           int
	SetupWriteRoots          int
	SetupDenyRead            int
	SetupDenyWrite           int
	SecuritySummary          string
	FullAccessMode           bool
	GlobalSetupCurrent       bool
	GlobalSetupRequired      bool
	GlobalSetupReason        string
	WorkspaceSetupCurrent    bool
	WorkspaceSetupRequired   bool
	WorkspaceSetupReason     string
	WorkspaceSetupRoot       string
	WorkspaceSetupWriteRoots int
	WorkspaceSetupPolicyHash string
	WorkspaceSetupUpdatedAt  time.Time
}

type sandboxSetupDiagnostics struct {
	setup               controlstatus.SandboxSetupStatus
	present             bool
	required            bool
	err                 string
	version             int
	markerCurrent       bool
	markerReason        string
	runnerHash          string
	policyHash          string
	offlineUser         string
	onlineUser          string
	ownerUser           string
	readRoots           int
	writeRoots          int
	denyRead            int
	denyWrite           int
	globalCurrent       bool
	globalRequired      bool
	globalReason        string
	workspaceCurrent    bool
	workspaceRequired   bool
	workspaceReason     string
	workspaceRoot       string
	workspaceWriteRoots int
	workspacePolicyHash string
	workspaceUpdatedAt  time.Time
}

func writeDoctorResult(w io.Writer, format outputFormat, result doctorResult) error {
	switch format {
	case outputJSON:
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		return enc.Encode(result)
	default:
		_, err := fmt.Fprintln(w, formatDoctorResult(result))
		return err
	}
}

func doctorResultFromStatus(status controlstatus.StatusSnapshot) doctorResult {
	setup := sandboxSetupDiagnosticsFromStatus(status.SandboxStatus.Setup)
	var setupView *controlstatus.SandboxSetupStatus
	if setup.present {
		setupView = &setup.setup
	}
	policyProfile := strings.TrimSpace(status.Session.PolicyProfile)
	if policyProfile == "" && status.SandboxStatus.FullAccessMode {
		policyProfile = "danger-full-access"
	}
	return doctorResult{
		GoVersion:                       firstNonEmptyString(status.Diagnostics.GoVersion, runtime.Version()),
		GOOS:                            firstNonEmptyString(status.Diagnostics.GOOS, runtime.GOOS),
		GOARCH:                          firstNonEmptyString(status.Diagnostics.GOARCH, runtime.GOARCH),
		StoreDir:                        strings.TrimSpace(status.Session.StoreDir),
		ConfigPath:                      strings.TrimSpace(status.Diagnostics.ConfigPath),
		ConfigDirMode:                   strings.TrimSpace(status.Diagnostics.ConfigDirMode),
		ConfigFileMode:                  strings.TrimSpace(status.Diagnostics.ConfigFileMode),
		ConfigDirSecure:                 status.Diagnostics.ConfigDirSecure,
		ConfigFileSecure:                status.Diagnostics.ConfigFileSecure,
		ConfigPermissionsSecure:         status.Diagnostics.ConfigPermissionsSecure,
		SessionID:                       strings.TrimSpace(status.Session.ID),
		SessionMode:                     strings.TrimSpace(status.Session.SessionMode),
		PolicyProfile:                   policyProfile,
		ActiveModelAlias:                firstNonEmptyString(status.ModelStatus.Alias, status.ModelStatus.Display),
		ActiveProvider:                  strings.TrimSpace(status.ModelStatus.Provider),
		ActiveModel:                     strings.TrimSpace(status.ModelStatus.Name),
		MissingAPIKey:                   status.ModelStatus.MissingAPIKey,
		TokenSource:                     strings.TrimSpace(status.Diagnostics.TokenSource),
		PersistedPlaintextToken:         status.Diagnostics.PersistedPlaintextToken,
		SandboxRequestedBackend:         strings.TrimSpace(status.SandboxStatus.RequestedBackend),
		SandboxResolvedBackend:          strings.TrimSpace(status.SandboxStatus.ResolvedBackend),
		SandboxRoute:                    strings.TrimSpace(status.SandboxStatus.Route),
		SandboxFallbackReason:           strings.TrimSpace(status.SandboxStatus.FallbackReason),
		SandboxInstallHint:              strings.TrimSpace(status.SandboxStatus.InstallHint),
		SandboxSetup:                    setupView,
		SandboxSetupRequired:            setup.required,
		SandboxSetupError:               setup.err,
		SandboxSetupVersion:             setup.version,
		SandboxSetupMarkerCurrent:       setup.markerCurrent,
		SandboxSetupMarkerReason:        setup.markerReason,
		SandboxSetupRunnerHash:          setup.runnerHash,
		SandboxSetupPolicyHash:          setup.policyHash,
		SandboxSetupOfflineUser:         setup.offlineUser,
		SandboxSetupOnlineUser:          setup.onlineUser,
		SandboxSetupOwnerUser:           setup.ownerUser,
		SandboxSetupReadRoots:           setup.readRoots,
		SandboxSetupWriteRoots:          setup.writeRoots,
		SandboxSetupDenyRead:            setup.denyRead,
		SandboxSetupDenyWrite:           setup.denyWrite,
		SandboxSecuritySummary:          strings.TrimSpace(status.SandboxStatus.SecuritySummary),
		SandboxGlobalSetupCurrent:       setup.globalCurrent,
		SandboxGlobalSetupRequired:      setup.globalRequired,
		SandboxGlobalSetupReason:        setup.globalReason,
		SandboxWorkspaceSetupCurrent:    setup.workspaceCurrent,
		SandboxWorkspaceSetupRequired:   setup.workspaceRequired,
		SandboxWorkspaceSetupReason:     setup.workspaceReason,
		SandboxWorkspaceSetupRoot:       setup.workspaceRoot,
		SandboxWorkspaceSetupWriteRoots: setup.workspaceWriteRoots,
		SandboxWorkspaceSetupPolicyHash: setup.workspacePolicyHash,
		SandboxWorkspaceSetupUpdatedAt:  setup.workspaceUpdatedAt,
		HostExecution:                   status.SandboxStatus.HostExecution,
		FullAccessMode:                  status.SandboxStatus.FullAccessMode,
		HasActiveTurn:                   status.Runtime.Running,
		ActiveTurnCount:                 status.Runtime.ActiveJobs,
		ActiveTurnSessions:              append([]string(nil), status.Runtime.ActiveSessions...),
		Warnings:                        append([]string(nil), status.Diagnostics.Warnings...),
	}
}

func formatDoctorResult(report doctorResult) string {
	lines := make([]string, 0, 48)
	if serviceError := strings.TrimSpace(report.ServiceError); serviceError != "" {
		lines = append(lines, "blocked: "+serviceError)
		if logPath := strings.TrimSpace(report.ServiceLogPath); logPath != "" {
			lines = append(lines, "fix: inspect the private service log at "+logPath)
		}
		lines = append(lines, "")
	}
	if blocker := firstNonEmptyString(report.SandboxFallbackReason, report.SandboxSetupError, report.SandboxGlobalSetupReason, report.SandboxWorkspaceSetupReason); blocker != "" {
		lines = append(lines, "blocked: "+blocker)
		if hint := strings.TrimSpace(report.SandboxInstallHint); hint != "" {
			lines = append(lines, "fix: "+hint)
		}
		lines = append(lines, "")
	}
	lines = append(lines,
		fmt.Sprintf("go_version: %s", firstNonEmptyString(report.GoVersion, "unknown")),
		fmt.Sprintf("platform: %s/%s", firstNonEmptyString(report.GOOS, "unknown"), firstNonEmptyString(report.GOARCH, "unknown")),
		fmt.Sprintf("store_dir: %s", firstNonEmptyString(report.StoreDir, "-")),
		fmt.Sprintf("config_path: %s", firstNonEmptyString(report.ConfigPath, "-")),
	)
	if report.ServiceState != "" || report.ServiceError != "" || report.ServiceLogPath != "" {
		lines = append(lines,
			fmt.Sprintf("service_state: %s", firstNonEmptyString(report.ServiceState, "-")),
			fmt.Sprintf("service_error: %s", firstNonEmptyString(report.ServiceError, "-")),
			fmt.Sprintf("service_log_path: %s", firstNonEmptyString(report.ServiceLogPath, "-")),
		)
	}
	lines = append(lines,
		fmt.Sprintf("config_permissions_secure: %t", report.ConfigPermissionsSecure),
		fmt.Sprintf("config_dir_mode: %s", firstNonEmptyString(report.ConfigDirMode, "-")),
		fmt.Sprintf("config_file_mode: %s", firstNonEmptyString(report.ConfigFileMode, "-")),
		fmt.Sprintf("session_id: %s", firstNonEmptyString(report.SessionID, "-")),
		fmt.Sprintf("session_mode: %s", firstNonEmptyString(report.SessionMode, "auto-review")),
		fmt.Sprintf("policy_profile: %s", firstNonEmptyString(report.PolicyProfile, "workspace-write")),
		fmt.Sprintf("active_model_alias: %s", firstNonEmptyString(report.ActiveModelAlias, "-")),
		fmt.Sprintf("active_provider: %s", firstNonEmptyString(report.ActiveProvider, "-")),
		fmt.Sprintf("active_model: %s", firstNonEmptyString(report.ActiveModel, "-")),
		fmt.Sprintf("missing_api_key: %t", report.MissingAPIKey),
		fmt.Sprintf("token_source: %s", firstNonEmptyString(report.TokenSource, "-")),
		fmt.Sprintf("persisted_plaintext_token: %t", report.PersistedPlaintextToken),
		fmt.Sprintf("sandbox_requested_backend: %s", firstNonEmptyString(report.SandboxRequestedBackend, "-")),
		fmt.Sprintf("sandbox_resolved_backend: %s", firstNonEmptyString(report.SandboxResolvedBackend, "-")),
		fmt.Sprintf("sandbox_route: %s", firstNonEmptyString(report.SandboxRoute, "-")),
		fmt.Sprintf("sandbox_repair_reason: %s", firstNonEmptyString(report.SandboxFallbackReason, "-")),
		fmt.Sprintf("sandbox_install_hint: %s", firstNonEmptyString(report.SandboxInstallHint, "-")),
		fmt.Sprintf("sandbox_setup_required: %t", report.SandboxSetupRequired),
		fmt.Sprintf("sandbox_setup_error: %s", firstNonEmptyString(report.SandboxSetupError, "-")),
		fmt.Sprintf("sandbox_setup_version: %d", report.SandboxSetupVersion),
		fmt.Sprintf("sandbox_setup_marker_current: %t", report.SandboxSetupMarkerCurrent),
		fmt.Sprintf("sandbox_setup_marker_reason: %s", firstNonEmptyString(report.SandboxSetupMarkerReason, "-")),
		fmt.Sprintf("sandbox_setup_runner_hash: %s", firstNonEmptyString(shortDoctorHash(report.SandboxSetupRunnerHash), "-")),
		fmt.Sprintf("sandbox_setup_policy_hash: %s", firstNonEmptyString(shortDoctorHash(report.SandboxSetupPolicyHash), "-")),
		fmt.Sprintf("sandbox_setup_offline_user: %s", firstNonEmptyString(report.SandboxSetupOfflineUser, "-")),
		fmt.Sprintf("sandbox_setup_online_user: %s", firstNonEmptyString(report.SandboxSetupOnlineUser, "-")),
		fmt.Sprintf("sandbox_setup_owner_user: %s", firstNonEmptyString(report.SandboxSetupOwnerUser, "-")),
		fmt.Sprintf("sandbox_setup_read_roots: %d", report.SandboxSetupReadRoots),
		fmt.Sprintf("sandbox_setup_write_roots: %d", report.SandboxSetupWriteRoots),
		fmt.Sprintf("sandbox_setup_deny_read: %d", report.SandboxSetupDenyRead),
		fmt.Sprintf("sandbox_setup_deny_write: %d", report.SandboxSetupDenyWrite),
		fmt.Sprintf("sandbox_security_summary: %s", firstNonEmptyString(report.SandboxSecuritySummary, "-")),
		fmt.Sprintf("sandbox_global_setup_current: %t", report.SandboxGlobalSetupCurrent),
		fmt.Sprintf("sandbox_global_setup_required: %t", report.SandboxGlobalSetupRequired),
		fmt.Sprintf("sandbox_global_setup_reason: %s", firstNonEmptyString(report.SandboxGlobalSetupReason, "-")),
		fmt.Sprintf("sandbox_workspace_setup_current: %t", report.SandboxWorkspaceSetupCurrent),
		fmt.Sprintf("sandbox_workspace_setup_required: %t", report.SandboxWorkspaceSetupRequired),
		fmt.Sprintf("sandbox_workspace_setup_reason: %s", firstNonEmptyString(report.SandboxWorkspaceSetupReason, "-")),
		fmt.Sprintf("sandbox_workspace_setup_root: %s", firstNonEmptyString(report.SandboxWorkspaceSetupRoot, "-")),
		fmt.Sprintf("sandbox_workspace_setup_write_roots: %d", report.SandboxWorkspaceSetupWriteRoots),
		fmt.Sprintf("sandbox_workspace_setup_policy_hash: %s", firstNonEmptyString(shortDoctorHash(report.SandboxWorkspaceSetupPolicyHash), "-")),
		fmt.Sprintf("sandbox_workspace_setup_updated_at: %s", formatDoctorTime(report.SandboxWorkspaceSetupUpdatedAt)),
		fmt.Sprintf("host_execution: %t", report.HostExecution),
		fmt.Sprintf("full_access_mode: %t", report.FullAccessMode),
		fmt.Sprintf("has_active_turn: %t", report.HasActiveTurn),
		fmt.Sprintf("active_turn_count: %d", report.ActiveTurnCount),
		fmt.Sprintf("active_turn_sessions: %s", firstNonEmptyString(strings.Join(report.ActiveTurnSessions, ", "), "-")),
	)
	for _, warning := range report.Warnings {
		if warning = strings.TrimSpace(warning); warning != "" {
			lines = append(lines, "warning: "+warning)
		}
	}
	return strings.Join(lines, "\n")
}

func sandboxSetupDiagnosticsFromStatus(status controlstatus.SandboxSetupStatus) sandboxSetupDiagnostics {
	setup := controlstatus.CloneSandboxSetupStatus(status)
	global, _ := setup.Check("global")
	workspace, _ := setup.Check("workspace")
	return sandboxSetupDiagnostics{
		setup:               setup,
		present:             setup.Required || strings.TrimSpace(setup.Error) != "" || len(setup.Details) > 0 || len(setup.Counts) > 0 || len(setup.Checks) > 0,
		required:            setup.Required,
		err:                 firstNonEmptyString(setup.Error, global.Error, workspace.Error),
		version:             global.Version,
		markerCurrent:       global.Current,
		markerReason:        strings.TrimSpace(global.Reason),
		runnerHash:          strings.TrimSpace(global.Details["runner_hash"]),
		policyHash:          firstNonEmptyString(global.Details["policy_hash"], workspace.Details["policy_hash"]),
		offlineUser:         strings.TrimSpace(global.Details["offline_user"]),
		onlineUser:          strings.TrimSpace(global.Details["online_user"]),
		ownerUser:           strings.TrimSpace(global.Details["owner_user"]),
		readRoots:           workspace.Counts["read_roots"],
		writeRoots:          workspace.Counts["write_roots"],
		denyRead:            workspace.Counts["deny_read"],
		denyWrite:           workspace.Counts["deny_write"],
		globalCurrent:       global.Current,
		globalRequired:      global.Required,
		globalReason:        strings.TrimSpace(global.Reason),
		workspaceCurrent:    workspace.Current,
		workspaceRequired:   workspace.Required,
		workspaceReason:     strings.TrimSpace(workspace.Reason),
		workspaceRoot:       strings.TrimSpace(workspace.Root),
		workspaceWriteRoots: workspace.Counts["write_roots"],
		workspacePolicyHash: strings.TrimSpace(workspace.Details["policy_hash"]),
		workspaceUpdatedAt:  workspace.UpdatedAt,
	}
}

func shortDoctorHash(value string) string {
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

func writeSandboxStatusResult(w io.Writer, format outputFormat, result sandboxStatusResult) error {
	switch format {
	case outputJSON:
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		return enc.Encode(result)
	default:
		_, err := fmt.Fprintln(w, formatSandboxStatus(result))
		return err
	}
}

func sandboxStatusResultFromStatus(status controlstatus.StatusSandbox) sandboxStatusResult {
	setup := sandboxSetupDiagnosticsFromStatus(status.Setup)
	return sandboxStatusResult{
		RequestedBackend:         strings.TrimSpace(status.RequestedBackend),
		ResolvedBackend:          strings.TrimSpace(status.ResolvedBackend),
		Route:                    strings.TrimSpace(status.Route),
		FallbackReason:           strings.TrimSpace(status.FallbackReason),
		InstallHint:              strings.TrimSpace(status.InstallHint),
		Setup:                    setup.setup,
		SetupRequired:            setup.required,
		SetupError:               setup.err,
		SetupVersion:             setup.version,
		SetupMarkerCurrent:       setup.markerCurrent,
		SetupMarkerReason:        setup.markerReason,
		SetupRunnerHash:          setup.runnerHash,
		SetupPolicyHash:          setup.policyHash,
		SetupOfflineUser:         setup.offlineUser,
		SetupOnlineUser:          setup.onlineUser,
		SetupOwnerUser:           setup.ownerUser,
		SetupReadRoots:           setup.readRoots,
		SetupWriteRoots:          setup.writeRoots,
		SetupDenyRead:            setup.denyRead,
		SetupDenyWrite:           setup.denyWrite,
		SecuritySummary:          strings.TrimSpace(status.SecuritySummary),
		FullAccessMode:           status.FullAccessMode,
		GlobalSetupCurrent:       setup.globalCurrent,
		GlobalSetupRequired:      setup.globalRequired,
		GlobalSetupReason:        setup.globalReason,
		WorkspaceSetupCurrent:    setup.workspaceCurrent,
		WorkspaceSetupRequired:   setup.workspaceRequired,
		WorkspaceSetupReason:     setup.workspaceReason,
		WorkspaceSetupRoot:       setup.workspaceRoot,
		WorkspaceSetupWriteRoots: setup.workspaceWriteRoots,
		WorkspaceSetupPolicyHash: setup.workspacePolicyHash,
		WorkspaceSetupUpdatedAt:  setup.workspaceUpdatedAt,
	}
}

func formatSandboxStatus(status sandboxStatusResult) string {
	globalSetup, _ := status.Setup.Check("global")
	setupRequired := status.Setup.Required || status.SetupRequired
	setupError := firstNonEmptyString(status.Setup.Error, globalSetup.Error, status.SetupError)
	setupMarkerCurrent := globalSetup.Current || status.SetupMarkerCurrent
	setupMarkerReason := firstNonEmptyString(globalSetup.Reason, status.SetupMarkerReason)
	lines := []string{
		fmt.Sprintf("sandbox_requested_backend: %s", firstNonEmptyString(strings.TrimSpace(status.RequestedBackend), "-")),
		fmt.Sprintf("sandbox_resolved_backend: %s", firstNonEmptyString(strings.TrimSpace(status.ResolvedBackend), "-")),
		fmt.Sprintf("sandbox_route: %s", firstNonEmptyString(strings.TrimSpace(status.Route), "-")),
		fmt.Sprintf("sandbox_full_access_mode: %t", status.FullAccessMode),
		fmt.Sprintf("sandbox_security_summary: %s", firstNonEmptyString(strings.TrimSpace(status.SecuritySummary), "-")),
		fmt.Sprintf("sandbox_setup_required: %t", setupRequired),
		fmt.Sprintf("sandbox_setup_error: %s", firstNonEmptyString(strings.TrimSpace(setupError), "-")),
		fmt.Sprintf("sandbox_setup_marker_current: %t", setupMarkerCurrent),
		fmt.Sprintf("sandbox_setup_marker_reason: %s", firstNonEmptyString(strings.TrimSpace(setupMarkerReason), "-")),
	}
	return strings.Join(lines, "\n")
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

package gatewayapp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

type doctorSessionMigrationReporter struct {
	session.Service
	report sessionfile.MigrationReport
}

func (r doctorSessionMigrationReporter) MigrationReport() sessionfile.MigrationReport {
	return r.report
}

func TestDoctorReportsObservedLegacySessionParticipantDrop(t *testing.T) {
	root := t.TempDir()
	workdir := t.TempDir()
	stack, err := newGatewayAppTestStack(t, Config{
		AppName: "caelis", UserID: "doctor-session-migration", StoreDir: root,
		WorkspaceKey: workdir, WorkspaceCWD: workdir, Assembly: assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	stack.Sessions = doctorSessionMigrationReporter{
		Service: stack.Sessions,
		report: sessionfile.MigrationReport{
			FromVersion: 1,
			Dropped: []sessionfile.MigrationDrop{{
				SessionRef: session.SessionRef{SessionID: "legacy-session"},
				Category:   "acp_participant",
				Identity:   "participants[2]",
				Reason:     "invalid_sealed_placement",
			}},
		},
	}

	report, err := stack.Doctor(context.Background(), DoctorRequest{})
	if err != nil {
		t.Fatalf("Doctor() error = %v", err)
	}
	warnings := strings.Join(report.Warnings, "\n")
	for _, want := range []string{
		"legacy Session document version 1 skipped 1 unsafe participant binding",
		"legacy Session legacy-session skipped participants[2]: category=acp_participant reason=invalid_sealed_placement",
	} {
		if !strings.Contains(warnings, want) {
			t.Fatalf("Doctor warnings = %q, want %q", warnings, want)
		}
	}
}

func TestDoctorReportsLossyLegacyMigrationWithoutLeakingRawConfig(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workdir := t.TempDir()
	const secret = "doctor-legacy-secret"
	raw := `{
  "models": {"configs": [{
    "provider": "openai-codex",
    "model": "gpt-5.6",
    "credential_ref": "codex-oauth",
    "token": "` + secret + `",
    "persist_token": true,
    "reasoning_mode": "effort",
    "reasoning_levels": ["low", "high"],
    "default_reasoning_effort": "high"
  }]},
  "agent_roster": {"discoveries": [{
    "connection_id": "missing",
    "models": [{"id": "remote", "name": "Remote"}]
  }]},
  "delegation": {"bindings": [{
    "profile": "orbit",
    "target": "agent",
    "agent_id": "missing"
  }]}
}`
	configPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	stack, err := newGatewayAppTestStack(t, Config{
		AppName:      "caelis",
		UserID:       "doctor-migration-test",
		StoreDir:     root,
		WorkspaceKey: workdir,
		WorkspaceCWD: workdir,
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	report, err := stack.Doctor(ctx, DoctorRequest{})
	if err != nil {
		t.Fatalf("Doctor() error = %v", err)
	}
	warnings := strings.Join(report.Warnings, "\n")
	for _, want := range []string{
		"original backed up at " + configPath + ".v1.bak",
		"agent_roster.discoveries[0]",
		"delegation.bindings[0]",
	} {
		if !strings.Contains(warnings, want) {
			t.Fatalf("Doctor warnings = %q, want %q", warnings, want)
		}
	}
	if strings.Contains(warnings, secret) {
		t.Fatalf("Doctor warnings leaked legacy credential: %q", warnings)
	}
	backup, err := os.ReadFile(configPath + ".v1.bak")
	if err != nil || string(backup) != raw {
		t.Fatalf("legacy backup = %q, %v; want original bytes", backup, err)
	}
}

func TestDoctorReportFindsAPIKeyThroughCredentialReferenceAfterReload(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workdir := t.TempDir()

	stack, err := newGatewayAppTestStack(t, Config{
		AppName:      "caelis",
		UserID:       "doctor-test",
		StoreDir:     root,
		WorkspaceKey: workdir,
		WorkspaceCWD: workdir,
		ApprovalMode: "auto-review",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	session, err := stack.StartSession(ctx, "doctor-session", "cli-headless")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	profile, err := stack.Connect(ModelConfig{
		Provider: "minimax",
		API:      providers.APIAnthropicCompatible,
		Model:    "MiniMax-M1",
		Token:    "super-secret-token",
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if err := stack.UseModel(ctx, session.SessionRef, profile.Backend.Provider.ModelConfigID); err != nil {
		t.Fatalf("UseModel() error = %v", err)
	}
	if _, err := stack.SetSessionMode(ctx, session.SessionRef, "manual"); err != nil {
		t.Fatalf("SetSessionMode() error = %v", err)
	}

	reloaded, err := newGatewayAppTestStack(t, Config{
		AppName:      "caelis",
		UserID:       "doctor-test",
		StoreDir:     root,
		WorkspaceKey: workdir,
		WorkspaceCWD: workdir,
		ApprovalMode: "auto-review",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack(reloaded) error = %v", err)
	}

	report, err := reloaded.Doctor(ctx, DoctorRequest{
		SessionID: session.SessionID,
	})
	if err != nil {
		t.Fatalf("Doctor() error = %v", err)
	}
	if report.ActiveProvider != "minimax" || report.ActiveModel != "MiniMax-M1" {
		t.Fatalf("Doctor() provider/model = %q/%q, want minimax/MiniMax-M1", report.ActiveProvider, report.ActiveModel)
	}
	if report.MissingAPIKey {
		t.Fatal("Doctor().MissingAPIKey = true, want credential-store reference to remain available")
	}
	if report.SessionMode != "manual" {
		t.Fatalf("Doctor().SessionMode = %q, want manual", report.SessionMode)
	}
	if report.FullAccessMode {
		t.Fatal("Doctor().FullAccessMode = true, want false after mode simplification")
	}

	text := FormatDoctorText(report)
	if strings.Contains(text, "super-secret-token") {
		t.Fatalf("FormatDoctorText() leaked token: %q", text)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal(report) error = %v", err)
	}
	if strings.Contains(string(data), "super-secret-token") {
		t.Fatalf("Doctor JSON leaked token: %s", data)
	}
}

func TestDoctorReportUsesConfiguredModeWithoutSession(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workdir := t.TempDir()

	stack, err := newGatewayAppTestStack(t, Config{
		AppName:      "caelis",
		UserID:       "doctor-test",
		StoreDir:     root,
		WorkspaceKey: workdir,
		WorkspaceCWD: workdir,
		ApprovalMode: "manual",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}

	report, err := stack.Doctor(ctx, DoctorRequest{})
	if err != nil {
		t.Fatalf("Doctor() error = %v", err)
	}
	if report.SessionID != "" {
		t.Fatalf("Doctor().SessionID = %q, want empty", report.SessionID)
	}
	if report.SessionMode != "manual" {
		t.Fatalf("Doctor().SessionMode = %q, want manual", report.SessionMode)
	}
}

func TestDoctorReportTableDrivenTokenSourceAndLeakSafety(t *testing.T) {
	tests := []struct {
		name       string
		cfg        ModelConfig
		envValue   string
		wantSource string
		wantMiss   bool
	}{
		{
			name: "managed credential",
			cfg: ModelConfig{
				Provider:      "openai-codex",
				API:           providers.APIOpenAICodex,
				Model:         "gpt-5.4",
				CredentialRef: "codex-oauth",
			},
			wantSource: "credential:codex-oauth",
			wantMiss:   false,
		},
		{
			name: "env token",
			cfg: ModelConfig{
				Provider: "deepseek",
				API:      providers.APIDeepSeek,
				Model:    "deepseek-v4-pro",
				TokenEnv: "CAELIS_DOCTOR_TOKEN",
			},
			envValue:   "env-secret",
			wantSource: "env:CAELIS_DOCTOR_TOKEN",
			wantMiss:   false,
		},
		{
			name: "memory token",
			cfg: ModelConfig{
				Provider: "deepseek",
				API:      providers.APIDeepSeek,
				Model:    "deepseek-v4-pro",
				Token:    "memory-secret",
			},
			wantSource: "memory",
			wantMiss:   false,
		},
		{
			name: "missing token",
			cfg: ModelConfig{
				Provider: "deepseek",
				API:      providers.APIDeepSeek,
				Model:    "deepseek-v4-pro",
				TokenEnv: "CAELIS_DOCTOR_TOKEN_MISSING",
			},
			wantSource: "env:CAELIS_DOCTOR_TOKEN_MISSING",
			wantMiss:   true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if tt.cfg.TokenEnv != "" {
				t.Setenv(tt.cfg.TokenEnv, tt.envValue)
			}
			if got := modelConfigTokenSource(tt.cfg); got != tt.wantSource {
				t.Fatalf("modelConfigTokenSource() = %q, want %q", got, tt.wantSource)
			}
			if got := modelConfigMissingAPIKey(tt.cfg); got != tt.wantMiss {
				t.Fatalf("modelConfigMissingAPIKey() = %v, want %v", got, tt.wantMiss)
			}
			out := FormatDoctorText(DoctorReport{
				ActiveProvider: tt.cfg.Provider,
				ActiveModel:    tt.cfg.Model,
				TokenSource:    modelConfigTokenSource(tt.cfg),
				MissingAPIKey:  modelConfigMissingAPIKey(tt.cfg),
			})
			if tt.cfg.Token != "" && strings.Contains(out, tt.cfg.Token) {
				t.Fatalf("FormatDoctorText() leaked token: %q", out)
			}
			if tt.envValue != "" && strings.Contains(out, tt.envValue) {
				t.Fatalf("FormatDoctorText() leaked env value: %q", out)
			}
		})
	}
}

func TestFormatDoctorTextIncludesSandboxSetupDiagnostics(t *testing.T) {
	report := DoctorReport{
		SandboxSetupRequired:      true,
		SandboxSetupVersion:       1,
		SandboxSetupMarkerCurrent: false,
		SandboxSetupMarkerReason:  "setup marker missing",
		SandboxSetupRunnerHash:    "1234567890abcdef",
		SandboxSetupPolicyHash:    "abcdef1234567890",
		SandboxSetupOfflineUser:   "CaelisSandboxOffline",
		SandboxSetupOnlineUser:    "CaelisSandboxOnline",
		SandboxSetupReadRoots:     5,
		SandboxSetupWriteRoots:    2,
		SandboxSetupDenyRead:      3,
		SandboxSetupDenyWrite:     4,
	}
	out := FormatDoctorText(report)
	for _, want := range []string{
		"sandbox_setup_required: true",
		"sandbox_setup_version: 1",
		"sandbox_setup_marker_current: false",
		"sandbox_setup_marker_reason: setup marker missing",
		"sandbox_setup_runner_hash: 1234567890ab",
		"sandbox_setup_policy_hash: abcdef123456",
		"sandbox_setup_offline_user: CaelisSandboxOffline",
		"sandbox_setup_online_user: CaelisSandboxOnline",
		"sandbox_setup_read_roots: 5",
		"sandbox_setup_write_roots: 2",
		"sandbox_setup_deny_read: 3",
		"sandbox_setup_deny_write: 4",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("FormatDoctorText() = %q, want %q", out, want)
		}
	}
}

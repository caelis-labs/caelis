package gatewayapp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdkproviders "github.com/OnslaughtSnail/caelis/sdk/model/providers"
	sdkplugin "github.com/OnslaughtSnail/caelis/sdk/plugin"
)

func TestDoctorReportFlagsMissingAPIKeyAfterRedactedPersistence(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workdir := t.TempDir()

	stack, err := newGatewayAppTestStack(t, Config{
		AppName:        "caelis",
		UserID:         "doctor-test",
		StoreDir:       root,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "default",
		Assembly:       sdkplugin.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	session, err := stack.StartSession(ctx, "doctor-session", "cli-headless")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	alias, err := stack.Connect(ModelConfig{
		Provider: "minimax",
		API:      sdkproviders.APIAnthropicCompatible,
		Model:    "MiniMax-M1",
		Token:    "super-secret-token",
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if err := stack.UseModel(ctx, session.SessionRef, alias); err != nil {
		t.Fatalf("UseModel() error = %v", err)
	}
	if _, err := stack.SetSessionMode(ctx, session.SessionRef, "full_access"); err != nil {
		t.Fatalf("SetSessionMode() error = %v", err)
	}

	reloaded, err := newGatewayAppTestStack(t, Config{
		AppName:        "caelis",
		UserID:         "doctor-test",
		StoreDir:       root,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "default",
		Assembly:       sdkplugin.ResolvedAssembly{},
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
	if !report.MissingAPIKey {
		t.Fatal("Doctor().MissingAPIKey = false, want true after token is redacted from persisted config")
	}
	if !report.FullAccessMode {
		t.Fatal("Doctor().FullAccessMode = false, want true")
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

func TestDoctorReportTableDrivenTokenSourceAndLeakSafety(t *testing.T) {
	tests := []struct {
		name       string
		cfg        ModelConfig
		envValue   string
		wantSource string
		wantMiss   bool
	}{
		{
			name: "env token",
			cfg: ModelConfig{
				Provider: "deepseek",
				API:      sdkproviders.APIDeepSeek,
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
				API:      sdkproviders.APIDeepSeek,
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
				API:      sdkproviders.APIDeepSeek,
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

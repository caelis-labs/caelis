package assembly_test

import (
	"context"
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/memory"
	bridgeassembly "github.com/caelis-labs/caelis/internal/acpagentbridge/assembly"
	assemblyapi "github.com/caelis-labs/caelis/internal/controlassembly"
	acp "github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestProvidersFromAssemblyModeAndConfig(t *testing.T) {
	sessions := inmemory.NewStore(inmemory.Config{})
	started, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/ws-1",
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	assembly := assemblyapi.ResolvedAssembly{
		Modes: []assemblyapi.ModeConfig{
			{ID: "default", Name: "Default"},
			{ID: "plan", Name: "Plan"},
		},
		Configs: []assemblyapi.ConfigOption{{
			ID:           "effort",
			Name:         "Effort",
			DefaultValue: "balanced",
			Options: []assemblyapi.ConfigSelectOption{
				{Value: "balanced", Name: "Balanced"},
				{Value: "deep", Name: "Deep"},
			},
		}},
	}

	providers := bridgeassembly.ProvidersFromAssembly(bridgeassembly.ProviderConfig{
		Assembly: assembly,
		Sessions: sessions,
		AppName:  "caelis",
		UserID:   "user-1",
	})
	if providers.Modes == nil || providers.ModeWriter == nil || providers.Config == nil || providers.ConfigWriter == nil {
		t.Fatalf("ProvidersFromAssembly() = %#v, want all capabilities", providers)
	}

	session, err := sessions.Session(context.Background(), started.SessionRef)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}

	state, err := providers.Modes.SessionModes(context.Background(), session)
	if err != nil {
		t.Fatalf("SessionModes() error = %v", err)
	}
	if got := state.CurrentModeID; got != "default" {
		t.Fatalf("CurrentModeID = %q, want %q", got, "default")
	}

	if _, err := providers.ModeWriter.SetSessionMode(context.Background(), acpsdk.SetSessionModeRequest{
		SessionId: acpsdk.SessionId(session.SessionID),
		ModeId:    "plan",
	}); err != nil {
		t.Fatalf("SetSessionMode() error = %v", err)
	}

	state, err = providers.Modes.SessionModes(context.Background(), session)
	if err != nil {
		t.Fatalf("SessionModes() after set error = %v", err)
	}
	if got := state.CurrentModeID; got != "plan" {
		t.Fatalf("CurrentModeID after set = %q, want %q", got, "plan")
	}

	options, err := providers.Config.SessionConfigOptions(context.Background(), session)
	if err != nil {
		t.Fatalf("SessionConfigOptions() error = %v", err)
	}
	if got, want := len(options), 1; got != want {
		t.Fatalf("len(SessionConfigOptions) = %d, want %d", got, want)
	}
	if got := options[0].CurrentValue; got != "balanced" {
		t.Fatalf("default config value = %#v, want balanced", got)
	}

	resp, err := providers.ConfigWriter.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
		SessionID: session.SessionID,
		ConfigID:  "effort",
		Value:     "deep",
	})
	if err != nil {
		t.Fatalf("SetSessionConfigOption() error = %v", err)
	}
	if got := resp.ConfigOptions[0].CurrentValue; got != "deep" {
		t.Fatalf("updated config value = %#v, want deep", got)
	}
}

func TestProvidersFromAssemblyEmptyPreservesAbsentCapabilities(t *testing.T) {
	providers := bridgeassembly.ProvidersFromAssembly(bridgeassembly.ProviderConfig{})
	if providers.Modes != nil || providers.ModeWriter != nil || providers.Config != nil || providers.ConfigWriter != nil {
		t.Fatalf("ProvidersFromAssembly(empty) = %#v, want no capabilities", providers)
	}
}

func TestSkillBundlesNormalizeNamespaceAndDropEmptyRoots(t *testing.T) {
	assembly := assemblyapi.ResolvedAssembly{
		Skills: []assemblyapi.SkillBundle{
			{
				Plugin:   "plugin-a",
				Root:     "/tmp/a",
				Disabled: []string{" alpha ", "beta"},
			},
			{
				Plugin:    "plugin-b",
				Namespace: "custom",
				Root:      " /tmp/b ",
			},
			{
				Plugin: "ignored",
				Root:   "   ",
			},
		},
	}

	bundles := bridgeassembly.SkillBundles(assembly)
	if got, want := len(bundles), 2; got != want {
		t.Fatalf("len(SkillBundles) = %d, want %d", got, want)
	}
	if got := bundles[0].Namespace; got != "plugin-a" {
		t.Fatalf("bundle[0].Namespace = %q, want plugin name default", got)
	}
	if got := bundles[0].Disabled[0]; got != "alpha" {
		t.Fatalf("bundle[0].Disabled[0] = %q, want trimmed value", got)
	}
	if got := bundles[1].Namespace; got != "custom" {
		t.Fatalf("bundle[1].Namespace = %q, want explicit namespace", got)
	}
}

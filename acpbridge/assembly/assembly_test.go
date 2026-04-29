package assembly_test

import (
	"context"
	"testing"

	"github.com/OnslaughtSnail/caelis/acp"
	bridgeassembly "github.com/OnslaughtSnail/caelis/acpbridge/assembly"
	sdkplugin "github.com/OnslaughtSnail/caelis/sdk/plugin"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	"github.com/OnslaughtSnail/caelis/sdk/session/inmemory"
)

func TestProvidersFromAssemblyModeAndConfig(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	started, err := sessions.StartSession(context.Background(), sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: sdksession.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/ws-1",
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	assembly := sdkplugin.ResolvedAssembly{
		Modes: []sdkplugin.ModeConfig{
			{ID: "default", Name: "Default"},
			{ID: "plan", Name: "Plan"},
		},
		Configs: []sdkplugin.ConfigOption{{
			ID:           "effort",
			Name:         "Effort",
			DefaultValue: "balanced",
			Options: []sdkplugin.ConfigSelectOption{
				{Value: "balanced", Name: "Balanced"},
				{Value: "deep", Name: "Deep"},
			},
		}},
	}

	modes, configs := bridgeassembly.ProvidersFromAssembly(bridgeassembly.ProviderConfig{
		Assembly: assembly,
		Sessions: sessions,
		AppName:  "caelis",
		UserID:   "user-1",
	})
	if modes == nil || configs == nil {
		t.Fatalf("ProvidersFromAssembly() = (%T, %T), want non-nil providers", modes, configs)
	}

	session, err := sessions.Session(context.Background(), started.SessionRef)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}

	state, err := modes.SessionModes(context.Background(), session)
	if err != nil {
		t.Fatalf("SessionModes() error = %v", err)
	}
	if got := state.CurrentModeID; got != "default" {
		t.Fatalf("CurrentModeID = %q, want %q", got, "default")
	}

	if _, err := modes.SetSessionMode(context.Background(), acp.SetSessionModeRequest{
		SessionID: session.SessionID,
		ModeID:    "plan",
	}); err != nil {
		t.Fatalf("SetSessionMode() error = %v", err)
	}

	state, err = modes.SessionModes(context.Background(), session)
	if err != nil {
		t.Fatalf("SessionModes() after set error = %v", err)
	}
	if got := state.CurrentModeID; got != "plan" {
		t.Fatalf("CurrentModeID after set = %q, want %q", got, "plan")
	}

	options, err := configs.SessionConfigOptions(context.Background(), session)
	if err != nil {
		t.Fatalf("SessionConfigOptions() error = %v", err)
	}
	if got, want := len(options), 1; got != want {
		t.Fatalf("len(SessionConfigOptions) = %d, want %d", got, want)
	}
	if got := options[0].CurrentValue; got != "balanced" {
		t.Fatalf("default config value = %#v, want balanced", got)
	}

	resp, err := configs.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
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

func TestSkillBundlesNormalizeNamespaceAndDropEmptyRoots(t *testing.T) {
	assembly := sdkplugin.ResolvedAssembly{
		Skills: []sdkplugin.SkillBundle{
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

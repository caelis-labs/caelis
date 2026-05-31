//go:build e2e

package eval

import (
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/config"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/internal/app/local"
	"github.com/OnslaughtSnail/caelis/internal/app/services"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	"github.com/OnslaughtSnail/caelis/internal/surface/headless"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/gatewaydriver"
)

func TestLocalStackClaudeBuiltInACPE2E(t *testing.T) {
	requireClaudeACPE2E(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	stack, active := newClaudeACPLocalStackForE2E(ctx, t, "claude-e2e-test", "claude-e2e")
	registerClaudeACPAgentForE2E(ctx, t, stack)

	driver, err := gatewaydriver.NewGatewayDriver(
		ctx,
		gatewaydriver.BindAppServices(&gatewaydriver.DriverStack{}, stack.Services()),
		active.SessionID,
		"claude-e2e",
		"",
	)
	if err != nil {
		t.Fatalf("gatewaydriver.NewGatewayDriver() error = %v", err)
	}

	const want = "caelis claude acp e2e ok"
	view, err := driver.ExecuteCommand(ctx, gatewaydriver.CommandExecutionOptions{Input: "/claude Reply with exactly: " + want})
	if err != nil {
		t.Fatalf("ExecuteCommand(/claude) error = %v", err)
	}
	result := strings.TrimSpace(commandAssistantText(view.Events))
	if !strings.Contains(result, want) {
		t.Fatalf("claude sidecar result = %q, want %q", result, want)
	}

	loaded, err := stack.Services().Sessions().Load(ctx, active.Ref)
	if err != nil {
		t.Fatalf("Sessions.Load() error = %v", err)
	}
	var sawParticipantResponse bool
	for _, event := range loaded.Events {
		if event.Type == session.EventAssistant && event.Scope != nil && event.Scope.Participant.Kind == session.ParticipantACP && strings.Contains(session.EventText(event), want) {
			sawParticipantResponse = true
			break
		}
	}
	if !sawParticipantResponse {
		t.Fatalf("loaded events missing Claude ACP participant response")
	}
}

func TestLocalStackClaudeACPMainResumeOrNewE2E(t *testing.T) {
	requireClaudeACPE2E(t)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	stack, active := newClaudeACPLocalStackForE2E(ctx, t, "claude-resume-e2e-test", "claude-resume-e2e")
	registerClaudeACPAgentForE2E(ctx, t, stack)

	handoff, err := stack.Services().Controllers().Handoff(ctx, services.ControllerHandoffRequest{
		SessionRef: active.Ref,
		Target:     "claude",
		Source:     "test",
		Reason:     "exercise real Claude ACP session/resume",
	})
	if err != nil {
		t.Fatalf("Controllers.Handoff(claude) error = %v", err)
	}
	if !handoff.ActiveACP || handoff.Controller.Kind != session.ControllerACP {
		t.Fatalf("controller handoff = %#v, want Claude ACP controller", handoff)
	}

	const marker = "caelis claude acp resume e2e"
	const wantFirst = marker + " first ok"
	prompt := "Reply with exactly this text and no markdown: " + wantFirst
	result, err := headless.RunOnce(ctx, headless.Request{
		Services:           stack.Services(),
		SessionRef:         active.Ref,
		PreferredSessionID: active.SessionID,
		Input:              prompt,
		Surface:            "headless-claude-acp-resume-e2e",
	})
	if err != nil {
		t.Fatalf("RunOnce(claude) error = %v", err)
	}
	if !strings.Contains(strings.TrimSpace(result.Output), marker) {
		t.Fatalf("RunOnce(first Claude turn) output = %q, want marker %q", result.Output, marker)
	}
	status, found, err := stack.Services().Controllers().Status(ctx, active.Ref)
	if err != nil {
		t.Fatalf("Controllers.Status(after first turn) error = %v", err)
	}
	if !found || strings.TrimSpace(status.RemoteSessionID) == "" {
		t.Fatalf("controller status after first turn = %#v found=%v, want remote session", status, found)
	}
	firstRemoteSessionID := strings.TrimSpace(status.RemoteSessionID)

	if _, err := stack.Services().Controllers().Handoff(ctx, services.ControllerHandoffRequest{
		SessionRef: active.Ref,
		Target:     "claude",
		Source:     "test-resume",
		Reason:     "resume existing Claude ACP remote session",
	}); err != nil {
		t.Fatalf("Controllers.Handoff(claude resume) error = %v", err)
	}

	const wantSecond = marker + " second ok"
	result, err = headless.RunOnce(ctx, headless.Request{
		Services:           stack.Services(),
		SessionRef:         active.Ref,
		PreferredSessionID: active.SessionID,
		Input:              "Reply with exactly this text and no markdown: " + wantSecond,
		Surface:            "headless-claude-acp-resume-e2e",
	})
	if err != nil {
		t.Fatalf("RunOnce(second Claude turn) error = %v", err)
	}
	status, found, err = stack.Services().Controllers().Status(ctx, active.Ref)
	if err != nil {
		t.Fatalf("Controllers.Status(after second turn) error = %v", err)
	}
	resumedRemoteSessionID := strings.TrimSpace(status.RemoteSessionID)
	if resumedRemoteSessionID == "" {
		t.Fatalf("controller status after second turn = %#v found=%v, want non-empty Claude ACP remote session", status, found)
	}
	if resumedRemoteSessionID != firstRemoteSessionID {
		t.Logf("Claude ACP returned a new remote session on second handoff: old=%s new=%s", firstRemoteSessionID, resumedRemoteSessionID)
	}
	if output := strings.TrimSpace(result.Output); output == "" {
		t.Log("Claude ACP second turn completed without assistant text after resume/new handoff")
	} else if !strings.Contains(output, marker) {
		t.Logf("Claude ACP second turn output after resume/new handoff = %q", result.Output)
	}
}

func newClaudeACPLocalStackForE2E(ctx context.Context, t *testing.T, userID string, sessionID string) (*local.Stack, session.Session) {
	t.Helper()
	workdir := t.TempDir()
	storeRoot := filepath.Join(t.TempDir(), "sessions")
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatalf("settings.NewManager() error = %v", err)
	}
	stack, err := local.New(local.Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       userID,
			WorkspaceKey: "repo",
			WorkspaceCWD: workdir,
			Store: config.Store{
				Backend: "jsonl",
				URI:     storeRoot,
			},
		},
		Provider: localACPStaticProvider{},
		Settings: manager,
	})
	if err != nil {
		t.Fatalf("local.New() error = %v", err)
	}
	active, err := stack.Services().Sessions().Start(ctx, services.StartSessionRequest{
		PreferredSessionID: sessionID,
		Workspace: session.Workspace{
			Key: "repo",
			CWD: workdir,
		},
		Title: sessionID,
	})
	if err != nil {
		t.Fatalf("Sessions.Start() error = %v", err)
	}
	return stack, active
}

func requireClaudeACPE2E(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("CAELIS_RUN_CLAUDE_ACP_E2E")) != "1" {
		t.Skip("set CAELIS_RUN_CLAUDE_ACP_E2E=1 to run the local Claude Code ACP e2e")
	}
	if _, err := osexec.LookPath("npx"); err != nil {
		t.Skip("npx is not available")
	}
	if strings.TrimSpace(os.Getenv("CAELIS_CLAUDE_ACP_E2E_INSTALL")) == "1" {
		if _, err := osexec.LookPath("npm"); err != nil {
			t.Skip("npm is not available")
		}
	}
	if strings.TrimSpace(os.Getenv("npm_config_cache")) == "" {
		t.Setenv("npm_config_cache", filepath.Join(os.TempDir(), "caelis-npm-cache"))
	}
}

func registerClaudeACPAgentForE2E(ctx context.Context, t *testing.T, stack *local.Stack) {
	t.Helper()
	opts := services.RegisterBuiltinAgentOptions{}
	if strings.TrimSpace(os.Getenv("CAELIS_CLAUDE_ACP_E2E_INSTALL")) == "1" {
		opts.Install = true
	}
	if _, err := stack.Services().Agents().RegisterBuiltinWithOptions(ctx, "claude", opts); err != nil {
		t.Fatalf("Agents.RegisterBuiltinWithOptions(claude, %+v) error = %v", opts, err)
	}
}

func commandAssistantText(events []session.Event) string {
	var out string
	for _, event := range events {
		if event.Type != session.EventAssistant {
			continue
		}
		if text := strings.TrimSpace(session.EventText(event)); text != "" {
			out = text
		}
	}
	return out
}

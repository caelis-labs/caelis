//go:build e2e

package headless

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/agent/local"
	"github.com/OnslaughtSnail/caelis/impl/agent/local/chat"
	"github.com/OnslaughtSnail/caelis/impl/model/providers/e2etest"
	"github.com/OnslaughtSnail/caelis/impl/policy/presets"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/host"
	"github.com/OnslaughtSnail/caelis/impl/session/file"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin"
	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestHeadlessGatewayProviderE2E(t *testing.T) {
	spec := e2etest.RequireLLM(t, e2etest.Config{
		DefaultProvider: "minimax",
		DefaultModel:    "MiniMax-M2",
		Timeout:         90 * time.Second,
		MaxTokens:       256,
	})

	root := t.TempDir()
	sessions := file.NewService(file.NewStore(file.Config{RootDir: root}))
	sandboxRuntime, err := host.New(host.Config{CWD: root})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	tools, err := builtin.BuildCoreTools(builtin.CoreToolsConfig{Runtime: sandboxRuntime})
	if err != nil {
		t.Fatalf("BuildCoreTools() error = %v", err)
	}
	rt, err := local.New(local.Config{
		Sessions:          sessions,
		AgentFactory:      chat.Factory{SystemPrompt: "Answer tersely."},
		DefaultPolicyMode: presets.ModeAutoReview,
	})
	if err != nil {
		t.Fatalf("local.New() error = %v", err)
	}
	gw, err := kernel.New(kernel.Config{
		Sessions: sessions,
		Runtime:  rt,
		Resolver: testResolver{
			model: spec.LLM,
			tools: tools,
		},
	})
	if err != nil {
		t.Fatalf("kernel.New() error = %v", err)
	}
	session, err := gw.StartSession(context.Background(), kernel.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-headless-e2e",
			CWD: root,
		},
		PreferredSessionID: "sdk-headless-e2e",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	result, err := RunOnce(ctx, gw, kernel.BeginTurnRequest{
		SessionRef: session.SessionRef,
		Input:      "Reply with exactly: headless gateway e2e ok",
		Surface:    "headless-e2e",
	}, Options{})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if got := strings.TrimSpace(result.Output); got != "headless gateway e2e ok" {
		t.Fatalf("RunOnce() output = %q, want %q", got, "headless gateway e2e ok")
	}
}

type testResolver struct {
	model model.LLM
	tools []tool.Tool
}

func (r testResolver) ResolveTurn(_ context.Context, intent kernel.TurnIntent) (kernel.ResolvedTurn, error) {
	return kernel.ResolvedTurn{
		RunRequest: agent.RunRequest{
			SessionRef: intent.SessionRef,
			Input:      intent.Input,
			AgentSpec: agent.AgentSpec{
				Name:  "main",
				Model: r.model,
				Tools: r.tools,
			},
		},
	}, nil
}

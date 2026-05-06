//go:build e2e

package headless

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/gateway"
	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	"github.com/OnslaughtSnail/caelis/sdk/model/providers/e2etest"
	sdkpolicy "github.com/OnslaughtSnail/caelis/sdk/policy/presets"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	"github.com/OnslaughtSnail/caelis/sdk/runtime/agents/chat"
	localruntime "github.com/OnslaughtSnail/caelis/sdk/runtime/local"
	"github.com/OnslaughtSnail/caelis/sdk/sandbox/host"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sessionfile "github.com/OnslaughtSnail/caelis/sdk/session/file"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
	sdkbuiltin "github.com/OnslaughtSnail/caelis/sdk/tool/builtin"
)

func TestHeadlessGatewayProviderE2E(t *testing.T) {
	spec := e2etest.RequireLLM(t, e2etest.Config{
		DefaultProvider: "minimax",
		DefaultModel:    "MiniMax-M2",
		Timeout:         90 * time.Second,
		MaxTokens:       256,
	})

	root := t.TempDir()
	sessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root}))
	sandboxRuntime, err := host.New(host.Config{CWD: root})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	tools, err := sdkbuiltin.BuildCoreTools(sdkbuiltin.CoreToolsConfig{Runtime: sandboxRuntime})
	if err != nil {
		t.Fatalf("BuildCoreTools() error = %v", err)
	}
	rt, err := localruntime.New(localruntime.Config{
		Sessions:          sessions,
		AgentFactory:      chat.Factory{SystemPrompt: "Answer tersely."},
		DefaultPolicyMode: sdkpolicy.ModeDefault,
	})
	if err != nil {
		t.Fatalf("local.New() error = %v", err)
	}
	gw, err := gateway.New(gateway.Config{
		Sessions: sessions,
		Runtime:  rt,
		Resolver: testResolver{
			model: spec.LLM,
			tools: tools,
		},
	})
	if err != nil {
		t.Fatalf("gateway.New() error = %v", err)
	}
	session, err := gw.StartSession(context.Background(), gateway.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: sdksession.WorkspaceRef{
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

	result, err := RunOnce(ctx, gw, gateway.BeginTurnRequest{
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
	model sdkmodel.LLM
	tools []sdktool.Tool
}

func (r testResolver) ResolveTurn(_ context.Context, intent gateway.TurnIntent) (gateway.ResolvedTurn, error) {
	return gateway.ResolvedTurn{
		RunRequest: sdkruntime.RunRequest{
			SessionRef: intent.SessionRef,
			Input:      intent.Input,
			AgentSpec: sdkruntime.AgentSpec{
				Name:  "main",
				Model: r.model,
				Tools: r.tools,
			},
		},
	}, nil
}

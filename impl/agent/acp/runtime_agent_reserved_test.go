package acp_test

import (
	"context"
	"encoding/json"
	"testing"

	runtimeacp "github.com/OnslaughtSnail/caelis/impl/agent/acp"
	"github.com/OnslaughtSnail/caelis/impl/session/memory"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp"
	"github.com/OnslaughtSnail/caelis/protocol/acp/control/commands"
)

func TestRuntimeAgentReservedSlashCommandsDoNotRunSideACP(t *testing.T) {
	t.Parallel()

	for _, command := range append(commands.DefaultNames(), "sandbox") {
		command := command
		t.Run(command, func(t *testing.T) {
			t.Parallel()

			sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
			runtime := &sidecarLifecycleRuntime{sessions: sessions}
			runtimeAgent, err := runtimeacp.New(runtimeacp.Config{
				Runtime:  runtime,
				Sessions: sessions,
				BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
					return agent.AgentSpec{Name: "chat"}, nil
				},
				Commands: sideACPCommandProvider{{Name: command, Description: "reserved collision"}},
				AppName:  "caelis",
				UserID:   "user-1",
			})
			if err != nil {
				t.Fatalf("runtimeacp.New() error = %v", err)
			}
			activeSession, err := runtimeAgent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
			if err != nil {
				t.Fatalf("NewSession() error = %v", err)
			}

			resp, err := runtimeAgent.Prompt(context.Background(), acp.PromptRequest{
				SessionID: activeSession.SessionID,
				Prompt: []json.RawMessage{
					json.RawMessage(`{"type":"text","text":"/` + command + ` inspect the repo"}`),
				},
			}, &recordingPromptCallbacks{})
			if err != nil {
				t.Fatalf("Prompt(/%s) error = %v", command, err)
			}
			if resp.StopReason != acp.StopReasonEndTurn {
				t.Fatalf("StopReason = %q, want end_turn", resp.StopReason)
			}
			if !runtime.runCalled {
				t.Fatal("main runtime Run was not called for reserved slash command")
			}
			if runtime.attach.Agent != "" {
				t.Fatalf("attach request = %#v, want no side ACP attach", runtime.attach)
			}
		})
	}
}

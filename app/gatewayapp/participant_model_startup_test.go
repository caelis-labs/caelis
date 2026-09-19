package gatewayapp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acptest/jsonrpc"
	"github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func TestDirectParticipantPinsProviderModelBeforeACPStartup(t *testing.T) {
	for _, test := range []struct {
		name    string
		handle  agentbinding.Handle
		connect modelconfig.ConnectRequest
	}{
		{"reviewer-default", agentbinding.HandleReviewer, modelconfig.ConnectRequest{Provider: "deepseek", Models: []modelconfig.ModelSelection{{Name: "deepseek-flash"}}, APIKey: "test-key"}},
		{"orbit-endpoint", agentbinding.HandleOrbit, modelconfig.ConnectRequest{Provider: "deepseek", EndpointID: "office", BaseURL: "https://office.example/v1", Models: []modelconfig.ModelSelection{{Name: "deepseek-flash"}}, APIKey: "test-key"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			stack, parent := newLocalStateTestStack(t)
			t.Cleanup(func() { _ = stack.Close() })
			configs, err := modelconfig.AssembleConnect(ctx, test.connect, modelconfig.ConnectOptions{})
			if err != nil || len(configs) != 1 {
				t.Fatalf("AssembleConnect: %v", err)
			}
			profile, err := stack.connectTestModel(configs[0])
			if err != nil {
				t.Fatal(err)
			}
			if _, err := stack.testAgentBindings().BindAgentBinding(ctx, agentbinding.Binding{Handle: test.handle, ProfileID: profile.ID, Effort: "high"}); err != nil {
				t.Fatal(err)
			}
			parentRuntime, release := activateHeldSessionRuntime(t, stack, parent.SessionID)
			defer release()
			instance := parentRuntime.instance
			cfg, err := instance.materializeDelegatedModel(string(test.handle), profile.ID, "high", instance.activeRuntime)
			if err != nil {
				t.Fatal(err)
			}
			if test.handle == agentbinding.HandleReviewer {
				resolved, err := instance.withReviewerAgent(controlassembly.ResolvedAssembly{}, instance.activeRuntime)
				if err != nil || len(resolved.Agents) != 1 {
					t.Fatalf("Reviewer assembly: %#v, %v", resolved.Agents, err)
				}
				cfg = resolved.Agents[0]
			}
			child, err := startGatewayAppTestSession(ctx, stack, "direct-model-child")
			if err != nil {
				t.Fatal(err)
			}
			// Substitute only the ACP process. The real Control command, placement,
			// assembly, startup validation, and durable child model pin still run.
			published := modelconfig.PublicSelector(*cfg.PinnedModel)
			if published == cfg.PinnedModel.ID {
				t.Fatal("fixture does not exercise distinct durable and public model IDs")
			}
			cfg.Command = os.Args[0]
			cfg.Args = []string{"-test.run=^TestParticipantModelStartupHelperProcess$", "--"}
			callsFile := filepath.Join(t.TempDir(), "session-calls")
			if cfg.Env == nil {
				cfg.Env = make(map[string]string)
			}
			for key, value := range map[string]string{
				"CAELIS_PARTICIPANT_MODEL_HELPER":   "1",
				"CAELIS_PARTICIPANT_MODEL_SESSION":  child.SessionID,
				"CAELIS_PARTICIPANT_MODEL_SELECTOR": published,
				"CAELIS_PARTICIPANT_MODEL_CALLS":    callsFile,
			} {
				cfg.Env[key] = value
			}
			if err := instance.acpControlPlane.UpdateAgents([]controlassembly.AgentConfig{cfg}); err != nil {
				t.Fatal(err)
			}
			principal := appserver.Principal{ID: stack.UserID()}
			sessions, err := appserver.BindSessionClient(stack.ControlClient(), principal)
			if err != nil {
				t.Fatal(err)
			}
			participants, err := appserver.BindParticipantClient(stack.ControlParticipants(), principal)
			if err != nil {
				t.Fatal(err)
			}
			turns, err := appserver.NewParticipantTurnClient(sessions, participants)
			if err != nil {
				t.Fatal(err)
			}
			turn, err := turns.Start(ctx, appserver.ParticipantTurnStartRequest{
				SessionID: parent.SessionID, Handle: string(test.handle), Input: "review the changes",
				Label: "@" + string(test.handle), Source: "test-direct-model",
				Transient: test.handle == agentbinding.HandleReviewer,
			})
			if err != nil {
				t.Fatalf("Start(%s): %v", test.handle, err)
			}
			defer turn.Close()
			for range turn.Events() {
			}
			if err := turn.Err(); err != nil {
				t.Fatalf("participant turn: %v", err)
			}
			if test.handle == agentbinding.HandleOrbit {
				current := mustCurrentSession(t, stack, parent.SessionID)
				if len(current.Participants) != 1 {
					t.Fatalf("participants = %#v", current.Participants)
				}
				// Drop the live endpoint and reattach from the durable participant.
				// Its recorded effort must win over newly assembled defaults.
				if err := instance.acpControlPlane.Quiesce(ctx); err != nil {
					t.Fatal(err)
				}
				cfg.PinnedModel = &configs[0]
				cfg.SessionOptions = caelisModelSessionOptions(configs[0], "none")
				if err := instance.acpControlPlane.UpdateAgents([]controlassembly.AgentConfig{cfg}); err != nil {
					t.Fatal(err)
				}
				resumed, err := turns.Prompt(ctx, appserver.ParticipantTurnPromptRequest{
					SessionID: parent.SessionID, ParticipantID: current.Participants[0].ID, Input: "continue the review",
				})
				if err != nil {
					t.Fatalf("Prompt after reattachment: %v", err)
				}
				defer resumed.Close()
				for range resumed.Events() {
				}
				if err := resumed.Err(); err != nil {
					t.Fatal(err)
				}
				calls, err := os.ReadFile(callsFile)
				if err != nil || string(calls) != "new\nresume\n" {
					t.Fatalf("Session calls = %q, %v; want new then resume", calls, err)
				}
			}
			state, err := instance.sessions.SnapshotState(ctx, child.SessionRef)
			if err != nil {
				t.Fatal(err)
			}
			if got := kernel.CurrentModelAlias(state); got != cfg.PinnedModel.ID {
				t.Fatalf("child model = %q, want durable ID %q", got, cfg.PinnedModel.ID)
			}
			if got := kernel.CurrentReasoningEffort(state); got != "high" {
				t.Fatalf("child effort = %q, want high", got)
			}
		})
	}
}

func TestParticipantModelStartupHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_PARTICIPANT_MODEL_HELPER") != "1" {
		return
	}
	selector := os.Getenv("CAELIS_PARTICIPANT_MODEL_SELECTOR")
	manual := false
	options := func(mode string) []client.SessionConfigOption {
		return []client.SessionConfigOption{
			{ID: "model", Name: "Model", Type: "select", Category: "model", CurrentValue: selector,
				Options: []client.SessionConfigSelectOption{{Value: selector, Name: "Selected model"}}},
			{ID: "mode", Name: "Mode", Type: "select", CurrentValue: mode,
				Options: []client.SessionConfigSelectOption{{Value: "auto-review", Name: "Auto review"}, {Value: "manual", Name: "Manual"}}},
		}
	}
	conn := jsonrpc.New(os.Stdin, os.Stdout)
	_ = conn.Serve(context.Background(), func(_ context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
		switch msg.Method {
		case client.MethodInitialize:
			return client.InitializeResponse{ProtocolVersion: 1, AgentCapabilities: client.AgentCapabilities{
				SessionCapabilities: map[string]json.RawMessage{"resume": json.RawMessage(`{}`)},
			}}, nil
		case client.MethodSessionNew:
			recordParticipantModelSessionCall("new")
			return client.NewSessionResponse{
				SessionID:     os.Getenv("CAELIS_PARTICIPANT_MODEL_SESSION"),
				ConfigOptions: options("auto-review"),
			}, nil
		case client.MethodSessionResume:
			recordParticipantModelSessionCall("resume")
			return client.ResumeSessionResponse{ConfigOptions: options("auto-review")}, nil
		case client.MethodSessionPrompt:
			if !manual {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "parent permission bridge was not configured"}
			}
			return client.PromptResponse{StopReason: string(acpsdk.StopReasonEndTurn)}, nil
		case client.MethodSessionSetConfig:
			var req client.SetSessionConfigOptionRequest
			// Explicit mode defaults are reasserted before permission bridging;
			// provider model and effort configuration must still stay in Control.
			if err := json.Unmarshal(msg.Params, &req); err != nil || req.ValueId == nil || req.ValueId.ConfigId != "mode" || (req.ValueId.Value != "auto-review" && req.ValueId.Value != "manual") {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "only mode configuration may cross ACP"}
			}
			mode := string(req.ValueId.Value)
			manual = mode == "manual"
			return client.SetSessionConfigOptionResponse{ConfigOptions: options(mode)}, nil
		case client.MethodSessionSetModel:
			return nil, &jsonrpc.RPCError{Code: -32602, Message: "provider configuration must stay in Control"}
		default:
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
		}
	}, nil)
	os.Exit(0)
}

func recordParticipantModelSessionCall(method string) {
	file, err := os.OpenFile(os.Getenv("CAELIS_PARTICIPANT_MODEL_CALLS"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(1)
	}
	_, err = file.WriteString(strings.TrimSpace(method) + "\n")
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		os.Exit(1)
	}
}

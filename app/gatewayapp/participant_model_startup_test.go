package gatewayapp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/internal/acpdraft"
	"github.com/caelis-labs/caelis/internal/acptest/draftpeer"
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
			if cfg.ACPDraftSchema != acpdraft.SchemaTag {
				t.Fatalf("self participant schema = %q, want explicit alpha.8", cfg.ACPDraftSchema)
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
	var manual atomic.Bool
	options := func(mode string) []v2.SessionConfigOption {
		modelCategory := v2.SessionConfigOptionCategory("model")
		modelOptions := v2.SessionConfigSelectOptionsUngrouped{{Value: v2.SessionConfigValueId(selector), Name: "Selected model"}}
		modeOptions := v2.SessionConfigSelectOptionsUngrouped{{Value: "auto-review", Name: "Auto review"}, {Value: "manual", Name: "Manual"}}
		return []v2.SessionConfigOption{
			{Select: &v2.SessionConfigOptionSelect{ConfigId: "model", Name: "Model", Type: "select", Category: &modelCategory, CurrentValue: v2.SessionConfigValueId(selector), Options: v2.SessionConfigSelectOptions{Ungrouped: &modelOptions}}},
			{Select: &v2.SessionConfigOptionSelect{ConfigId: "mode", Name: "Mode", Type: "select", CurrentValue: v2.SessionConfigValueId(mode), Options: v2.SessionConfigSelectOptions{Ungrouped: &modeOptions}}},
		}
	}
	peer := &draftpeer.Peer{SessionID: os.Getenv("CAELIS_PARTICIPANT_MODEL_SESSION")}
	peer.OnNew = func(v2.NewSessionRequest) (v2.NewSessionResponse, error) {
		recordParticipantModelSessionCall("new")
		return v2.NewSessionResponse{SessionId: v2.SessionId(peer.SessionID), ConfigOptions: options("auto-review")}, nil
	}
	peer.OnResume = func(v2.ResumeSessionRequest) (v2.ResumeSessionResponse, error) {
		recordParticipantModelSessionCall("resume")
		return v2.ResumeSessionResponse{ConfigOptions: options("auto-review")}, nil
	}
	peer.OnConfig = func(req v2.SetSessionConfigOptionRequest) (v2.SetSessionConfigOptionResponse, error) {
		// Explicit mode defaults are reasserted before permission bridging;
		// provider model and effort configuration must still stay in Control.
		if req.Id == nil || req.Id.ConfigId != "mode" || (req.Id.Value != "auto-review" && req.Id.Value != "manual") {
			return v2.SetSessionConfigOptionResponse{}, acpsdk.NewInvalidParams("only mode configuration may cross ACP")
		}
		mode := string(req.Id.Value)
		manual.Store(mode == "manual")
		return v2.SetSessionConfigOptionResponse{ConfigOptions: options(mode)}, nil
	}
	peer.OnPrompt = func(ctx context.Context, req v2.PromptRequest, activity, message string) (v2.PromptResponse, error) {
		if !manual.Load() {
			return v2.PromptResponse{}, acpsdk.NewInvalidParams("parent permission bridge was not configured")
		}
		if err := peer.State(ctx, req.SessionId, activity, "running", ""); err != nil {
			return v2.PromptResponse{}, err
		}
		if err := peer.Echo(ctx, req.SessionId, message, "review input"); err != nil {
			return v2.PromptResponse{}, err
		}
		if err := peer.State(ctx, req.SessionId, activity, "idle", "end_turn"); err != nil {
			return v2.PromptResponse{}, err
		}
		return v2.PromptResponse{MessageId: v2.MessageId(message), Meta: acpdraft.ActivityMeta(activity)}, nil
	}
	conn, err := peer.Serve(os.Stdin, os.Stdout)
	if err != nil {
		os.Exit(1)
	}
	<-conn.Done()

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

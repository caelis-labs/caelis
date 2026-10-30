package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sdkdelegation "github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/appserver"
	controlplacement "github.com/caelis-labs/caelis/control/placement"
)

// The transport is synthetic, but every assertion below goes through the real
// provider adapter, native ToolAugmenter and an admitted Session Turn.
func delegationResponse(req *http.Request) (*http.Response, error) {
	defer req.Body.Close()
	var body struct{ Stream bool }
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return nil, err
	}
	response := `{"id":"delegation-response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`
	contentType := "application/json"
	if body.Stream {
		response = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n"
		contentType = "text/event-stream"
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
}

func promptDelegationSession(t *testing.T, stack *Stack, id, operation string) {
	t.Helper()
	result, err := stack.ControlClient().Prompt(t.Context(), appserver.Principal{ID: stack.UserID()}, appserver.PromptRequest{
		WriteBase: appserver.WriteBase{OperationID: operation, SessionID: id}, Input: "continue",
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("native Prompt: %v, %v", result.Outcome, err)
	}
	waitForGatewayAppTestTurn(t, stack, id)
}

func TestNativeSelfSnapshotSurvivesHostCapabilityEdit(t *testing.T) {
	stack, parent := newLocalStateTestStack(t)
	t.Cleanup(func() { _ = stack.Close() })
	var requests atomic.Int32
	stack.composition.lookup.resolveTransportHTTPClient = func(context.Context, ModelConfig) (*http.Client, error) {
		return &http.Client{Transport: gatewayAppRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			data, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			_ = req.Body.Close()
			var payload struct {
				Model     string
				Reasoning struct{ Effort string }
			}
			if err := json.Unmarshal(data, &payload); err != nil {
				return nil, err
			}
			if payload.Model != "session-x" || payload.Reasoning.Effort != "high" {
				return nil, fmt.Errorf("self request lost independent model or effort")
			}
			requests.Add(1)
			req.Body = io.NopCloser(strings.NewReader(string(data)))
			return delegationResponse(req)
		})}, nil
	}
	configured := ModelConfig{Provider: "custom", API: providers.APIOpenAIResponses, Model: "session-x", BaseURL: "https://self.example/v1", Token: "synthetic-self", ReasoningEffort: "low", DefaultReasoningEffort: "low", ReasoningLevels: []string{"low", "high"}}
	profile, err := stack.connectTestModel(configured)
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.useTestHostModel(t.Context(), session.SessionRef{}, profile.Backend.Provider.ModelConfigID, "low"); err != nil {
		t.Fatal(err)
	}
	if _, err := stack.testAgentBindings().BindAgentBinding(t.Context(), agentbinding.Binding{Handle: agentbinding.HandleBreeze, ProfileID: profile.ID, Effort: "low"}); err != nil {
		t.Fatal(err)
	}
	runtime, release := activateHeldSessionRuntime(t, stack, parent.SessionID)
	defer release()
	parent = mustCurrentSession(t, stack, parent.SessionID)
	_, err = stack.ConfigurationCommands().UseSessionModel(t.Context(), appserver.Principal{ID: stack.UserID()}, appserver.SessionModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "select-self-high", SessionID: parent.SessionID, ExpectedRevision: &parent.Revision, ExpectedControllerEpoch: parent.Controller.EpochID},
		Model:     profile.ID, ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	promptDelegationSession(t, stack, parent.SessionID, "before-capability-edit")
	if requests.Load() != 1 {
		t.Fatal("initial main model request was not issued")
	}
	configured.ReasoningLevels = []string{"low"}
	if _, err := stack.connectTestModel(configured); err != nil {
		t.Fatal(err)
	}
	// This must reach the next main request with a nonempty self selection.
	// Empty SessionContext helper calls would miss the ToolAugmenter failure.
	promptDelegationSession(t, stack, parent.SessionID, "after-capability-edit")
	if requests.Load() != 2 {
		t.Fatal("next main model request was not issued")
	}
	_, targets, err := runtime.instance.delegationSpawnConfiguration(controlplacement.SessionContext{ProfileID: profile.ID, Effort: "high"})
	if err != nil || targets["self"].Placement.ReasoningEffort != "high" {
		t.Fatalf("self snapshot: %v", err)
	}
	if _, err := runtime.instance.resolveDelegationPlacement(t.Context(), sdkdelegation.TargetRequest{Target: targets["self"]}, runtime.instance.activeRuntime); err != nil {
		t.Fatal(err)
	}
	team, err := runtime.instance.resolveDelegationPlacement(t.Context(), sdkdelegation.TargetRequest{Target: targets["breeze"]}, runtime.instance.activeRuntime)
	if err != nil {
		t.Fatal(err)
	}
	if team.PinnedModel.ReasoningEffort != "low" || len(team.PinnedModel.ReasoningLevels) != 1 {
		t.Fatal("Team inherited the Session main-model capability snapshot")
	}
	if err := stack.deleteTestHostModel(t.Context(), session.SessionRef{}, profile.Backend.Provider.ModelConfigID); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.instance.resolveDelegationPlacement(t.Context(), sdkdelegation.TargetRequest{Target: targets["self"]}, runtime.instance.activeRuntime); err == nil {
		t.Fatal("explicit provider deletion did not revoke self")
	}
}

func TestTeamChildPreparationUsesMaterializedCredentials(t *testing.T) {
	for _, change := range []string{"added-key", "rotated-key"} {
		for _, handle := range []agentbinding.Handle{agentbinding.HandleBreeze, agentbinding.HandleOrbit, agentbinding.HandleZenith, agentbinding.HandleReviewer} {
			t.Run(change+"/"+string(handle), func(t *testing.T) {
				stack, parent := newLocalStateTestStack(t)
				t.Cleanup(func() { _ = stack.Close() })
				var requests atomic.Int32
				var wrongCredential atomic.Bool
				stack.composition.lookup.resolveTransportHTTPClient = func(context.Context, ModelConfig) (*http.Client, error) {
					return &http.Client{Transport: gatewayAppRoundTripFunc(func(req *http.Request) (*http.Response, error) {
						requests.Add(1)
						if req.Header.Get("Authorization") != "Bearer synthetic-new" {
							wrongCredential.Store(true)
						}
						return delegationResponse(req)
					})}, nil
				}
				configured := ModelConfig{Provider: "custom", API: providers.APIOpenAIResponses, Model: "team-key", BaseURL: "https://team-key.example/v1", Token: "synthetic-old"}
				if change == "rotated-key" {
					if _, err := stack.connectTestModel(configured); err != nil {
						t.Fatal(err)
					}
				}
				runtime, release := activateHeldSessionRuntime(t, stack, parent.SessionID)
				defer release()
				configured.Token = "synthetic-new"
				profile, err := stack.connectTestModel(configured)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := stack.testAgentBindings().BindAgentBinding(t.Context(), agentbinding.Binding{Handle: handle, ProfileID: profile.ID, Effort: profile.Effort.DefaultEffort}); err != nil {
					t.Fatal(err)
				}
				ctx, err := runtime.instance.captureTeamSnapshot(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				purpose := controlplacement.PurposeSpawn
				if handle == agentbinding.HandleReviewer {
					purpose = controlplacement.PurposeReviewer
				}
				placed, err := runtime.instance.resolveHandlePlacement(ctx, controlplacement.HandleRequest{Handle: handle, Purpose: purpose})
				if err != nil {
					t.Fatal(err)
				}
				agent, err := runtime.instance.resolveDelegationPlacement(ctx, sdkdelegation.TargetRequest{Target: spawn.Target{Selector: string(handle), Placement: placed}}, runtime.instance.activeRuntime)
				if err != nil {
					t.Fatal(err)
				}
				child, err := startGatewayAppTestSession(t.Context(), stack, "team-key-child")
				if err != nil {
					t.Fatal(err)
				}
				options, err := runtime.instance.prepareSpawnedACPSession(ctx, child.SessionID, agent)
				if err != nil {
					t.Fatalf("real child preparation: %v", err)
				}
				defer runtime.instance.releaseSpawnedSessionModelPin(child.SessionID)
				if options.ModelID != "" {
					t.Fatal("process-local pin escaped into ACP options")
				}
				_, releaseChild := activateHeldSessionRuntime(t, stack, child.SessionID)
				defer releaseChild()
				promptDelegationSession(t, stack, child.SessionID, "team-key-request")
				if requests.Load() != 1 || wrongCredential.Load() {
					t.Fatal("child provider request missing or used an obsolete credential")
				}
				if change == "rotated-key" {
					old, err := runtime.instance.lookup.ResolveConfig(profile.Backend.Provider.ModelConfigID)
					if err != nil {
						t.Fatal(err)
					}
					old, err = runtime.instance.lookup.materializeAPIKeyCredential(t.Context(), old)
					if err != nil || old.Token != "synthetic-old" {
						t.Fatal("Team refresh mutated parent main-model credential snapshot")
					}
				} else if runtime.instance.lookup.HasAlias(profile.Backend.Provider.ModelConfigID) {
					t.Fatal("Team addition refreshed parent main-model lookup")
				}
			})
		}
	}
}

func TestMaterializedTeamChildKeepsInFlightCredential(t *testing.T) {
	stack, parent := newLocalStateTestStack(t)
	t.Cleanup(func() { _ = stack.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	entered, resume := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	}()
	var requests atomic.Int32
	stack.composition.lookup.resolveTransportHTTPClient = func(context.Context, ModelConfig) (*http.Client, error) {
		return &http.Client{Transport: gatewayAppRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if requests.Add(1) != 1 || req.Header.Get("Authorization") != "Bearer synthetic-before" {
				return nil, fmt.Errorf("unexpected in-flight request or credential")
			}
			close(entered)
			select {
			case <-resume:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
			return delegationResponse(req)
		})}, nil
	}
	configured := ModelConfig{Provider: "custom", API: providers.APIOpenAIResponses, Model: "held-key", BaseURL: "https://held-key.example/v1", Token: "synthetic-before"}
	profile, err := stack.connectTestModel(configured)
	if err != nil {
		t.Fatal(err)
	}
	runtime, release := activateHeldSessionRuntime(t, stack, parent.SessionID)
	defer release()
	agent, err := runtime.instance.materializeDelegatedModel(t.Context(), "orbit", profile.ID, profile.Effort.DefaultEffort, runtime.instance.activeRuntime)
	if err != nil {
		t.Fatal(err)
	}
	child, err := startGatewayAppTestSession(t.Context(), stack, "held-child")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.instance.prepareSpawnedACPSession(t.Context(), child.SessionID, agent); err != nil {
		t.Fatal(err)
	}
	defer runtime.instance.releaseSpawnedSessionModelPin(child.SessionID)
	_, releaseChild := activateHeldSessionRuntime(t, stack, child.SessionID)
	defer releaseChild()
	_, err = stack.ControlClient().Prompt(t.Context(), appserver.Principal{ID: stack.UserID()}, appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "held-request", SessionID: child.SessionID}, Input: "continue"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("child request never started")
	}
	configured.Token = "synthetic-after"
	if _, err := stack.connectTestModel(configured); err != nil {
		t.Fatal(err)
	}
	close(resume)
	waitForGatewayAppTestTurn(t, stack, child.SessionID)
	if requests.Load() != 1 || agent.PinnedModel.Token != "synthetic-before" {
		t.Fatal("materialized in-flight selection changed")
	}
	// A frozen, not-yet-started child cannot silently refresh its key after a
	// rotation. Keep the existing pin admission fence; do not replay the action.
	unstarted, err := startGatewayAppTestSession(t.Context(), stack, "unstarted-child")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.instance.prepareSpawnedACPSession(t.Context(), unstarted.SessionID, agent); err == nil {
		t.Fatal("obsolete credential admitted after rotation")
	}
}

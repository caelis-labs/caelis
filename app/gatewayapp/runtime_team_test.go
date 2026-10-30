package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sdkdelegation "github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/appserver"
	controlplacement "github.com/caelis-labs/caelis/control/placement"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func TestRuntimeTeamGuardianSupersedesLegacyProfileReviewer(t *testing.T) {
	stack := newLocalStateTestHost(t)
	luna, err := stack.connectTestModel(ModelConfig{Provider: "ollama", Model: "luna"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.testAgentBindings().BindAgentBinding(t.Context(), agentbinding.Binding{Handle: agentbinding.HandleGuardian, ProfileID: luna.ID, Effort: luna.Effort.DefaultEffort}); err != nil {
		t.Fatal(err)
	}
	profile := sessionprofile.Profile{
		Version: "v1", Model: "old-main", ToolsVersion: "v1", Execution: "tools-only",
		Permissions: sessionprofile.Permissions{ApprovalMode: "auto-review"},
		Reviewer:    &sessionprofile.Reviewer{Kind: "guardian", Model: "retired-mimo"},
	}
	state, err := stack.composition.applicationReviewerState(t.Context(), profile)
	if err != nil || state.Status != "ready" || state.Reviewer == nil || state.Reviewer.Model != luna.Backend.Provider.ModelConfigID {
		t.Fatalf("runtime Team must supersede historical reviewer: %#v, %v", state, err)
	}
}

// Use actual provider adapters and activated Runtime compositions. Only the HTTP
// transport is synthetic; neither Session main pins nor activation may own Team.
func TestRuntimeTeamRevisionAcrossActivatedSessions(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	stack := newLocalStateTestHost(t)
	profiles, err := stack.connectTestModels([]ModelConfig{
		{Provider: "xiaomi", Model: "mimo-v2.6-flash", Token: "synthetic"},
		{Provider: "deepseek", Model: "deepseek-v4-flash", Token: "synthetic"},
		{Provider: "openai", API: providers.APIOpenAI, Model: "gpt-6-luna", Token: "synthetic", ReasoningEffort: "high", ReasoningLevels: []string{"low", "high"}},
		{Provider: "openai", API: providers.APIOpenAI, Model: "gpt-6-sol", Token: "synthetic", ReasoningEffort: "high", ReasoningLevels: []string{"low", "high"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var requests []map[string]any
	stack.composition.lookup.resolveTransportHTTPClient = func(context.Context, ModelConfig) (*http.Client, error) {
		return &http.Client{Transport: gatewayAppRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			defer req.Body.Close()
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				return nil, err
			}
			requests = append(requests, body)
			response := `{"id":"team-response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"tools\":[\"ProbeTool\"]}"}]}]}`
			contentType := "application/json"
			if body["stream"] == true {
				response = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n"
				contentType = "text/event-stream"
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
		})}, nil
	}
	client, err := appserver.BindSessionClient(stack.ControlClient(), appserver.Principal{ID: stack.composition.authorities.userID})
	if err != nil {
		t.Fatal(err)
	}
	type active struct {
		composition *runtimeComposition
		native      *kernel.AssemblyResolver
		application *applicationTurnResolver
		children    *applicationChildren
		ref         session.SessionRef
	}
	var sessions []active
	for index, profile := range profiles[:2] {
		id := createWorkspaceRuntimeTestSession(t, client, fmt.Sprintf("team-create-%d", index), fmt.Sprintf("team-%d", index), stack.composition.workspace.Key, stack.composition.workspace.CWD)
		instance := activateSessionRuntime(t, stack, id).instance
		alias := profile.Backend.Provider.ModelConfigID
		native, err := kernel.NewAssemblyResolver(kernel.AssemblyResolverConfig{Sessions: guardianModelState{alias: alias}, ModelLookup: instance.lookup,
			ApprovalConfigurationSnapshot: instance.captureTeamSnapshot,
			ApprovalModelResolver: func(ctx context.Context, _ session.SessionRef) (model.LLM, bool, error) {
				return instance.resolveGuardianAgentModel(ctx, 0)
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		binding := sessionprofile.Binding{SessionID: id, Profile: sessionprofile.Profile{Version: "team-v1", ToolsVersion: "team-tools-v1", Execution: "tools-only", Permissions: sessionprofile.Permissions{ApprovalMode: "auto-review"}, Model: alias, Reviewer: &sessionprofile.Reviewer{Kind: "guardian", Model: alias}}}
		children := &applicationChildren{composition: &instance.runtimeComposition, mainModelSnapshot: func(context.Context) (AppConfig, *modelLookup, error) {
			t.Fatal("Team role consulted independent main-model snapshot")
			return AppConfig{}, nil, nil
		}}
		sessions = append(sessions, active{composition: &instance.runtimeComposition, native: native, application: &applicationTurnResolver{composition: &instance.runtimeComposition, binding: binding}, children: children, ref: session.SessionRef{SessionID: id}})
	}
	handles := []agentbinding.Handle{agentbinding.HandleBreeze, agentbinding.HandleOrbit, agentbinding.HandleZenith, agentbinding.HandleToolSearch, agentbinding.HandleGuardian}
	assertRequest := func(want, effort, speed string) {
		t.Helper()
		body := requests[len(requests)-1]
		reasoning, _ := body["reasoning"].(map[string]any)
		if body["model"] != want || reasoning["effort"] != effort || (body["service_tier"] == "priority") != (speed == "fast") {
			t.Fatalf("wrong Team request: %#v", body)
		}
	}
	generate := func(llm model.LLM, want, effort, speed string) {
		t.Helper()
		for _, err := range model.Generate(t.Context(), llm, &model.Request{Reasoning: model.ReasoningConfig{Effort: "low"}, ServiceTier: model.ServiceTierPriority}) {
			if err != nil {
				t.Fatal(err)
			}
		}
		assertRequest(want, effort, speed)
	}
	var frozen model.LLM
	var frozenContext context.Context
	for round, profile := range profiles[2:] {
		effort, speed := "high", "fast"
		if round == 1 {
			effort, speed = "low", "standard"
		}
		for _, handle := range handles {
			if _, err := stack.testAgentBindings().BindAgentBinding(t.Context(), agentbinding.Binding{Handle: handle, ProfileID: profile.ID, Effort: effort, Speed: speed}); err != nil {
				t.Fatal(err)
			}
		}
		want := "gpt-6-luna"
		if round == 1 {
			want = "gpt-6-sol"
		}
		for _, current := range sessions {
			for _, resolve := range []func(context.Context, session.SessionRef) (model.LLM, error){current.native.ResolveApprovalModel, current.application.ResolveApprovalModel} {
				llm, err := resolve(t.Context(), current.ref)
				if err != nil {
					t.Fatal(err)
				}
				generate(llm, want, effort, speed)
			}
			main, err := current.composition.lookup.ResolveModel(t.Context(), current.application.binding.Profile.Model, 0)
			if err != nil {
				t.Fatal(err)
			}
			names, err := newBoundToolSearchRanker(current.composition).Rank(t.Context(), "inspect probe", []tool.Definition{{Name: "ProbeTool", Description: "inspect probe"}}, 1, toolsearch.SearchModel{Model: main.Model, Reasoning: model.ReasoningConfig{Effort: "low"}, ServiceTier: model.ServiceTierPriority, ReadSchema: func(context.Context, string) (tool.Definition, error) { return tool.Definition{Name: "ProbeTool"}, nil }})
			if err != nil || len(names) != 1 || names[0] != "ProbeTool" {
				t.Fatalf("native ToolSearch ranking: %v, %v", names, err)
			}
			assertRequest(want, effort, speed)
			_, targets, err := current.composition.delegationSpawnConfiguration(controlplacement.SessionContext{})
			if err != nil {
				t.Fatal(err)
			}
			for _, handle := range handles[:3] {
				materialized, err := current.composition.resolveDelegationPlacement(t.Context(), sdkdelegation.TargetRequest{Target: targets[string(handle)]}, current.composition.activeRuntime)
				if err != nil || materialized.SessionOptions.ModelID != profile.Backend.Provider.ModelConfigID || materialized.SessionOptions.ConfigValues[acpConfigReasoningID] != effort {
					t.Fatalf("native child materialization: %+v, %v", materialized.SessionOptions, err)
				}
				child, err := current.children.placement(t.Context(), handle)
				if err != nil {
					t.Fatal(err)
				}
				native, err := current.composition.resolveHandlePlacement(t.Context(), controlplacement.HandleRequest{Handle: handle, Purpose: controlplacement.PurposeSpawn})
				if err != nil || child.Fingerprint != native.Fingerprint || child.Model != profile.Backend.Provider.ModelConfigID || child.ReasoningEffort != effort || (child.ServiceTier == "priority") != (speed == "fast") {
					t.Fatalf("Team child placement: %+v / %+v, %v", child, native, err)
				}
			}
		}
		if round == 0 {
			frozenContext, err = sessions[0].composition.captureTeamSnapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			frozen, err = sessions[0].native.ResolveApprovalModel(frozenContext, sessions[0].ref)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	// A completed selection and a deferred selection from an already captured
	// review keep the old model, effort and speed across a later bind.
	generate(frozen, "gpt-6-luna", "high", "fast")
	deferred, err := sessions[0].native.ResolveApprovalModel(frozenContext, sessions[0].ref)
	if err != nil {
		t.Fatal(err)
	}
	generate(deferred, "gpt-6-luna", "high", "fast")
	for _, handle := range handles {
		if _, err := stack.testAgentBindings().ResetAgentBinding(t.Context(), handle); err != nil {
			t.Fatal(err)
		}
	}
	for index, current := range sessions {
		llm, err := current.native.ResolveApprovalModel(t.Context(), current.ref)
		want := []string{"mimo-v2.6-flash", "deepseek-v4-flash"}[index]
		if err != nil || llm.Name() != want {
			t.Fatalf("reset main fallback = %v, %v; want %s", llm, err, want)
		}
		if llm, err := current.composition.boundToolSearchAgentModel(t.Context()); err != nil || llm != nil {
			t.Fatalf("reset tool-search = %v, %v", llm, err)
		}
		for _, handle := range handles[:3] {
			if _, err := current.children.placement(t.Context(), handle); err == nil {
				t.Fatalf("reset retained %s", handle)
			}
		}
	}
	if len(requests) != 14 {
		t.Fatalf("unexpected provider attempts: %d", len(requests))
	}
}

package codex

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
)

func tierModels() []codexModel {
	return []codexModel{{ID: "model", DefaultServiceTier: "default", ServiceTiers: []codexServiceTier{{ID: "default", Name: "Standard"}, {ID: "fast", Name: "Fast"}}}, {ID: "other"}}
}

func TestServiceTierDiscoveryPreservesUnknownAndEffectiveDefault(t *testing.T) {
	for _, raw := range []string{`{"model":"model"}`, `{"model":"model","serviceTier":null}`, `{"model":"model","serviceTier":"fast"}`} {
		var opened threadOpenResponse
		if err := json.Unmarshal([]byte(raw), &opened); err != nil {
			t.Fatal(err)
		}
		state := &sessionState{models: tierModels()}
		state.applyOpenResponse(opened)
		option := state.serviceTierOptionLocked()
		if option == nil {
			t.Fatal("missing advertised tiers")
		}
		want := "default"
		if opened.ServiceTier != nil {
			want = *opened.ServiceTier
		}
		if string(option.Select.CurrentValue) != want {
			t.Fatalf("current = %q, want %s", option.Select.CurrentValue, want)
		}
		if opened.ServiceTier == nil && state.serviceTier != nil {
			t.Fatal("inheritance became explicit standard")
		}
	}
	state := &sessionState{model: "model", models: []codexModel{{ID: "model"}}}
	if state.serviceTierOptionLocked() != nil {
		t.Fatal("guessed tiers for old schema")
	}
	state.models = tierModels()
	state.models[0].DefaultServiceTier = "fast"
	if option := state.serviceTierOptionLocked(); option == nil || option.Select.CurrentValue != "fast" || state.serviceTier != nil {
		t.Fatal("inherited Fast misreported as explicit off")
	}
	state.models[0].DefaultServiceTier = ""
	if option := state.serviceTierOptionLocked(); option == nil || option.Select.CurrentValue != "default" {
		t.Fatal("missing protocol standard tier")
	}
}

// TestServiceTierStandardSurvivesTierlessModelSwitch covers the protocol
// baseline staying selectable across a model change. Fast -> Standard -> a
// model that advertises no additional tiers must switch, and the next Turns
// must carry Standard rather than resurrecting Fast.
func TestServiceTierStandardSurvivesTierlessModelSwitch(t *testing.T) {
	appIn, appOut := io.Pipe()
	adapterIn, adapterOut := io.Pipe()
	defer appIn.Close()
	defer appOut.Close()
	defer adapterIn.Close()
	defer adapterOut.Close()
	fake := newPromptRPCFake(adapterIn, appOut)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	backend, err := NewBackend(ctx, appIn, adapterOut)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	a := &agent{backend: backend, sessions: map[string]*sessionState{}}
	route, err := a.reserveSession("one", t.TempDir(), nil, routeLive)
	if err != nil {
		t.Fatal(err)
	}
	route.state.models = tierModels()
	route.state.model = "model"
	route.state.serviceTier = acp.Ptr("fast")
	state := a.sessions["one"]

	standard := make(chan error, 1)
	go func() { _, err := a.setServiceTier(ctx, state, "default"); standard <- err }()
	list := expectPromptRPCRequest(t, ctx, fake.requests, "model/list")
	fake.respond(list, map[string]any{"data": tierModels()})
	if err := <-standard; err != nil {
		t.Fatal(err)
	}
	if state.serviceTier == nil || *state.serviceTier != "default" {
		t.Fatalf("service tier = %v, want explicit default", state.serviceTier)
	}

	switched := make(chan error, 1)
	go func() {
		_, err := a.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: "one", ConfigId: configIDModel, Value: "other"}})
		switched <- err
	}()
	refresh := expectPromptRPCRequest(t, ctx, fake.requests, "model/list")
	fake.respond(refresh, map[string]any{"data": tierModels()})
	if err := <-switched; err != nil {
		t.Fatalf("Standard blocked a tier-less model switch: %v", err)
	}
	if state.model != "other" || state.serviceTier == nil || *state.serviceTier != "default" {
		t.Fatalf("model = %q, service tier = %v", state.model, state.serviceTier)
	}
	if state.serviceTierOptionLocked() != nil {
		t.Fatal("tier-less model advertised a service tier")
	}

	for _, turnID := range []string{"turn-1", "turn-2"} {
		done := make(chan error, 1)
		go func() {
			_, err := a.Prompt(ctx, acp.PromptRequest{SessionId: "one", Prompt: []acp.ContentBlock{acp.TextBlock("hello")}})
			done <- err
		}()
		start := expectPromptRPCRequest(t, ctx, fake.requests, "turn/start")
		if got := stringValue(start.Params["serviceTier"]); got != "default" {
			t.Fatalf("turn serviceTier = %q, want default", got)
		}
		fake.respond(start, map[string]any{"turn": map[string]any{"id": turnID}})
		fake.notify("turn/completed", map[string]any{"threadId": "one", "turn": map[string]any{"id": turnID, "status": "completed"}})
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestServiceTierBackendCommitIsolationAndTurnRequest(t *testing.T) {
	appIn, appOut := io.Pipe()
	adapterIn, adapterOut := io.Pipe()
	defer appIn.Close()
	defer appOut.Close()
	defer adapterIn.Close()
	defer adapterOut.Close()
	fake := newPromptRPCFake(adapterIn, appOut)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	backend, err := NewBackend(ctx, appIn, adapterOut)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	a := &agent{backend: backend, sessions: map[string]*sessionState{}}
	for _, id := range []string{"one", "two"} {
		route, err := a.reserveSession(id, t.TempDir(), nil, routeLive)
		if err != nil {
			t.Fatal(err)
		}
		route.state.models = tierModels()
		route.state.model = "model"
		route.state.serviceTier = acp.Ptr("fast")
	}
	state := a.sessions["one"]
	changeModel := make(chan error, 1)
	go func() {
		_, err := a.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: "one", ConfigId: configIDModel, Value: "other"}})
		changeModel <- err
	}()
	refresh := expectPromptRPCRequest(t, ctx, fake.requests, "model/list")
	fake.respond(refresh, map[string]any{"data": tierModels()})
	if err := <-changeModel; err == nil || state.model != "model" || *state.serviceTier != "fast" {
		t.Fatal("model change leaked an unsupported tier")
	}
	for _, value := range []string{"default", "fast"} {
		done := make(chan error, 1)
		go func() { _, err := a.setServiceTier(ctx, state, value); done <- err }()
		list := expectPromptRPCRequest(t, ctx, fake.requests, "model/list")
		fake.respond(list, map[string]any{"data": tierModels()})
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if *state.serviceTier != value || *a.sessions["two"].serviceTier != "fast" {
			t.Fatal("cross-session mutation")
		}
	}
	state.effectiveServiceTier = acp.Ptr("default")
	rejected := make(chan error, 1)
	go func() {
		_, err := a.Prompt(ctx, acp.PromptRequest{SessionId: "one", Prompt: []acp.ContentBlock{acp.TextBlock("hello")}})
		rejected <- err
	}()
	denied := expectPromptRPCRequest(t, ctx, fake.requests, "turn/start")
	fake.respondError(denied, -32602, "tier unavailable")
	if err := <-rejected; err == nil || *state.serviceTier != "default" {
		t.Fatal("rejected tier retained")
	}
	state.serviceTier = acp.Ptr("fast")
	done := make(chan error, 1)
	go func() {
		_, err := a.Prompt(ctx, acp.PromptRequest{SessionId: "one", Prompt: []acp.ContentBlock{acp.TextBlock("hello")}})
		done <- err
	}()
	start := expectPromptRPCRequest(t, ctx, fake.requests, "turn/start")
	if stringValue(start.Params["serviceTier"]) != "fast" {
		t.Fatalf("turn params = %v", start.Params)
	}
	if _, ok := start.Params["serviceTierForTurn"]; ok {
		t.Fatal("persistent selection became single-turn override")
	}
	if _, err := a.setServiceTier(ctx, state, "default"); err == nil {
		t.Fatal("tier changed active turn")
	}
	fake.respond(start, map[string]any{"turn": map[string]any{"id": "turn"}})
	fake.notify("turn/completed", map[string]any{"threadId": "one", "turn": map[string]any{"id": "turn", "status": "completed"}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	lost := make(chan error, 1)
	go func() { _, err := a.setServiceTier(ctx, state, "fast"); lost <- err }()
	refresh = expectPromptRPCRequest(t, ctx, fake.requests, "model/list")
	fake.respond(refresh, map[string]any{"data": []codexModel{{ID: "model"}}})
	if err := <-lost; err == nil || state.serviceTierOptionLocked() != nil {
		t.Fatal("lost account/model capability still advertised Fast")
	}
}

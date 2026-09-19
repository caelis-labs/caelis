package codex

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
)

func assertStagedTurnRequest(t *testing.T, params map[string]json.RawMessage, model, tier string) {
	t.Helper()
	if _, ok := params["serviceTierForTurn"]; ok {
		t.Fatal("persistent selection became single-turn override")
	}
	if got := stringValue(params["model"]); got != model {
		t.Fatalf("turn model = %q, want %s", got, model)
	}
	if got := stringValue(params["serviceTier"]); got != tier {
		t.Fatalf("turn serviceTier = %q, want %s", got, tier)
	}
}

func startStagedTurn(t *testing.T, ctx context.Context, a *agent, fake *promptRPCFake, sessionID string) (chan error, promptRPCRequest) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := a.Prompt(ctx, acp.PromptRequest{SessionId: acp.SessionId(sessionID), Prompt: []acp.ContentBlock{acp.TextBlock("hello")}})
		done <- err
	}()
	return done, expectPromptRPCRequest(t, ctx, fake.requests, "turn/start")
}

func completeStagedTurn(t *testing.T, fake *promptRPCFake, done chan error, start promptRPCRequest, sessionID, turnID, model, tier string) {
	t.Helper()
	assertStagedTurnRequest(t, start.Params, model, tier)
	fake.respond(start, map[string]any{"turn": map[string]any{"id": turnID}})
	fake.notify("turn/completed", map[string]any{
		"threadId": sessionID, "turn": map[string]any{"id": turnID, "status": "completed"},
	})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// TestServiceTierRejectedStartKeepsStagedModelAndTier covers the admission
// rejection that follows a completed Fast Turn on model A, an explicit Standard
// selection, and a switch to model B that advertises no additional tiers. The
// rejection must leave the staged model and tier untouched: reverting only the
// tier would retry as B plus model A's Fast, which B never advertised. The next
// Turn carries the same complete B/Standard request and succeeds.
func TestServiceTierRejectedStartKeepsStagedModelAndTier(t *testing.T) {
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

	// A completed Fast Turn on model A establishes the tier the backend
	// actually ran, the state a tier-only revert would resurrect later.
	done, start := startStagedTurn(t, ctx, a, fake, "one")
	completeStagedTurn(t, fake, done, start, "one", "turn-1", "model", "fast")

	standard := make(chan error, 1)
	go func() { _, err := a.setServiceTier(ctx, state, "default"); standard <- err }()
	list := expectPromptRPCRequest(t, ctx, fake.requests, "model/list")
	fake.respond(list, map[string]any{"data": tierModels()})
	if err := <-standard; err != nil {
		t.Fatal(err)
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
		t.Fatalf("model = %q, service tier = %v, want other/default", state.model, state.serviceTier)
	}
	// The tier-less model still publishes the protocol baseline so an explicit
	// Standard selection stays available on the next Turn.
	assertStandardOnly(t, state.serviceTierOptionLocked())

	rejected, denied := startStagedTurn(t, ctx, a, fake, "one")
	assertStagedTurnRequest(t, denied.Params, "other", "default")
	fake.respondError(denied, -32602, "tier unavailable")
	if err := <-rejected; err == nil {
		t.Fatal("backend rejection returned no error")
	}
	if state.model != "other" || state.serviceTier == nil || *state.serviceTier != "default" {
		t.Fatalf("rejection mutated the staged selection: model = %q, service tier = %v", state.model, state.serviceTier)
	}
	if err := route.failure(); err != nil {
		t.Fatalf("proven rejection closed the Session: %v", err)
	}

	done, start = startStagedTurn(t, ctx, a, fake, "one")
	completeStagedTurn(t, fake, done, start, "one", "turn-2", "other", "default")
}

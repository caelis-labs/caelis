package controladapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/modelconfig/providerusage"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/controller"
)

func TestAdapterFullStatusQueriesProviderUsageAndFailsSoft(t *testing.T) {
	t.Parallel()

	providerCalls := 0
	doctorCalls := 0
	usageCalls := 0
	deps := &runtimeDeps{
		Session: SessionRuntimeDeps{Store: inmemory.NewStore(inmemory.Config{})},
		Status: StatusRuntimeDeps{DoctorFn: func(context.Context, DoctorRequest) (DoctorStatusProjection, error) {
			doctorCalls++
			return DoctorStatusProjection{}, nil
		}},
		Model: ModelRuntimeDeps{
			EffectiveAliasFn: func() string { return "openai-codex/gpt-5.6-luna" },
			ConfigFn: func(string) (ModelConfig, bool) {
				return ModelConfig{ContextWindowTokens: 128_000}, true
			},
			SessionUsageSnapshotFn: func(context.Context, session.SessionRef, string) (compact.UsageSnapshot, error) {
				usageCalls++
				// A provider snapshot may omit static capacity; the model catalog
				// remains authoritative for that field.
				return compact.UsageSnapshot{TotalTokens: 1_600}, nil
			},
			ProviderUsageFn: func(context.Context, string) (providerusage.Snapshot, bool, error) {
				providerCalls++
				if providerCalls == 2 {
					return providerusage.Snapshot{}, true, errors.New("temporary usage outage")
				}
				return providerusage.Snapshot{
					Provider: "openai-codex", Plan: "pro",
					Limits: []providerusage.Limit{{ID: "codex", Windows: []providerusage.Window{{
						Kind: "primary", UsedPercent: 5, Duration: 7 * 24 * time.Hour,
					}}}},
				}, true, nil
			},
		},
	}
	driver := newHostAssembler(deps, "surface", "")
	driver.session = session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}}
	driver.hasSession = true

	lightweight, err := driver.LightweightStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if lightweight.Usage.TotalTokens != 0 || lightweight.Usage.ContextWindowTokens != 128_000 ||
		!lightweight.Usage.ContextUsageAvailable || lightweight.Usage.ContextUsageReplace || lightweight.Usage.ContextUsageControllerEpoch != "" {
		t.Fatalf("lightweight usage = %#v, want available provider capacity with merge semantics", lightweight.Usage)
	}
	if providerCalls != 0 || doctorCalls != 0 || usageCalls != 0 || len(lightweight.RateLimits.Limits) != 0 {
		t.Fatalf(
			"lightweight status ran diagnostics: provider=%d doctor=%d usage=%d limits=%#v",
			providerCalls, doctorCalls, usageCalls, lightweight.RateLimits,
		)
	}
	status, err := driver.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if providerCalls != 1 || doctorCalls != 1 || usageCalls != 1 || status.RateLimits.Plan != "pro" || len(status.RateLimits.Limits) != 1 {
		t.Fatalf("full status = provider=%d doctor=%d usage=%d rate limits=%#v", providerCalls, doctorCalls, usageCalls, status.RateLimits)
	}
	if status.Usage.TotalTokens != 1_600 || status.Usage.ContextWindowTokens != 128_000 {
		t.Fatalf("full usage = %#v, want dynamic total with static capacity", status.Usage)
	}
	status, err = driver.Status(context.Background())
	if err != nil {
		t.Fatalf("temporary provider usage failure escaped /status: %v", err)
	}
	if providerCalls != 2 || doctorCalls != 2 || usageCalls != 2 || len(status.RateLimits.Limits) != 0 {
		t.Fatalf("failed provider usage should be omitted: provider=%d doctor=%d usage=%d rate limits=%#v", providerCalls, doctorCalls, usageCalls, status.RateLimits)
	}
}

func TestSessionUsageTreatsACPContextUpdatesAsReplaceableGauges(t *testing.T) {
	t.Parallel()

	events := []*session.Event{
		{ContextUsage: &session.ContextUsageSnapshot{Size: 200000, Used: 12000}, Invocation: &session.EventInvocation{Provider: "codex", Model: "gpt-test"}},
		{ContextUsage: &session.ContextUsageSnapshot{Size: 200000, Used: 42000}, Invocation: &session.EventInvocation{Provider: "codex", Model: "gpt-test"}},
		{ContextUsage: &session.ContextUsageSnapshot{Size: 100000, Used: 5000}, Invocation: &session.EventInvocation{Provider: "claude", Model: "sonnet"}, Scope: &session.EventScope{Participant: session.ParticipantRef{ID: "child-1", Kind: session.ParticipantKindACP, Role: session.ParticipantRoleDelegated}}},
	}
	breakdown := sessionTokenUsageBreakdownFromEvents(events, tokenUsageCategoryMain)
	if breakdown.Total.TotalTokens != 47000 || breakdown.Main.TotalTokens != 42000 || breakdown.Subagents.TotalTokens != 5000 {
		t.Fatalf("ACP usage breakdown = %#v, want latest main plus latest child gauges", breakdown)
	}
}

func TestACPControllerUsageFeedsLightweightStatusAndSessionStatistics(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := inmemory.NewStore(inmemory.Config{})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1", PreferredSessionID: "session-acp"})
	if err != nil {
		t.Fatal(err)
	}
	active, err = store.BindController(ctx, session.BindControllerRequest{
		SessionRef: active.SessionRef,
		Binding: session.ControllerBinding{
			Kind: session.ControllerKindACP, ControllerID: "codex", AgentName: "codex", EpochID: "epoch-1",
			Placement: placement.Placement{Kind: placement.KindAgent, Agent: "codex", Model: "gpt-test"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{
		Type: session.EventTypeCustom, Visibility: session.VisibilityMirror,
		Scope:        &session.EventScope{Controller: session.ControllerRef{Kind: session.ControllerKindACP, ID: "codex", EpochID: "epoch-1"}},
		Invocation:   &session.EventInvocation{Provider: "codex", Model: "gpt-test"},
		ContextUsage: &session.ContextUsageSnapshot{Size: 200000, Used: 42000},
	}}); err != nil {
		t.Fatal(err)
	}
	deps := &runtimeDeps{Session: SessionRuntimeDeps{Store: store}}
	driver, err := newAssemblerForSession(ctx, deps, active, "tui", "")
	if err != nil {
		t.Fatal(err)
	}
	lightweight, err := driver.LightweightStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if lightweight.Usage.TotalTokens != 42000 || lightweight.Usage.ContextWindowTokens != 200000 ||
		!lightweight.Usage.ContextUsageAvailable || !lightweight.Usage.ContextUsageReplace ||
		lightweight.Usage.ContextUsageControllerEpoch != "epoch-1" {
		t.Fatalf("lightweight ACP usage = %#v, want available replace used/size gauge", lightweight.Usage)
	}
	full, err := driver.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if full.Usage.SessionUsageTotal.TotalTokens != 42000 || len(full.Usage.SessionUsageByModel) != 1 ||
		full.Usage.SessionUsageByModel[0].Provider != "codex" || full.Usage.SessionUsageByModel[0].Model != "gpt-test" {
		t.Fatalf("full ACP usage = %#v, want one attributed controller gauge", full.Usage)
	}
	if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{
		Type: session.EventTypeCustom, Visibility: session.VisibilityMirror,
		Scope:        &session.EventScope{Controller: session.ControllerRef{Kind: session.ControllerKindACP, ID: "other", EpochID: "epoch-other"}},
		Invocation:   &session.EventInvocation{Provider: "other", Model: "other-model"},
		ContextUsage: &session.ContextUsageSnapshot{Size: 100000, Used: 7000},
	}}); err != nil {
		t.Fatal(err)
	}
	withLaterNonmatchingGauge, err := driver.LightweightStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if withLaterNonmatchingGauge.Usage.TotalTokens != 42000 || !withLaterNonmatchingGauge.Usage.ContextUsageAvailable {
		t.Fatalf("later nonmatching gauge hid current controller usage: %#v", withLaterNonmatchingGauge.Usage)
	}
	withLaterNonmatchingGaugeFull, err := driver.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if withLaterNonmatchingGaugeFull.Usage.SessionUsageTotal.TotalTokens != 42000 {
		t.Fatalf("later nonmatching gauge hid current Session usage: %#v", withLaterNonmatchingGaugeFull.Usage)
	}
	currentControllerModel := "gpt-new"
	deps.Agent.ControllerStatusFn = func(context.Context, session.SessionRef) (controller.ControllerStatus, bool, error) {
		return controller.ControllerStatus{Agent: "codex", Model: currentControllerModel}, true, nil
	}
	afterModelSwitch, err := driver.LightweightStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterModelSwitch.Usage.TotalTokens != 0 || afterModelSwitch.Usage.ContextWindowTokens != 0 ||
		afterModelSwitch.Usage.ContextUsageAvailable || !afterModelSwitch.Usage.ContextUsageReplace ||
		afterModelSwitch.Usage.ContextUsageControllerEpoch != "epoch-1" {
		t.Fatalf("usage after ACP model switch = %#v, want unavailable replace gauge with stale prior-model usage omitted", afterModelSwitch.Usage)
	}
	afterModelSwitchFull, err := driver.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterModelSwitchFull.Usage.SessionUsageTotal.TotalTokens != 0 || len(afterModelSwitchFull.Usage.SessionUsageByModel) != 0 {
		t.Fatalf("session usage after ACP model switch = %#v, want stale prior-model gauge excluded", afterModelSwitchFull.Usage)
	}
	if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{
		Type: session.EventTypeCustom, Visibility: session.VisibilityMirror,
		Scope:        &session.EventScope{Controller: session.ControllerRef{Kind: session.ControllerKindACP, ID: "codex", EpochID: "epoch-1"}},
		Invocation:   &session.EventInvocation{Provider: "codex", Model: "gpt-new"},
		ContextUsage: &session.ContextUsageSnapshot{Size: 200000, Used: 8000},
	}}); err != nil {
		t.Fatal(err)
	}
	modelBGauge, err := driver.LightweightStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if modelBGauge.Usage.TotalTokens != 8000 || !modelBGauge.Usage.ContextUsageAvailable {
		t.Fatalf("model B usage = %#v, want fresh B gauge", modelBGauge.Usage)
	}
	currentControllerModel = "gpt-test"
	afterABA, err := driver.LightweightStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterABA.Usage.TotalTokens != 0 || afterABA.Usage.ContextUsageAvailable {
		t.Fatalf("usage after A-B-A model switch = %#v, want old A gauge to remain unavailable", afterABA.Usage)
	}
	afterABAFull, err := driver.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterABAFull.Usage.SessionUsageTotal.TotalTokens != 0 || len(afterABAFull.Usage.SessionUsageByModel) != 0 {
		t.Fatalf("Session usage after A-B-A model switch = %#v, want old A gauge excluded", afterABAFull.Usage)
	}
}

func TestSessionUsageIncludesACPSubagentTaskGauge(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := inmemory.NewStore(inmemory.Config{})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1", PreferredSessionID: "session-parent"})
	if err != nil {
		t.Fatal(err)
	}
	deps := &runtimeDeps{
		Session: SessionRuntimeDeps{Store: store},
		Status: StatusRuntimeDeps{TaskEntriesFn: func(context.Context, session.SessionRef) ([]*taskapi.Entry, error) {
			return []*taskapi.Entry{{
				TaskID: "task-1", Kind: taskapi.KindSubagent,
				ContextUsage: &taskapi.ContextUsageRecord{
					Snapshot:   session.ContextUsageSnapshot{Size: 200000, Used: 42000},
					Invocation: session.EventInvocation{Provider: "codex", Model: "gpt-test"},
				},
			}}, nil
		}},
	}
	driver, err := newAssemblerForSession(ctx, deps, active, "tui", "")
	if err != nil {
		t.Fatal(err)
	}
	breakdown, err := driver.sessionTokenUsageBreakdown(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if breakdown.Total.TotalTokens != 42000 || breakdown.Subagents.TotalTokens != 42000 || breakdown.Main.TotalTokens != 0 {
		t.Fatalf("Task ACP usage breakdown = %#v, want subagent-only gauge", breakdown)
	}
	if len(breakdown.ByModel) != 1 {
		t.Fatalf("Task ACP by-model usage = %#v, want one model", breakdown.ByModel)
	}
}

func TestSessionUsageSkipsACPTaskGaugeWhenLocalChildSessionIsAggregated(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := inmemory.NewStore(inmemory.Config{})
	parent, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1", PreferredSessionID: "session-parent"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1", PreferredSessionID: "session-child"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: child.SessionRef, Event: &session.Event{
		Type: session.EventTypeAssistant, Visibility: session.VisibilityMirror,
		Invocation: &session.EventInvocation{Provider: "grok", Model: "grok-4"},
		Meta: map[string]any{"usage": map[string]any{
			"prompt_tokens": 8000, "completion_tokens": 2000, "total_tokens": 10000,
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: parent.SessionRef,
		Binding: session.ParticipantBinding{
			ID: "child-1", Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleDelegated,
			SessionID: child.SessionID, DelegationID: "task-local",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: parent.SessionRef,
		Binding: session.ParticipantBinding{
			ID: "remote-1", Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleDelegated,
			SessionID: "remote-acp-session", DelegationID: "task-remote",
		},
	}); err != nil {
		t.Fatal(err)
	}
	deps := &runtimeDeps{
		Session: SessionRuntimeDeps{Store: store},
		Status: StatusRuntimeDeps{TaskEntriesFn: func(context.Context, session.SessionRef) ([]*taskapi.Entry, error) {
			return []*taskapi.Entry{
				{
					TaskID: "task-local", Kind: taskapi.KindSubagent,
					ContextUsage: &taskapi.ContextUsageRecord{
						Snapshot:   session.ContextUsageSnapshot{Size: 200000, Used: 42000},
						Invocation: session.EventInvocation{Provider: "grok", Model: "grok-4"},
					},
				},
				{
					TaskID: "task-remote", Kind: taskapi.KindSubagent,
					ContextUsage: &taskapi.ContextUsageRecord{
						Snapshot:   session.ContextUsageSnapshot{Size: 100000, Used: 7000},
						Invocation: session.EventInvocation{Provider: "codex", Model: "gpt-test"},
					},
				},
			}, nil
		}},
	}
	driver, err := newAssemblerForSession(ctx, deps, parent, "tui", "")
	if err != nil {
		t.Fatal(err)
	}
	breakdown, err := driver.sessionTokenUsageBreakdown(ctx, parent.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if breakdown.Total.TotalTokens != 17000 || breakdown.Subagents.TotalTokens != 17000 || breakdown.Main.TotalTokens != 0 {
		t.Fatalf("usage breakdown = %#v, want local child session plus remote Task gauge only", breakdown)
	}
	if len(breakdown.ByModel) != 2 {
		t.Fatalf("by-model usage = %#v, want local child and remote Task models", breakdown.ByModel)
	}
	local := breakdown.ByModel["grok\x00grok-4"]
	remote := breakdown.ByModel["codex\x00gpt-test"]
	if local.Usage.TotalTokens != 10000 || remote.Usage.TotalTokens != 7000 {
		t.Fatalf("by-model usage = %#v, want child 10000 and remote 7000", breakdown.ByModel)
	}
}

func TestStatusRateLimitsPreservesLabeledWindowWithoutKnownDuration(t *testing.T) {
	t.Parallel()

	status := statusRateLimitsFromProviderUsage(providerusage.Snapshot{
		Provider: "xai",
		Limits: []providerusage.Limit{{
			ID: "xai",
			Windows: []providerusage.Window{{
				Kind:        "credits",
				Label:       "Credit limit",
				UsedPercent: 37.5,
			}},
		}},
	})
	if len(status.Limits) != 1 || len(status.Limits[0].Windows) != 1 {
		t.Fatalf("rate limits = %#v", status)
	}
	window := status.Limits[0].Windows[0]
	if window.Label != "Credit limit" || window.DurationMinutes != 0 || window.UsedPercent != 37.5 {
		t.Fatalf("window = %#v", window)
	}
}

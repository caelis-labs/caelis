package controladapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/control/modelconfig/providerusage"
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
	if lightweight.Usage.TotalTokens != 0 || lightweight.Usage.ContextWindowTokens != 128_000 {
		t.Fatalf("lightweight usage = %#v, want static context window only", lightweight.Usage)
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

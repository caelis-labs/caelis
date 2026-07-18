package controladapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/control/modelconfig/providerusage"
)

func TestAdapterFullStatusQueriesProviderUsageAndFailsSoft(t *testing.T) {
	t.Parallel()

	calls := 0
	stack := &RuntimeStack{
		Session: SessionRuntimeDeps{Store: inmemory.NewStore(inmemory.Config{})},
		Model: ModelRuntimeDeps{
			DefaultAliasFn: func() string { return "openai-codex/gpt-5.6-luna" },
			ProviderUsageFn: func(context.Context, string) (providerusage.Snapshot, bool, error) {
				calls++
				if calls == 2 {
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
	driver := newAdapterForStack(stack, "surface", "")
	driver.session = session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}}
	driver.hasSession = true

	lightweight, err := driver.LightweightStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || len(lightweight.RateLimits.Limits) != 0 {
		t.Fatalf("lightweight status queried provider usage: calls=%d limits=%#v", calls, lightweight.RateLimits)
	}
	status, err := driver.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || status.RateLimits.Plan != "pro" || len(status.RateLimits.Limits) != 1 {
		t.Fatalf("full status = calls=%d rate limits=%#v", calls, status.RateLimits)
	}
	status, err = driver.Status(context.Background())
	if err != nil {
		t.Fatalf("temporary provider usage failure escaped /status: %v", err)
	}
	if calls != 2 || len(status.RateLimits.Limits) != 0 {
		t.Fatalf("failed provider usage should be omitted: calls=%d rate limits=%#v", calls, status.RateLimits)
	}
}

package runtime

import (
	"fmt"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type idleRetryProviderToolModel struct {
	*idleProbeModel
	usesProviderTools bool
}

func (m *idleRetryProviderToolModel) UsesProviderExecutedTools(*model.Request) bool {
	return m.usesProviderTools
}

func TestRuntimeIdleCompactionChecksRetryWrappedProviderTools(t *testing.T) {
	for _, usesProviderTools := range []bool{false, true} {
		t.Run(fmt.Sprintf("provider-tools=%v", usesProviderTools), func(t *testing.T) {
			h := newAsyncHarness(t, "")
			h.rt.compaction.Async.Enabled = false
			h.rt.compaction.Idle.Enabled = true
			t.Cleanup(h.rt.DiscardIdleCompactionSources)
			m := &idleRetryProviderToolModel{idleProbeModel: &idleProbeModel{asyncProbeModel: h.llm}, usesProviderTools: usesProviderTools}
			h.source.set(agent.ModelRequestSnapshot{Revision: "epoch-1", Model: model.WithRetry(m, model.RetryConfig{})})
			history := model.NewTextMessage(model.RoleAssistant, strings.Repeat("synthetic durable context fact; ", 2500))
			if _, err := h.store.AppendEvent(h.ctx, session.AppendEventRequest{SessionRef: h.active.SessionRef, MutationGuard: session.RuntimeMutationGuard(h.ctx), Event: &session.Event{Type: session.EventTypeAssistant, Message: &history, Scope: &session.EventScope{TurnID: "old-turn"}}}); err != nil {
				t.Fatal(err)
			}
			run := h.run(t)
			receiveSnapshotValue(t, h.llm.foreground)
			h.llm.responses <- asyncTextResponse("final durable answer", 24000)
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			job, err := h.rt.PrepareIdleCompaction(t.Context(), h.active.SessionRef)
			if job != nil {
				defer job.Close()
			}
			if err != nil || (job == nil) != usesProviderTools {
				t.Fatalf("idle job available=%v with provider tools=%v, err=%v", job != nil, usesProviderTools, err)
			}
		})
	}
}

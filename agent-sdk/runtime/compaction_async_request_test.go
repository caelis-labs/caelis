package runtime

import (
	"context"
	"iter"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/requestsource"
)

// Hook the assembled chat request before the Runtime gate can load the Store.
type asyncAssemblyFactory struct {
	hook func(*model.Request)
}

func (f asyncAssemblyFactory) NewAgent(ctx context.Context, spec agent.AgentSpec) (agent.Agent, error) {
	resolve := spec.ResolveModelRequest
	spec.ResolveModelRequest = func(ctx context.Context) (agent.ModelRequestSnapshot, error) {
		snapshot, err := resolve(ctx)
		if err == nil {
			snapshot.Model = &asyncAssemblyModel{LLM: snapshot.Model, hook: f.hook}
		}
		return snapshot, err
	}
	return (chat.Factory{}).NewAgent(ctx, spec)
}

type asyncAssemblyModel struct {
	model.LLM
	hook func(*model.Request)
}

func (*asyncAssemblyModel) TracksInvocations()                 {}
func (m *asyncAssemblyModel) Capabilities() model.Capabilities { return runtimeTestModelCapabilities() }
func (m *asyncAssemblyModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.hook(req)
	return model.Generate(ctx, m.LLM, req)
}

func TestAsyncCompactionFailedInContextNeverGeneratesColdCheckpoint(t *testing.T) {
	for _, failure := range []string{"empty", "length", "tools"} {
		t.Run(failure, func(t *testing.T) {
			h := newAsyncHarness(t, "")
			run := h.run(t)
			next := h.startCandidate(t)
			response := asyncTextResponse("", 2000)
			switch failure {
			case "length":
				response = asyncTextResponse(asyncFixtureSummary, 2000)
				response.response.FinishReason = model.FinishReasonLength
				response.response.FinishReasonReported = true
			case "tools":
				response = asyncToolResponse("forbidden")
			}
			h.llm.summaries <- response
			receiveSnapshotValue(t, next.scope.job.done)
			if next.scope.job.err == nil {
				t.Fatal("failed generation accepted")
			}
			h.llm.responses <- asyncToolResponse("second")
			receiveSnapshotValue(t, h.secondTool)
			close(h.releaseSecond)
			receiveSnapshotValue(t, h.llm.foreground)
			h.llm.responses <- asyncTextResponse("done", 500)
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case <-h.llm.background:
				t.Fatal("strict generation issued another request")
			default:
			}
			if _, _, ok := compact.LatestCompactEvent(h.load(t).Events); ok {
				t.Fatal("failed generation wrote checkpoint")
			}
		})
	}
}

func TestAsyncCompactionRejectsUnboundOrChangedRequest(t *testing.T) {
	for _, scenario := range []string{"unbound", "source_hash", "messages_hash", "open_call", "parallel_open_call"} {
		t.Run(scenario, func(t *testing.T) {
			h := newAsyncHarness(t, "")
			calls := 0
			h.rt.agentFactory = asyncAssemblyFactory{hook: func(req *model.Request) {
				calls++
				if calls != 2 {
					return
				}
				if req.Source == nil {
					t.Error("second request has no assembly binding")
					return
				}
				switch scenario {
				case "unbound":
					req.Source = nil
				case "source_hash":
					req.Source.SourcePrefixHash = "mismatch"
				case "messages_hash":
					req.Source.MessagesHash = "mismatch"
				default:
					calls := []model.ToolCall{{ID: "open-a", Name: "work", Args: "{}"}}
					if scenario == "parallel_open_call" {
						calls = append(calls, model.ToolCall{ID: "open-b", Name: "work", Args: "{}"})
					}
					req.Messages = append(req.Messages, model.MessageFromToolCalls(model.RoleAssistant, calls, ""))
					req.Source.MessagesHash, _ = requestsource.Fingerprint(req.Messages)
				}
			}}
			run := h.run(t)
			receiveSnapshotValue(t, h.llm.foreground)
			h.llm.responses <- asyncToolResponse("first")
			next := receiveSnapshotValue(t, h.llm.foreground)
			if next.scope.job != nil {
				t.Error("ineligible request launched a candidate")
			}
			h.llm.responses <- asyncTextResponse("done", 500)
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case <-h.llm.background:
				t.Fatal("ineligible request generated a summary")
			default:
			}
			if _, _, ok := compact.LatestCompactEvent(h.load(t).Events); ok {
				t.Fatal("ineligible request wrote checkpoint")
			}
		})
	}
}

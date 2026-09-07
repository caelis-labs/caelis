package chat

import (
	"context"
	"iter"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type accountingGapModel struct{ calls int }

func (*accountingGapModel) Name() string { return "accounting-gap" }
func (m *accountingGapModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	message := model.NewTextMessage(model.RoleAssistant, "done")
	if m.calls == 1 {
		message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{Name: "echo", Args: "{}"}}, "")
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(model.StreamEventFromResponse(&model.Response{Message: message, TurnComplete: true, Provider: "probe", Model: m.Name(), Usage: model.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}}), nil)
	}
}
func TestRepairRetainsEveryProviderInvocation(t *testing.T) {
	llm := &accountingGapModel{}
	a, err := New("main", llm, "")
	if err != nil {
		t.Fatal(err)
	}
	recorded, tokens := 0, 0
	var events []*session.Event
	for event, err := range a.Run(agent.NewContext(agent.ContextSpec{Context: context.Background()})) {
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	for _, event := range session.InvocationAccountingEvents(events) {
		if usage := session.UsageSnapshotFromSessionEvent(event); usage != nil {
			recorded++
			tokens += usage.TotalTokens
		}
	}
	if llm.calls != 2 || recorded != 2 || tokens != 24 {
		t.Fatalf("actual calls=%d; recorded usage events=%d; total tokens=%d; want 2/2/24", llm.calls, recorded, tokens)
	}
}

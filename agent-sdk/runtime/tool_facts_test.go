package runtime

import (
	"context"
	"encoding/json"
	"iter"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func toolResultJSON(result *tool.Result) []byte { raw, _ := json.Marshal(result); return raw }
func missingToolPairs(t interface {
	Helper()
	Fatal(...any)
}, events []*session.Event) int {
	t.Helper()
	pairs, err := session.PairToolFacts(events)
	if err != nil {
		t.Fatal(err)
	}
	missing := 0
	for _, pair := range pairs {
		if pair.Result == nil {
			missing++
		}
	}
	return missing
}
func newToolFactTestSession(t *testing.T, id string) (*inmemory.Store, session.Session) {
	t.Helper()
	service := inmemory.NewStore(inmemory.Config{SessionIDGenerator: func() string { return id }})
	active, err := service.StartSession(t.Context(), session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	return service, active
}

func factJSON(value any) []byte { raw, _ := json.Marshal(value); return raw }

type recoveryCaptureModel struct{ messages []model.Message }

func (*recoveryCaptureModel) Name() string { return "recovery-capture" }

func (*recoveryCaptureModel) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalls: true}
}

func (m *recoveryCaptureModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.messages = model.CloneMessages(req.Messages)
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(model.StreamEventFromResponse(&model.Response{Message: model.NewTextMessage(model.RoleAssistant, "done"), TurnComplete: true, StepComplete: true, Status: model.ResponseStatusCompleted}), nil)
	}
}

func canonicalMessages(events []*session.Event) []model.Message {
	var messages []model.Message
	for _, event := range events {
		if !session.IsMainInvocationVisibleEvent(event) {
			continue
		}
		if message, ok := session.ModelMessageOf(event); ok {
			messages = append(messages, message)
		}
	}
	return messages
}

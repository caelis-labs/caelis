package collaboration

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

func TestSendMessageReturnedMailSurvivesModelContextRoundTrip(t *testing.T) {
	t.Parallel()
	service := openTestService(t, &testBackend{})
	incoming, err := service.Send(t.Context(), Identity{"work", "a"}, "b", "Continue with the review.", "")
	if err != nil {
		t.Fatal(err)
	}
	tools := Tools(false, func(ctx context.Context, req Request) (json.RawMessage, error) {
		return service.Call(ctx, Identity{"work", "b"}, req)
	})
	liveModel := &mailboxRoundTripModel{send: true}
	live, err := chat.NewWithTools("child", liveModel, tools, "")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "caelis", UserID: "test", Workspace: session.WorkspaceRef{Key: "work", CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	user := model.NewTextMessage(model.RoleUser, "Report progress.")
	events := []*session.Event{{Type: session.EventTypeUser, Text: user.TextContent(), Message: &user}}
	for event, err := range live.Run(agent.NewContext(agent.ContextSpec{Context: t.Context(), Session: active, Events: events})) {
		if err != nil {
			t.Fatal(err)
		}
		if event != nil {
			events = append(events, event)
		}
	}
	if len(liveModel.requests) != 2 {
		t.Fatalf("model calls = %d", len(liveModel.requests))
	}
	if len(liveModel.requests[0].Tools) != 2 {
		t.Fatalf("child model tools = %#v", liveModel.requests[0].Tools)
	}
	liveResult := sendResultPartJSON(t, liveModel.requests[1])
	if strings.Count(liveResult, incoming.ID) != 1 || !strings.Contains(liveResult, incoming.Text) {
		t.Fatalf("live model lost or repeated returned mail: %s", liveResult)
	}
	for _, event := range events {
		if _, err := store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: event}); err != nil {
			t.Fatal(err)
		}
	}
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	loaded, err := reopened.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	reloadModel := &mailboxRoundTripModel{}
	resumed, err := chat.NewWithTools("child", reloadModel, tools, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range resumed.Run(agent.NewContext(agent.ContextSpec{Context: t.Context(), Session: loaded.Session, Events: loaded.Events})) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(reloadModel.requests) != 1 {
		t.Fatalf("resumed model calls = %d", len(reloadModel.requests))
	}
	if got := sendResultPartJSON(t, reloadModel.requests[0]); got != liveResult {
		t.Fatalf("reloaded result = %s, want %s", got, liveResult)
	}
	if mail, err := service.Receive(t.Context(), Identity{"work", "b"}); err != nil || len(mail) != 0 {
		t.Fatalf("replayed consumed mail: %v, %v", mail, err)
	}
}

func sendResultPartJSON(t *testing.T, req *model.Request) string {
	t.Helper()
	var results []model.Part
	for _, message := range req.Messages {
		for _, part := range message.Parts {
			if part.ToolResult != nil {
				results = append(results, part)
			}
		}
	}
	if len(results) != 1 {
		t.Fatalf("tool result parts = %#v", results)
	}
	raw, err := json.Marshal(results[0])
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

type mailboxRoundTripModel struct {
	send     bool
	requests []*model.Request
}

func (*mailboxRoundTripModel) Name() string { return "mailbox-roundtrip" }
func (m *mailboxRoundTripModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.requests = append(m.requests, model.CloneRequest(req))
	return func(yield func(*model.StreamEvent, error) bool) {
		response := &model.Response{Message: model.NewTextMessage(model.RoleAssistant, "done"), TurnComplete: true, StepComplete: true, Status: model.ResponseStatusCompleted, FinishReason: model.FinishReasonStop}
		if m.send {
			m.send = false
			response.Message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "send-progress", Name: "SendMessage", Args: `{"to":"a","message":"Review in progress."}`}}, "")
			response.FinishReason = model.FinishReasonToolCalls
		}
		yield(&model.StreamEvent{Type: model.StreamEventTurnDone, Response: response}, nil)
	}
}

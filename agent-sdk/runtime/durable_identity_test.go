package runtime

import (
	"context"
	"iter"
	"reflect"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestDefaultDurableIdentitiesSurviveThreeRuntimeRestarts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	service := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root}))
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "identity-user"})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	var previousContext []model.Message
	seenRunIDs := map[string]bool{}
	for index := 1; index <= 3; index++ {
		service = sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root}))
		probe := &durableIdentityModel{text: "assistant-" + string(rune('0'+index))}
		runtime, err := New(Config{Sessions: service, AgentFactory: chat.Factory{}})
		if err != nil {
			t.Fatalf("New(runtime %d) error = %v", index, err)
		}
		input := "user-" + string(rune('0'+index))
		result, err := runtime.Run(context.Background(), agent.RunRequest{
			SessionRef: active.SessionRef,
			Input:      input,
			AgentSpec:  agent.AgentSpec{Name: "chat", Model: probe},
		})
		if err != nil {
			t.Fatalf("Run(runtime %d) error = %v", index, err)
		}
		if seenRunIDs[result.Handle.RunID()] {
			t.Fatalf("runtime %d reused durable run id %q", index, result.Handle.RunID())
		}
		seenRunIDs[result.Handle.RunID()] = true
		if _, err := drainRunnerEvents(t, result.Handle); err != nil {
			t.Fatalf("drain runtime %d error = %v", index, err)
		}

		wantContext := append(model.CloneMessages(previousContext), model.NewTextMessage(model.RoleUser, input))
		if !reflect.DeepEqual(probe.messages, wantContext) {
			t.Fatalf("runtime %d model context = %#v, want rebuilt %#v", index, probe.messages, wantContext)
		}
		previousContext = append(wantContext, model.NewTextMessage(model.RoleAssistant, probe.text))
	}

	reopened := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root}))
	events, err := reopened.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	eventIDs := map[string]bool{}
	runIDs := map[string]bool{}
	turnIDs := map[string]bool{}
	for _, event := range events {
		if event.ID == "" || eventIDs[event.ID] {
			t.Fatalf("durable event id %q is empty or repeated", event.ID)
		}
		eventIDs[event.ID] = true
		if event.Journal == nil || event.Journal.Execution == nil {
			continue
		}
		record := event.Journal.Execution
		switch record.Kind {
		case session.JournalKindRun:
			runIDs[record.RunID] = true
		case session.JournalKindTurn:
			turnIDs[record.TurnID] = true
		}
	}
	if len(runIDs) != 3 || len(turnIDs) != 3 {
		t.Fatalf("durable identities: runs=%v turns=%v, want three unique of each", runIDs, turnIDs)
	}
}

func TestDefaultPauseTokenIdentitySurvivesRuntimeRestart(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	service := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root}))
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "pause-user"})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	decision := agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}
	seenTokens := map[string]bool{}
	for index := 0; index < 2; index++ {
		service = sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root}))
		runtime, err := New(Config{Sessions: service, AgentFactory: chat.Factory{}})
		if err != nil {
			t.Fatalf("New(runtime %d) error = %v", index, err)
		}
		runID := runtime.nextID("run", nil)
		turnID := runtime.nextID("turn", nil)
		if err := runtime.startRunTurnJournal(context.Background(), active.SessionRef, runID, turnID); err != nil {
			t.Fatalf("startRunTurnJournal(runtime %d) error = %v", index, err)
		}
		result := make(chan error, 1)
		go func() {
			_, requestErr := runtime.requestDurableApproval(context.Background(), agent.ApprovalRequest{
				SessionRef: active.SessionRef,
				Session:    active,
				RunID:      runID,
				TurnID:     turnID,
				Tool:       tool.Definition{Name: "WRITE"},
				Call:       tool.Call{ID: runtime.nextID("call", nil), Name: "WRITE"},
			}, nil)
			result <- requestErr
		}()

		tokenID := waitForPendingPauseToken(t, service, active.SessionRef, runID)
		if seenTokens[tokenID] {
			t.Fatalf("runtime %d reused pause token %q", index, tokenID)
		}
		seenTokens[tokenID] = true
		if err := runtime.ResolveApproval(context.Background(), agent.ResolveApprovalRequest{SessionRef: active.SessionRef, TokenID: tokenID, Decision: decision}); err != nil {
			t.Fatalf("ResolveApproval(runtime %d) error = %v", index, err)
		}
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("requestDurableApproval(runtime %d) error = %v", index, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("requestDurableApproval(runtime %d) did not wake", index)
		}
		if err := runtime.transitionRunTurnJournal(context.Background(), active.SessionRef, runID, turnID, session.ExecutionSucceeded, ""); err != nil {
			t.Fatalf("transitionRunTurnJournal(runtime %d) error = %v", index, err)
		}
	}
}

func waitForPendingPauseToken(t *testing.T, service session.Service, ref session.SessionRef, runID string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		events, err := service.Events(context.Background(), session.EventsRequest{SessionRef: ref, IncludeTransient: true})
		if err != nil {
			t.Fatalf("Events() error = %v", err)
		}
		for _, event := range events {
			if event.Journal != nil && event.Journal.PauseToken != nil && event.Journal.PauseToken.RunID == runID && event.Journal.PauseToken.Status == session.PauseTokenPending {
				return event.Journal.PauseToken.TokenID
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("pending pause token for run %q was not persisted", runID)
	return ""
}

type durableIdentityModel struct {
	text     string
	messages []model.Message
}

func (m *durableIdentityModel) Name() string { return "durable-identity" }

func (m *durableIdentityModel) Capabilities() model.Capabilities {
	return staticModel{}.Capabilities()
}

func (m *durableIdentityModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.messages = model.CloneMessages(req.Messages)
	return staticModel{text: m.text}.Generate(ctx, req)
}

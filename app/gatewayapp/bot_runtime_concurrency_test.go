package gatewayapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
)

func TestBotRuntimeConcurrentInputCancelAndSettings(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	entered := make(chan map[string]any, 1)
	workRequests := make(chan map[string]any, 1)
	client := &http.Client{Transport: gatewayAppRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			return nil, err
		}
		messages, _ := payload["messages"].([]any)
		input := botWireMessageText(messages[len(messages)-1])
		if input == "block this reply" {
			entered <- payload
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		if input == "work while bot is running" {
			workRequests <- payload
		}
		if input == "fail this reply" {
			return nil, botRuntimeTerminalProviderError{}
		}
		body := "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
	storeDir, workspace := t.TempDir(), t.TempDir()
	stack, err := newGatewayAppTestStack(t, Config{
		StoreDir: storeDir, WorkspaceCWD: workspace,
		Model: ModelConfig{Provider: "openai-compatible", API: providers.APIOpenAICompatible, Model: "gpt-4.1", BaseURL: "https://provider.invalid/v1", Token: "test-token", HTTPClient: client},
	})
	if err != nil {
		t.Fatal(err)
	}
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	firstWindow, err := appserver.BindSessionClient(stack.ControlClient(), principal)
	if err != nil {
		t.Fatal(err)
	}
	secondWindow, err := appserver.BindSessionClient(stack.ControlClient(), principal)
	if err != nil {
		t.Fatal(err)
	}
	created, err := stack.Bots().CreateBot(ctx, principal, appserver.CreateBotRequest{
		WriteBase: appserver.WriteBase{OperationID: "create-concurrent-bot"}, Config: bot.Config{Name: "Concurrent bot"},
	})
	if err != nil || created.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("CreateBot = %#v, %v", created, err)
	}
	value, err := stack.Bots().GetBot(ctx, principal, created.Resource.Ref)
	if err != nil {
		t.Fatal(err)
	}
	ref := session.SessionRef{SessionID: value.SessionID}
	releaseObservation, err := stack.sessionRuntimes.retainObservation(ref)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(releaseObservation)
	runtime := activateSessionRuntime(t, stack, value.SessionID)
	first, err := firstWindow.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "first-bot-turn", SessionID: value.SessionID}, Input: "block this reply"})
	if err != nil || first.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("first Prompt = %#v, %v", first, err)
	}
	select {
	case request := <-entered:
		if tools, _ := request["tools"].([]any); len(tools) != 0 {
			t.Fatal("Bot request gained tools")
		}
	case <-ctx.Done():
		t.Fatal("Bot provider never entered")
	}
	second, err := secondWindow.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "second-bot-turn", SessionID: value.SessionID}, Input: "overlapping message"})
	if err == nil || second.Outcome != appserver.OutcomeConflicted {
		t.Fatalf("overlapping Prompt = %#v, %v", second, err)
	}
	value, err = stack.Bots().GetBot(ctx, principal, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	next := value.Config
	next.Description = "New settings after cancellation."
	updated, err := stack.Bots().UpdateBot(ctx, principal, appserver.UpdateBotRequest{
		WriteBase: appserver.WriteBase{OperationID: "update-during-bot-turn", SessionID: value.SessionID, ExpectedRevision: &value.Revision}, BotID: value.ID, Config: next,
	})
	if err == nil || updated.Outcome != appserver.OutcomeConflicted {
		t.Fatalf("active UpdateBot = %#v, %v", updated, err)
	}
	unchanged, err := stack.Bots().GetBot(ctx, principal, value.ID)
	if err != nil || unchanged.Config != value.Config {
		t.Fatalf("rejected settings changed Bot = %#v, %v", unchanged, err)
	}

	work, err := startGatewayAppTestSession(ctx, stack, "parallel-work-session")
	if err != nil {
		t.Fatal(err)
	}
	releaseWork, err := stack.sessionRuntimes.retainObservation(work.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(releaseWork)
	workRuntime := activateSessionRuntime(t, stack, work.SessionID)
	workResult, err := secondWindow.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "parallel-work-turn", SessionID: work.SessionID}, Input: "work while bot is running"})
	if err != nil || workResult.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("parallel work Prompt = %#v, %v", workResult, err)
	}
	waitBotRuntimeTestIdle(t, ctx, workRuntime)
	select {
	case request := <-workRequests:
		tools, _ := request["tools"].([]any)
		names := make(map[string]bool)
		for _, raw := range tools {
			entry, _ := raw.(map[string]any)
			function, _ := entry["function"].(map[string]any)
			name, _ := function["name"].(string)
			names[name] = true
		}
		for _, name := range []string{"Read", "Remember", "StartThread"} {
			if !names[name] {
				t.Fatalf("work Session lost %s while Bot was active: %v", name, names)
			}
		}
	case <-ctx.Done():
		t.Fatal("work Session could not progress while Bot was active")
	}
	if _, active := runtime.instance.currentGateway().ActiveTurn(value.SessionID); !active {
		t.Fatal("parallel work unexpectedly ended the blocked Bot Turn")
	}
	cancelled, err := secondWindow.Cancel(ctx, appserver.CancelRequest{WriteBase: appserver.WriteBase{OperationID: "cancel-bot-turn", SessionID: value.SessionID}, Target: first.Target, Reason: "test cancellation"})
	if err != nil || cancelled.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("Cancel = %#v, %v", cancelled, err)
	}
	waitBotRuntimeTestIdle(t, ctx, runtime)
	state, err := runtime.instance.engine.RunState(ctx, ref)
	if err != nil || state.Status != agent.RunLifecycleStatusInterrupted {
		t.Fatalf("cancelled RunState = %#v, %v", state, err)
	}
	value, err = stack.Bots().GetBot(ctx, principal, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	updated, err = stack.Bots().UpdateBot(ctx, principal, appserver.UpdateBotRequest{
		WriteBase: appserver.WriteBase{OperationID: "update-after-bot-cancel", SessionID: value.SessionID, ExpectedRevision: &value.Revision}, BotID: value.ID, Config: next,
	})
	if err != nil || updated.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("quiescent UpdateBot = %#v, %v", updated, err)
	}
	failed, err := firstWindow.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "failed-bot-turn", SessionID: value.SessionID}, Input: "fail this reply"})
	if err != nil || failed.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("failure Prompt admission = %#v, %v", failed, err)
	}
	waitBotRuntimeTestIdle(t, ctx, runtime)
	state, err = runtime.instance.engine.RunState(ctx, ref)
	if err != nil || state.Status != agent.RunLifecycleStatusFailed || !strings.Contains(state.LastError, "forced provider failure") {
		t.Fatalf("failed provider RunState = %#v, %v", state, err)
	}
	events, err := stack.composition.sessions.Events(ctx, session.EventsRequest{SessionRef: ref})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if session.EventTypeOf(event) == session.EventTypeAssistant {
			t.Fatal("cancelled or failed Bot reply persisted a fabricated assistant response")
		}
	}
	if _, err := firstWindow.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "host-stop-bot-turn", SessionID: value.SessionID}, Input: "block this reply"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("second blocked Bot Turn did not reach provider")
	}
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewLocalStack(Config{StoreDir: storeDir, WorkspaceCWD: workspace, Sandbox: SandboxConfig{RequestedType: "host"},
		ResolveProviderHTTPClient: func(context.Context, ModelConfig) (*http.Client, error) { return client, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	window, err := appserver.BindSessionClient(restarted.ControlClient(), principal)
	if err != nil {
		t.Fatal(err)
	}
	visible, err := window.InspectSession(ctx, appserver.StateRequest{SessionID: value.SessionID})
	if err != nil || visible.Run.Status != string(agent.RunLifecycleStatusInterrupted) {
		t.Fatalf("Host restart RunState = %#v, %v", visible.Run, err)
	}
}

// Like a terminal provider authentication transport failure, this preserves
// the provider retry contract while making failure deterministic and bounded.
type botRuntimeTerminalProviderError struct{}

func (botRuntimeTerminalProviderError) Error() string   { return "forced provider failure" }
func (botRuntimeTerminalProviderError) Retryable() bool { return false }

func waitBotRuntimeTestIdle(t *testing.T, ctx context.Context, runtime *sessionRuntime) {
	t.Helper()
	if active, ok := runtime.instance.currentGateway().ActiveTurn(runtime.sessionID); ok {
		if err := runtime.instance.currentGateway().WaitActiveTurnChange(ctx, active); err != nil {
			t.Fatal(err)
		}
	}
}

package gatewayapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
)

func TestBotManagedWorkParallelAndSourceIsolation(t *testing.T) {
	if os.Getenv("CAELIS_TEST_BOT_NATIVE") != "1" {
		t.Skip("set CAELIS_TEST_BOT_NATIVE=1 for native managed work isolation")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	entered := make(chan struct{}, 2)
	finish := make(chan struct{})
	defer close(finish)
	var mainCalls atomic.Int64
	client := &http.Client{Transport: gatewayAppRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			return nil, err
		}
		data, _ := json.Marshal(payload["messages"])
		if strings.Contains(string(data), "professional agent in an isolated managed work session") {
			entered <- struct{}{}
			select {
			case <-finish:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		} else {
			mainCalls.Add(1)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)), Request: req}, nil
	})}
	stack, err := newGatewayAppTestStack(t, Config{StoreDir: t.TempDir(), WorkspaceCWD: t.TempDir(), Model: ModelConfig{Provider: "openai-compatible", API: providers.APIOpenAICompatible, BaseURL: "https://provider.invalid/v1", Model: "gpt-4.1", HTTPClient: client, Token: "test-token"}})
	if err != nil {
		t.Fatal(err)
	}
	p := appserver.Principal{ID: stack.composition.authorities.userID}
	created, err := stack.Bots().CreateBot(ctx, p, appserver.CreateBotRequest{WriteBase: appserver.WriteBase{OperationID: "create-work-bot"}, Config: bot.Config{Name: "Assistant", ManagedWork: true}})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Resource.Ref
	_, err = stack.ControlClient().Prompt(ctx, p, appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: created.SessionID, OperationID: "real-user-request"}, Input: "Prepare two independent reports."})
	if err != nil {
		t.Fatal(err)
	}
	waitBotRuntimeTestIdle(t, ctx, activateSessionRuntime(t, stack, created.SessionID))
	source, err := stack.BotWork().GetBotRequest(ctx, p, id, "real-user-request")
	if err != nil {
		t.Fatal(err)
	}
	commands := stack.composition.authorities.botWorkCommands
	makeWork := func(op, assignment string) appserver.CommandResult {
		t.Helper()
		out, err := commands.CreateBotWork(ctx, p, appserver.BotWorkRequest{WriteBase: appserver.WriteBase{SessionID: created.SessionID, OperationID: op}, BotID: id, SourceID: source.ID, Assignment: assignment})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	a := makeWork("work-a", "Report A")
	b := makeWork("work-b", "Report B")
	if a.Resource.Ref == b.Resource.Ref || a.SessionID == b.SessionID {
		t.Fatal("work handles share identity")
	}
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("work did not run independently")
		}
	}
	_, err = stack.ControlClient().Prompt(ctx, p, appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: created.SessionID, OperationID: "main-still-available"}, Input: "Are you available?"})
	if err != nil {
		t.Fatal(err)
	}
	waitBotRuntimeTestIdle(t, ctx, activateSessionRuntime(t, stack, created.SessionID))
	if mainCalls.Load() != 2 {
		t.Fatalf("main Bot calls: %d", mainCalls.Load())
	}
	workA, err := stack.BotWork().GetBotWork(ctx, p, id, a.Resource.Ref)
	if err != nil {
		t.Fatal(err)
	}
	workB, err := stack.BotWork().GetBotWork(ctx, p, id, b.Resource.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if workA.Execution == workB.Execution {
		t.Fatal("runs share identity")
	}
	duplicate := makeWork("work-a", "Report A")
	if duplicate.Target != a.Target {
		t.Fatal("duplicate create did not reuse receipt")
	}
	if _, err := commands.CreateBotWork(ctx, p, appserver.BotWorkRequest{WriteBase: appserver.WriteBase{SessionID: created.SessionID, OperationID: "forged"}, BotID: id, SourceID: "notebook", Assignment: "Do it"}); err == nil {
		t.Fatal("forged source accepted")
	}
	if _, err := stack.ControlClient().Prompt(ctx, p, appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: a.SessionID, OperationID: "adopt"}, Input: "bypass Bot owner"}); err == nil {
		t.Fatal("ordinary prompt adopted owned work")
	}
	stale := workA.Execution
	stale.RunID = "stale"
	if _, err := commands.CancelBotWork(ctx, p, appserver.BotWorkRequest{WriteBase: appserver.WriteBase{SessionID: created.SessionID, OperationID: "stale"}, BotID: id, WorkID: workA.ID, Target: stale}); err == nil {
		t.Fatal("stale interrupt accepted")
	}
	registration, err := stack.BotWork().RegisterBotClient(ctx, p, appserver.RegisterBotClientRequest{WriteBase: appserver.WriteBase{OperationID: "desktop-enroll"}, BotID: id, Actions: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	store := stack.BotWork().Store
	activation, err := store.ActivateClient(ctx, registration.Client)
	if err != nil {
		t.Fatal(err)
	}
	clientPrincipal := appserver.Principal{ID: p.ID, BotID: id, ClientID: activation.ID}
	if _, err := stack.ControlClient().Prompt(ctx, clientPrincipal, appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: created.SessionID, OperationID: "client-source"}, Input: "Prepare my separate report."}); err != nil {
		t.Fatal(err)
	}
	waitBotRuntimeTestIdle(t, ctx, activateSessionRuntime(t, stack, created.SessionID))
	clientSource, err := stack.BotWork().GetBotRequest(ctx, clientPrincipal, id, "client-source")
	if err != nil {
		t.Fatal(err)
	}
	owned, err := commands.CreateBotWork(ctx, clientPrincipal, appserver.BotWorkRequest{WriteBase: appserver.WriteBase{SessionID: created.SessionID, OperationID: "client-work"}, BotID: id, SourceID: clientSource.ID, Assignment: "My report"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("client work did not start")
	}
	exited, err := commands.ExitBotClient(ctx, clientPrincipal, appserver.BotClientExitRequest{WriteBase: appserver.WriteBase{SessionID: created.SessionID, OperationID: "client-exit"}, BotID: id, ActivationID: activation.ActivationID, CancelOwnedWork: true})
	if err != nil || !strings.Contains(exited.Detail, owned.Resource.Ref) {
		t.Fatalf("client-owned work not interrupted: %+v %v", exited, err)
	}
	waitBotRuntimeTestIdle(t, ctx, activateSessionRuntime(t, stack, owned.SessionID))
	for _, other := range []appserver.CommandResult{a, b} {
		if !activateSessionRuntime(t, stack, other.SessionID).instance.currentGateway().SessionRunning(other.SessionID) {
			t.Fatal("client exit interrupted another owner's work")
		}
	}
	if _, err := store.ActiveClient(ctx, p.ID, id, activation.ID); err == nil {
		t.Fatal("client exit retained connection authority")
	}
	for _, work := range []bot.Work{workA, workB} {
		if _, err := commands.CancelBotWork(ctx, p, appserver.BotWorkRequest{WriteBase: appserver.WriteBase{SessionID: created.SessionID, OperationID: "stop-" + work.ID}, BotID: id, WorkID: work.ID, Target: work.Execution}); err != nil {
			t.Fatal(err)
		}
	}
}

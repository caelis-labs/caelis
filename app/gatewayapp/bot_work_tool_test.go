package gatewayapp

import (
	"context"
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

func TestBotNativeModelDelegationUsesBoundRequest(t *testing.T) {
	if os.Getenv("CAELIS_TEST_BOT_NATIVE") != "1" {
		t.Skip("set CAELIS_TEST_BOT_NATIVE=1 for native Bot delegation")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var mainCalls, workerCalls atomic.Int64
	provider := &http.Client{Transport: gatewayAppRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		reply := `{"choices":[{"message":{"role":"assistant","content":"DONE"},"finish_reason":"stop"}]}`
		if strings.Contains(string(raw), botWorkSystemPrompt) {
			workerCalls.Add(1)
			if !strings.Contains(string(raw), "Analyze the user report") {
				t.Error("delegation lost original user request")
			}
		} else if mainCalls.Add(1) <= 2 {
			// A model retry uses another call ID, but cannot create another work.
			id := "first"
			if mainCalls.Load() == 2 {
				id = "retry"
			}
			reply = `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"` + id + `","type":"function","function":{"name":"CreateWork","arguments":"{\"assignment\":\"Analyze the report in an independent workspace\"}"}}]},"finish_reason":"tool_calls"}]}`
		}
		reply = strings.NewReplacer(`"message":`, `"delta":`, `"choices":[{`, `"choices":[{"index":0,`, `"tool_calls":[{`, `"tool_calls":[{"index":0,`).Replace(reply)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + reply + "\n\ndata: [DONE]\n\n")), Request: req}, nil
	})}
	stack, err := newGatewayAppTestStack(t, Config{StoreDir: t.TempDir(), WorkspaceCWD: t.TempDir(), Model: ModelConfig{Provider: "openai-compatible", API: providers.APIOpenAICompatible, BaseURL: "https://provider.invalid/v1", Model: "gpt-4.1", HTTPClient: provider, Token: "test-token"}})
	if err != nil {
		t.Fatal(err)
	}
	owner := appserver.Principal{ID: stack.composition.authorities.userID}
	created, err := stack.Bots().CreateBot(ctx, owner, appserver.CreateBotRequest{WriteBase: appserver.WriteBase{OperationID: "create"}, Config: bot.Config{Name: "Assistant", ManagedWork: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.ControlClient().Prompt(ctx, owner, appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: created.SessionID, OperationID: "real-source"}, Input: "Analyze the user report"}); err != nil {
		t.Fatal(err)
	}
	waitBotRuntimeTestIdle(t, ctx, activateSessionRuntime(t, stack, created.SessionID))
	work, err := stack.BotWork().ListBotWork(ctx, owner, created.Resource.Ref)
	if err != nil || len(work) != 1 {
		t.Fatalf("model retry duplicated/lost work: %+v %v", work, err)
	}
	waitBotRuntimeTestIdle(t, ctx, activateSessionRuntime(t, stack, work[0].SessionID))
	source, err := stack.BotWork().GetBotRequest(ctx, owner, created.Resource.Ref, "real-source")
	if err != nil || work[0].SourceID != source.ID || work[0].PrincipalID != owner.ID || workerCalls.Load() != 1 {
		t.Fatalf("delegation binding: %+v source=%+v workers=%d err=%v", work[0], source, workerCalls.Load(), err)
	}
}

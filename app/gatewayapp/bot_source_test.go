package gatewayapp

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/control/appserver"
)

func TestBotPromptSourceFencesRetiredReceipt(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	var calls atomic.Int64
	client := &http.Client{Transport: gatewayAppRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)), Request: req}, nil
	})}
	stack, err := newGatewayAppTestStack(t, Config{StoreDir: t.TempDir(), WorkspaceCWD: t.TempDir(), Model: ModelConfig{Provider: "openai-compatible", API: providers.APIOpenAICompatible, BaseURL: "https://provider.invalid/v1", Model: "gpt-4.1", HTTPClient: client, Token: "test-token"}})
	if err != nil {
		t.Fatal(err)
	}
	owner := appserver.Principal{ID: stack.composition.authorities.userID}
	value := createTestBot(t, stack, "create", "Assistant")
	req := appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: value.SessionID, OperationID: "source"}, Input: "Prepare a report"}
	if _, err := stack.ControlClient().Prompt(ctx, owner, req); err != nil {
		t.Fatal(err)
	}
	waitBotRuntimeTestIdle(t, ctx, activateSessionRuntime(t, stack, value.SessionID))
	commands, err := appserver.NewCommandService(appserver.CommandServiceConfig{Authorizer: appserver.ProductCommandAuthorizer{Sessions: appserver.SessionAuthorizer{Sessions: stack.Sessions()}}, Operations: appserver.NewMemoryOperationStore(), Backend: stack.commandBackend})
	if err != nil {
		t.Fatal(err)
	}
	result, err := commands.Prompt(ctx, owner, req)
	if err == nil || result.Outcome != appserver.OutcomeUnknown || calls.Load() != 1 {
		t.Fatalf("retired receipt re-executed source: %+v %v calls=%d", result, err, calls.Load())
	}
	source, err := stack.BotWork().GetBotRequest(ctx, owner, value.ID, req.OperationID)
	if err != nil || source.Execution.TurnID == "" {
		t.Fatalf("source not recoverable: %+v %v", source, err)
	}
}

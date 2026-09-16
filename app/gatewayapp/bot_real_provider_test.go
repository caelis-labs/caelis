package gatewayapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
)

// TestBotRealMimoConversation copies one configured provider and its credential
// into a disposable Store. It never changes the user's Host or conversation.
func TestBotRealMimoConversation(t *testing.T) {
	if os.Getenv("CAELIS_BOT_E2E") != "1" {
		t.Skip("set CAELIS_BOT_E2E=1 and CAELIS_BOT_SOURCE_STORE for bounded real-provider chat")
	}
	source := os.Getenv("CAELIS_BOT_SOURCE_STORE")
	if source == "" {
		t.Fatal("CAELIS_BOT_SOURCE_STORE is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	store := t.TempDir()
	profile, effort := copyRealMimoConfiguration(t, ctx, source, store, defaultRealMimoProfile)
	var mu sync.Mutex
	var requests []map[string]any
	transport := gatewayAppRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Body != nil && req.Method == http.MethodPost {
			data, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			_ = req.Body.Close()
			req.Body = io.NopCloser(bytes.NewReader(data))
			var body map[string]any
			if json.Unmarshal(data, &body) == nil && body["messages"] != nil {
				mu.Lock()
				requests = append(requests, body)
				mu.Unlock()
			}
		}
		return http.DefaultTransport.RoundTrip(req)
	})
	cfg := Config{StoreDir: store, WorkspaceCWD: t.TempDir(), ModelProfileID: profile.ID, ModelProfileEffort: effort, SkillDirs: []string{}, Sandbox: SandboxConfig{RequestedType: "host"}, ResolveProviderHTTPClient: func(context.Context, ModelConfig) (*http.Client, error) {
		return &http.Client{Transport: transport}, nil
	}}
	stack, err := NewLocalStack(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stack.Close() }()
	principal := appserver.Principal{ID: "local-user"}
	result, err := stack.Bots().CreateBot(ctx, principal, appserver.CreateBotRequest{WriteBase: appserver.WriteBase{OperationID: "real-bot-create"}, Config: bot.Config{Name: "Birch", Description: "Reply concisely, in at most one short sentence. End each reply with BIRCH."}})
	if err != nil || result.Outcome != appserver.OutcomeCommitted || result.Resource == nil {
		t.Fatalf("create: %+v, %v", result, err)
	}
	id, sessionID := result.Resource.Ref, result.SessionID
	var replies []string
	prompt := func(index int, input string) {
		t.Helper()
		remote := newWorkspaceRuntimeHTTPClient(t, stack, "local-user")
		observation, err := remote.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sessionID})
		if err != nil {
			t.Fatal(err)
		}
		// Bootstrap is observation only. Drain the historical boundary before
		// waiting for this prompt's terminal, rather than accepting an old one.
		for {
			select {
			case delivery, ok := <-observation.Subscription.Deliveries():
				if !ok {
					t.Fatalf("bootstrap ended: %v", observation.Subscription.Err())
				}
				if delivery.Kind == appserver.FeedDeliverySync {
					goto synced
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	synced:
		started, err := remote.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: fmt.Sprintf("real-bot-prompt-%d", index), SessionID: sessionID}, Input: input})
		if err != nil || started.Outcome != appserver.OutcomeCommitted {
			t.Fatalf("prompt: %+v, %v", started, err)
		}
		observed := observeRealMimoTurn(ctx, "bot", sessionID, "", observation.Subscription)
		if observed.err != nil || observed.lifecycleState != "completed" || observed.finalText == "" {
			t.Fatalf("real reply: %+v", observed)
		}
		replies = append(replies, observed.finalText)
		t.Logf("reply %d (%d envelopes): %s", index, observed.envelopes, observed.finalText)
	}
	prompt(1, "Our conversation's secret word is acorn. Acknowledge it briefly.")
	prompt(2, "What was the word I just gave you?")
	if !strings.Contains(strings.ToLower(replies[1]), "acorn") || !strings.Contains(replies[0], "BIRCH") {
		t.Fatalf("real description or continuous conversation not followed: %q", replies)
	}
	value, err := stack.Bots().GetBot(ctx, principal, id)
	if err != nil {
		t.Fatal(err)
	}
	value.Config.Description = "Reply concisely, in at most one short sentence. End each reply with OAK instead of BIRCH."
	updated, err := stack.Bots().UpdateBot(ctx, principal, appserver.UpdateBotRequest{WriteBase: appserver.WriteBase{OperationID: "real-bot-update", SessionID: sessionID, ExpectedRevision: &value.Revision}, BotID: id, Config: value.Config})
	if err != nil || updated.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("settings: %+v, %v", updated, err)
	}
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}
	stack, err = NewLocalStack(cfg)
	if err != nil {
		t.Fatal(err)
	}
	prompt(3, "After reopening this conversation, what was my secret word?")
	if !strings.Contains(strings.ToLower(replies[2]), "acorn") || !strings.Contains(replies[2], "OAK") {
		t.Fatalf("real restart/settings response: %q", replies[2])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("provider calls = %d, want exactly three (no prompt replay)", len(requests))
	}
	for i, request := range requests {
		if tools, ok := request["tools"].([]any); ok && len(tools) > 0 {
			t.Fatalf("request %d exposed tools", i)
		}
		if i > 0 {
			previous := requests[i-1]["messages"].([]any)
			messages := request["messages"].([]any)
			if len(messages) <= len(previous) || !reflect.DeepEqual(previous, messages[:len(previous)]) {
				t.Fatalf("request %d rewrote provider history prefix", i)
			}
		}
	}
	if path := os.Getenv("CAELIS_BOT_E2E_OUT"); path != "" {
		data, err := json.MarshalIndent(map[string]any{"profile": profile.ID, "requests": requests, "replies": replies}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

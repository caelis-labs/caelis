package gatewayapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
)

// TestBotRealMimoConversation uses only synthetic preferences in a disposable
// Store. Tool effects, provider-prefix stability, and spontaneous note selection
// are separate observations; a fluent answer is not proof of a saved note.
func TestBotRealMimoConversation(t *testing.T) {
	if os.Getenv("CAELIS_BOT_E2E") != "1" {
		t.Skip("set CAELIS_BOT_E2E=1 and CAELIS_BOT_SOURCE_STORE for bounded real-provider chat")
	}
	source := os.Getenv("CAELIS_BOT_SOURCE_STORE")
	if source == "" {
		t.Fatal("CAELIS_BOT_SOURCE_STORE is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
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
				if len(requests) >= 28 {
					mu.Unlock()
					return nil, fmt.Errorf("Bot evaluation exceeded 28 provider requests")
				}
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
	result, err := stack.Bots().CreateBot(ctx, principal, appserver.CreateBotRequest{WriteBase: appserver.WriteBase{OperationID: "real-bot-create"}, Config: bot.Config{Name: "Birch", Description: "Reply concisely. Distinguish saved notes from information that is only in the conversation."}})
	if err != nil || result.Outcome != appserver.OutcomeCommitted || result.Resource == nil {
		t.Fatalf("create: %+v, %v", result, err)
	}
	id, sessionID := result.Resource.Ref, result.SessionID
	root, err := bot.NotebookRoot(store, id)
	if err != nil {
		t.Fatal(err)
	}
	var replies []string
	prompt := func(index int, input string) {
		t.Helper()
		remote := newWorkspaceRuntimeHTTPClient(t, stack, "local-user")
		observation, err := remote.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sessionID})
		if err != nil {
			t.Fatal(err)
		}
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
	prompt(1, "A lasting preference of mine: I prefer unsweetened jasmine tea when we plan breaks together.")
	spontaneous := false
	loaded, err := stack.composition.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: session.SessionRef{SessionID: sessionID}})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range loaded.Events {
		if event.Message != nil {
			for _, call := range event.Message.ToolCalls() {
				spontaneous = spontaneous || call.Name == "Write" || call.Name == "Patch"
			}
		}
	}
	t.Logf("natural preference prompt chose note-taking tools: %t (not a reliability assertion)", spontaneous)
	prompt(2, "Please save my stated jasmine tea preference in notes/preferences.md, with a relative link from index.md. Read any existing notes first, avoid duplicate claims, and confirm only after successful file tools.")
	before, err := os.ReadFile(filepath.Join(root, "notes", "preferences.md"))
	if err != nil || !strings.Contains(strings.ToLower(string(before)), "jasmine") {
		t.Fatalf("preference was not saved: %q, %v", before, err)
	}
	index, err := os.ReadFile(filepath.Join(root, "index.md"))
	if err != nil || !strings.Contains(string(index), "notes/preferences.md") {
		t.Fatalf("index did not link the saved note: %q, %v", index, err)
	}
	prompt(3, "Correction: I now prefer unsweetened cocoa, not jasmine tea. Read and revise notes/preferences.md to replace the obsolete preference, and keep index.md accurate. Confirm only after the save succeeds.")
	after, err := os.ReadFile(filepath.Join(root, "notes", "preferences.md"))
	if err != nil || bytes.Equal(before, after) || !strings.Contains(strings.ToLower(string(after)), "cocoa") {
		t.Fatalf("preference was not revised: %q, %v", after, err)
	}
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}
	stack, err = NewLocalStack(cfg)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = stack.composition.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: session.SessionRef{SessionID: sessionID}})
	if err != nil {
		t.Fatal(err)
	}
	lastSeq := session.LastEventSeq(loaded.Events)
	prompt(4, "After reopening, please read your notebook index, follow its relevant link, and tell me my current drink preference based on the saved note.")
	if !strings.Contains(strings.ToLower(replies[3]), "cocoa") {
		t.Fatalf("real notebook answer after restart: %q", replies[3])
	}
	loaded, err = stack.composition.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: session.SessionRef{SessionID: sessionID}})
	if err != nil {
		t.Fatal(err)
	}
	readIndex, readNote := false, false
	for _, event := range loaded.Events {
		if event.Seq <= lastSeq || event.Message == nil {
			continue
		}
		for _, call := range event.Message.ToolCalls() {
			if call.Name == "Read" {
				readIndex = readIndex || strings.Contains(call.Args, "index.md")
				readNote = readNote || strings.Contains(call.Args, "preferences.md")
			}
		}
	}
	if !readIndex || !readNote {
		t.Fatalf("restart answered without reading index and note: index=%t note=%t", readIndex, readNote)
	}
	mu.Lock()
	defer mu.Unlock()
	if path := os.Getenv("CAELIS_BOT_E2E_OUT"); path != "" {
		data, err := json.MarshalIndent(map[string]any{"profile": profile.ID, "requests": requests, "replies": replies, "spontaneous_note_tools": spontaneous, "note_before": string(before), "note_after": string(after)}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i, request := range requests {
		assertBotNotebookWireTools(t, request)
		if i > 0 {
			previous := requests[i-1]["messages"].([]any)
			messages := request["messages"].([]any)
			if len(messages) <= len(previous) || !reflect.DeepEqual(previous, messages[:len(previous)]) || !reflect.DeepEqual(requests[i-1]["tools"], request["tools"]) {
				t.Fatalf("request %d rewrote provider history or tool prefix", i)
			}
		}
	}
	t.Logf("notebook save, revision, restart read, and stable prefixes verified across %d provider requests", len(requests))
}

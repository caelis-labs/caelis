package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func TestBotLegacyNotebookExplicitEnableBoundary(t *testing.T) {
	ctx := t.Context()
	var requestsMu sync.Mutex
	var requests []map[string]any
	client := &http.Client{Transport: gatewayAppRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			return nil, err
		}
		requestsMu.Lock()
		requests = append(requests, payload)
		requestsMu.Unlock()
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"role":"assistant","content":"reply"},"finish_reason":"stop"}]}`)), Request: req}, nil
	})}
	storeDir, workspace := t.TempDir(), t.TempDir()
	stack, err := newGatewayAppTestStack(t, Config{StoreDir: storeDir, WorkspaceCWD: workspace,
		Model: ModelConfig{Provider: "openai-compatible", API: providers.APIOpenAICompatible, BaseURL: "https://provider.invalid/v1", Model: "gpt-4.1", Token: "test-token", HTTPClient: client},
	})
	if err != nil {
		t.Fatal(err)
	}
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	value := createLegacyNotebookTestBot(t, stack, "migration")
	ref := session.SessionRef{SessionID: value.SessionID}
	releaseObservation, err := stack.sessionRuntimes.retainObservation(ref)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(releaseObservation)
	instance := activateSessionRuntime(t, stack, value.SessionID).instance
	prompt := func(input string) map[string]any {
		t.Helper()
		requestsMu.Lock()
		before := len(requests)
		requestsMu.Unlock()
		stream := false
		turn, err := instance.currentGateway().BeginTurn(ctx, kernel.BeginTurnRequest{
			SessionRef: ref, Input: input, Request: agent.ModelRequestOptions{Stream: &stream},
		})
		if err != nil {
			t.Fatal(err)
		}
		waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := turn.Handle.WaitCompletion(waitCtx); err != nil {
			t.Fatal(err)
		}
		requestsMu.Lock()
		defer requestsMu.Unlock()
		if len(requests) != before+1 {
			t.Fatalf("provider calls = %d, want one new call after %d", len(requests), before)
		}
		return requests[before]
	}
	update := func(operation string, config bot.Config, enable bool) {
		t.Helper()
		current, err := stack.Bots().GetBot(ctx, principal, value.ID)
		if err != nil {
			t.Fatal(err)
		}
		result, err := stack.Bots().UpdateBot(ctx, principal, appserver.UpdateBotRequest{
			WriteBase: appserver.WriteBase{OperationID: operation, SessionID: value.SessionID, ExpectedRevision: &current.Revision},
			BotID:     value.ID, Config: config, EnableNotebook: enable,
		})
		if err != nil || result.Outcome != appserver.OutcomeCommitted {
			t.Fatalf("UpdateBot(%s) = %+v, %v", operation, result, err)
		}
	}
	first := prompt("legacy first input")
	second := prompt("legacy ordinary continuation")
	assertLegacyNotebookRequest(t, first)
	assertLegacyNotebookRequest(t, second)
	assertNotebookRequestPrefix(t, first, second)
	config := value.Config
	config.Name, config.Description = "Renamed legacy", "Keep all existing conversation facts."
	update("legacy-rename", config, false)
	third := prompt("legacy after rename")
	assertLegacyNotebookRequest(t, third)
	assertNotebookRequestPrefix(t, second, third)
	if _, err := stack.connectTestModel(ModelConfig{Provider: "openai-compatible", API: providers.APIOpenAICompatible,
		BaseURL: "https://provider.invalid/v1", Model: "gpt-4.1-mini", Token: "test-token"}); err != nil {
		t.Fatal(err)
	}
	configured, err := stack.composition.lookup.ResolveConfig("openai-compatible/gpt-4.1-mini")
	if err != nil {
		t.Fatal(err)
	}
	config.Model = configured.ID
	update("legacy-model", config, false)
	fourth := prompt("legacy after model change")
	assertLegacyNotebookRequest(t, fourth)
	assertNotebookRequestPrefix(t, third, fourth)
	if fourth["model"] != "gpt-4.1-mini" {
		t.Fatalf("legacy model change was not applied: %v", fourth["model"])
	}
	if err := stack.sessionRuntimes.releaseSession(ctx, value.SessionID); err != nil {
		t.Fatal(err)
	}
	instance = activateSessionRuntime(t, stack, value.SessionID).instance
	legacy := prompt("legacy after Runtime reactivation")
	assertLegacyNotebookRequest(t, legacy)
	assertNotebookRequestPrefix(t, fourth, legacy)
	current, err := stack.Bots().GetBot(ctx, principal, value.ID)
	if err != nil || current.NotebookEnabled {
		t.Fatalf("ordinary edits or activation enabled notebook: %+v, %v", current, err)
	}
	config = current.Config // Control resolves the selected model's default effort.
	notebookRoot, err := bot.NotebookRoot(storeDir, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(notebookRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy activation provisioned a notebook: %v", err)
	}
	before := loadNotebookMigrationSession(t, stack, ref)
	update("explicit-enable", config, true)
	after := loadNotebookMigrationSession(t, stack, ref)
	if len(after.Events) != len(before.Events)+1 || !reflect.DeepEqual(before.Events, after.Events[:len(before.Events)]) || session.EventText(after.Events[len(before.Events)]) != bot.NotebookEnableMessage() {
		t.Fatal("enable did not atomically append exactly one user admission while retaining all prior events")
	}
	if enabled, err := bot.NotebookEnabled(after.State); err != nil || !enabled {
		t.Fatalf("canonical enable event lacks admitted state: %t, %v", enabled, err)
	}
	if active := activateSessionRuntime(t, stack, value.SessionID).instance; active != instance {
		t.Fatal("enable replaced the resident Runtime")
	}
	enabled := prompt("first notebook input")
	assertBotNotebookWireTools(t, enabled)
	oldMessages, newMessages := legacy["messages"].([]any), enabled["messages"].([]any)
	if botWireMessageText(newMessages[0]) != bot.NotebookInstructions || len(newMessages) < len(oldMessages) || !reflect.DeepEqual(oldMessages[1:], newMessages[1:len(oldMessages)]) {
		t.Fatal("explicit enable changed canonical messages instead of only the instruction/tool baseline")
	}
	continued := prompt("notebook ordinary continuation")
	assertBotNotebookWireTools(t, continued)
	assertNotebookRequestPrefix(t, enabled, continued)
	if !reflect.DeepEqual(enabled["tools"], continued["tools"]) {
		t.Fatal("ordinary notebook continuation changed tool schemas")
	}
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}
	stack, err = NewLocalStack(Config{StoreDir: storeDir, WorkspaceCWD: workspace,
		Sandbox:                   SandboxConfig{RequestedType: "host"},
		ResolveProviderHTTPClient: func(context.Context, ModelConfig) (*http.Client, error) { return client, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	instance = activateSessionRuntime(t, stack, value.SessionID).instance
	restarted := prompt("notebook after Host restart")
	assertBotNotebookWireTools(t, restarted)
	assertNotebookRequestPrefix(t, continued, restarted)
	if !reflect.DeepEqual(continued["tools"], restarted["tools"]) {
		t.Fatal("Host restart changed notebook tool schemas")
	}
	current, err = stack.Bots().GetBot(ctx, principal, value.ID)
	if err != nil || !current.NotebookEnabled || current.ID != value.ID || current.SessionID != value.SessionID || current.Config != config {
		t.Fatalf("restart lost notebook identity/configuration: %+v, %v", current, err)
	}
}

func TestBotLegacyNotebookProvisionFailurePreservesAdmission(t *testing.T) {
	for _, failure := range []string{"symlink", "unwritable"} {
		t.Run(failure, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("POSIX symlink and permission fixture")
			}
			stack, err := newGatewayAppTestStack(t, Config{StoreDir: t.TempDir(), WorkspaceCWD: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			principal := appserver.Principal{ID: stack.composition.authorities.userID}
			value := createLegacyNotebookTestBot(t, stack, failure)
			ref := session.SessionRef{SessionID: value.SessionID}
			root, err := bot.NotebookRoot(stack.composition.authorities.storeDir, value.ID)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "symlink" {
				if err := os.Symlink(t.TempDir(), root); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(root, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
				probe := filepath.Join(root, "permission-probe")
				if err := os.WriteFile(probe, nil, 0o600); err == nil {
					_ = os.Remove(probe)
					t.Skip("current user can write through read-only directory permissions")
				} else if !errors.Is(err, os.ErrPermission) {
					t.Fatal(err)
				}
			}
			before := loadNotebookMigrationSession(t, stack, ref)
			request := appserver.UpdateBotRequest{WriteBase: appserver.WriteBase{OperationID: "failed-enable", SessionID: value.SessionID, ExpectedRevision: &value.Revision},
				BotID: value.ID, Config: value.Config, EnableNotebook: true}
			result, err := stack.Bots().UpdateBot(t.Context(), principal, request)
			if err == nil || result.Outcome == appserver.OutcomeCommitted {
				t.Fatalf("failed provisioning admitted notebook: %+v, %v", result, err)
			}
			after := loadNotebookMigrationSession(t, stack, ref)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed provisioning changed canonical history or guarded configuration")
			}
			if failure == "symlink" {
				if err := os.Remove(root); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			request.OperationID = "enable-after-repair"
			result, err = stack.Bots().UpdateBot(t.Context(), principal, request)
			if err != nil || result.Outcome != appserver.OutcomeCommitted {
				t.Fatalf("fresh enable after provisioning repair = %+v, %v", result, err)
			}
			current, err := stack.Bots().GetBot(t.Context(), principal, value.ID)
			if err != nil || !current.NotebookEnabled || current.ID != value.ID || current.SessionID != value.SessionID {
				t.Fatalf("repaired enable lost admission/identity: %+v, %v", current, err)
			}
		})
	}
}

func TestBotLegacyNotebookEnableRejectsStaleAndActiveTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	entered := make(chan map[string]any, 1)
	client := &http.Client{Transport: gatewayAppRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			return nil, err
		}
		entered <- payload
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}
	stack, err := newGatewayAppTestStack(t, Config{StoreDir: t.TempDir(), WorkspaceCWD: t.TempDir(),
		Model: ModelConfig{Provider: "openai-compatible", API: providers.APIOpenAICompatible, BaseURL: "https://provider.invalid/v1", Model: "gpt-4.1", Token: "test-token", HTTPClient: client},
	})
	if err != nil {
		t.Fatal(err)
	}
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	value := createLegacyNotebookTestBot(t, stack, "admission")
	ref := session.SessionRef{SessionID: value.SessionID}
	window, err := appserver.BindSessionClient(stack.ControlClient(), principal)
	if err != nil {
		t.Fatal(err)
	}
	release, err := stack.sessionRuntimes.retainObservation(ref)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	active := activateSessionRuntime(t, stack, value.SessionID)
	stale := value.Revision - 1
	before := loadNotebookMigrationSession(t, stack, ref)
	request := appserver.UpdateBotRequest{WriteBase: appserver.WriteBase{OperationID: "stale-enable", SessionID: value.SessionID, ExpectedRevision: &stale},
		BotID: value.ID, Config: value.Config, EnableNotebook: true}
	result, err := stack.Bots().UpdateBot(ctx, principal, request)
	if err == nil || result.Outcome != appserver.OutcomeConflicted {
		t.Fatalf("stale enable = %+v, %v", result, err)
	}
	if after := loadNotebookMigrationSession(t, stack, ref); !reflect.DeepEqual(before, after) {
		t.Fatal("stale enable changed canonical state")
	}
	prompt, err := window.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "active-input", SessionID: value.SessionID}, Input: "Keep this pending input exact."})
	if err != nil || prompt.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("Prompt = %+v, %v", prompt, err)
	}
	select {
	case payload := <-entered:
		assertLegacyNotebookRequest(t, payload)
	case <-ctx.Done():
		t.Fatal("provider never received the active legacy input")
	}
	value, err = stack.Bots().GetBot(ctx, principal, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	before = loadNotebookMigrationSession(t, stack, ref)
	request.OperationID, request.ExpectedRevision = "active-enable", &value.Revision
	result, err = stack.Bots().UpdateBot(ctx, principal, request)
	if err == nil || result.Outcome != appserver.OutcomeConflicted {
		t.Fatalf("active enable = %+v, %v", result, err)
	}
	if after := loadNotebookMigrationSession(t, stack, ref); !reflect.DeepEqual(before, after) {
		t.Fatal("rejected active enable changed accepted input or canonical state")
	}
	root, err := bot.NotebookRoot(stack.composition.authorities.storeDir, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected enable provisioned notebook: %v", err)
	}
	result, err = window.Cancel(ctx, appserver.CancelRequest{WriteBase: appserver.WriteBase{OperationID: "cancel-active", SessionID: value.SessionID}, Target: prompt.Target, Reason: "test complete"})
	if err != nil || result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("Cancel = %+v, %v", result, err)
	}
	waitBotRuntimeTestIdle(t, ctx, active)
}

func createLegacyNotebookTestBot(t *testing.T, stack *Stack, name string) bot.Bot {
	t.Helper()
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	id := bot.Identity(principal.ID, "legacy-"+name)
	sessionID, err := bot.ConversationID(id)
	if err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(stack.composition.authorities.storeDir, "bots", id)
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	workspace, err := canonicalWorkspaceRef(session.WorkspaceRef{Key: id, CWD: cwd}, session.WorkspaceRef{})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.composition.sessions.StartSession(t.Context(), session.StartSessionRequest{
		AppName: stack.composition.authorities.appName, UserID: principal.ID, Workspace: workspace, PreferredSessionID: sessionID,
		Controller: initialKernelControllerBinding("bot"),
		Metadata:   map[string]any{sessionvisibility.MetadataSystemManagedAgent: sessionvisibility.SystemManagedAgentBot, bot.MetadataID: id},
	})
	if err != nil {
		t.Fatal(err)
	}
	config := bot.Config{Name: name, Description: "Preserve this original description.", Model: stack.composition.lookup.DefaultID()}
	message := model.NewTextMessage(model.RoleUser, bot.ConfigurationMessage(config))
	_, err = stack.composition.sessions.(session.EventBatchStateService).AppendEventsAndUpdateState(t.Context(), session.AppendEventsAndUpdateStateRequest{
		SessionRef: active.SessionRef, ExpectedRevision: &active.Revision,
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest), TransactionID: "legacy-fixture", MutationDigest: "legacy-fixture",
		Events: []*session.Event{{ID: "legacy-settings", Type: session.EventTypeUser, Visibility: session.VisibilityCanonical,
			Actor: session.ActorRef{Kind: session.ActorKindUser, ID: principal.ID}, Message: &message}},
		UpdateState: func(_ []*session.Event, state map[string]any) (map[string]any, error) {
			next := session.CloneState(state)
			if next == nil {
				next = map[string]any{}
			}
			// This is the historical persisted shape, not today's Encode default.
			next[bot.StateKey] = map[string]any{"version": 1, "id": id, "config": config}
			return next, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	value, err := stack.Bots().GetBot(t.Context(), principal, id)
	if err != nil || value.NotebookEnabled {
		t.Fatalf("legacy fixture = %+v, %v", value, err)
	}
	return value
}

func loadNotebookMigrationSession(t *testing.T, stack *Stack, ref session.SessionRef) session.LoadedSession {
	t.Helper()
	loaded, err := stack.composition.sessions.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: ref})
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func assertLegacyNotebookRequest(t *testing.T, request map[string]any) {
	t.Helper()
	messages, _ := request["messages"].([]any)
	tools, _ := request["tools"].([]any)
	if len(messages) == 0 || botWireMessageText(messages[0]) != botSystemPrompt || len(tools) != 0 {
		t.Fatalf("legacy request changed its exact tool-free baseline: %#v", request)
	}
}

func assertNotebookRequestPrefix(t *testing.T, before, after map[string]any) {
	t.Helper()
	previous, _ := before["messages"].([]any)
	current, _ := after["messages"].([]any)
	if len(current) < len(previous) || !reflect.DeepEqual(previous, current[:len(previous)]) {
		t.Fatal("continuation rewrote the preceding provider message prefix")
	}
}

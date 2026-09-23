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

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
)

// TestBotFilesProvisionedOnceAtOwnerPrompt covers a missing private file area.
// The owner's first prompt provisions the private
// notebook through the same confined owner used at creation; later prompts
// change nothing, so a deleted index is never recreated and existing notes are
// never replaced.
func TestBotFilesProvisionedOnceAtOwnerPrompt(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
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
	storeDir := t.TempDir()
	stack, err := newGatewayAppTestStack(t, Config{StoreDir: storeDir, WorkspaceCWD: t.TempDir(),
		Model: ModelConfig{Provider: "openai-compatible", API: providers.APIOpenAICompatible, BaseURL: "https://provider.invalid/v1", Model: "gpt-4.1", Token: "test-token", HTTPClient: client},
	})
	if err != nil {
		t.Fatal(err)
	}
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	value := createUnprovisionedFilesTestBot(t, stack, "provision")
	instance := activateSessionRuntime(t, stack, value.SessionID)
	releaseObservation, err := stack.sessionRuntimes.retainObservation(session.SessionRef{SessionID: value.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(releaseObservation)
	root, err := bot.FilesRoot(storeDir, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("files fixture already has a notebook: %v", err)
	}
	prompt := func(input string) map[string]any {
		t.Helper()
		current, err := stack.Bots().GetBot(ctx, principal, value.ID)
		if err != nil {
			t.Fatal(err)
		}
		requestsMu.Lock()
		before := len(requests)
		requestsMu.Unlock()
		result, err := stack.ControlClient().Prompt(ctx, principal, appserver.PromptRequest{
			WriteBase: appserver.WriteBase{OperationID: "prompt-" + input, SessionID: value.SessionID, ExpectedRevision: &current.Revision},
			Input:     input,
		})
		if err != nil || result.Outcome != appserver.OutcomeCommitted {
			t.Fatalf("prompt %q = %+v, %v", input, result, err)
		}
		waitBotRuntimeTestIdle(t, ctx, instance)
		requestsMu.Lock()
		defer requestsMu.Unlock()
		if len(requests) != before+1 {
			t.Fatalf("provider calls after %q = %d, want one new call after %d", input, len(requests), before)
		}
		return requests[before]
	}
	first := prompt("first files input")
	index := filepath.Join(root, bot.NotebookIndex)
	if info, err := os.Lstat(index); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("first owner prompt did not provision the notebook index: %v, %v", info, err)
	}
	messages := first["messages"].([]any)
	if botWireMessageText(messages[0]) != buildBotSystemPrompt(stack.composition.authorities.appName) {
		t.Fatalf("provisioned Bot did not receive its system prompt: %#v", messages[0])
	}
	assertBotNotebookWireTools(t, first)

	// The notebook owner keeps its own files. An externally deleted index is
	// never recreated, and a later prompt must not rewrite the request prefix.
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	note := filepath.Join(root, "notes", "keep.md")
	if err := os.WriteFile(note, []byte("# Keep\nUser stated: retains tea.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	second := prompt("second files input")
	if _, err := os.Lstat(index); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a later prompt recreated the deleted index: %v", err)
	}
	if data, err := os.ReadFile(note); err != nil || string(data) != "# Keep\nUser stated: retains tea.\n" {
		t.Fatalf("existing notes = %q, %v", data, err)
	}
	previousMessages := first["messages"].([]any)
	currentMessages := second["messages"].([]any)
	if len(currentMessages) <= len(previousMessages) || !reflect.DeepEqual(previousMessages, currentMessages[:len(previousMessages)]) ||
		!reflect.DeepEqual(first["tools"], second["tools"]) {
		t.Fatal("files provisioning or a later prompt rewrote the provider request prefix")
	}
	assertBotNotebookWireTools(t, second)
}

// TestBotFilesProvisionFailureRejectsPrompt pins the failure semantics of the
// one-time provisioning: the owner gets an explicit error instead of a Turn whose
// tools cannot reach a notebook, and nothing is committed.
func TestBotFilesProvisionFailureRejectsPrompt(t *testing.T) {
	for _, failure := range []string{"symlink", "unwritable"} {
		t.Run(failure, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("POSIX symlink and permission fixture")
			}
			ctx := t.Context()
			modelCalls := 0
			client := &http.Client{Transport: gatewayAppRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				modelCalls++
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
					Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"role":"assistant","content":"reply"},"finish_reason":"stop"}]}`)), Request: req}, nil
			})}
			storeDir := t.TempDir()
			stack, err := newGatewayAppTestStack(t, Config{StoreDir: storeDir, WorkspaceCWD: t.TempDir(),
				Model: ModelConfig{Provider: "openai-compatible", API: providers.APIOpenAICompatible, BaseURL: "https://provider.invalid/v1", Model: "gpt-4.1", Token: "test-token", HTTPClient: client},
			})
			if err != nil {
				t.Fatal(err)
			}
			principal := appserver.Principal{ID: stack.composition.authorities.userID}
			value := createUnprovisionedFilesTestBot(t, stack, failure)
			ref := session.SessionRef{SessionID: value.SessionID}
			botDir := filepath.Join(storeDir, "bots", value.ID)
			if failure == "symlink" {
				if err := os.RemoveAll(botDir); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), botDir); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Chmod(botDir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(botDir, 0o700) })
				probe := filepath.Join(botDir, "permission-probe")
				if err := os.WriteFile(probe, nil, 0o600); err == nil {
					_ = os.Remove(probe)
					t.Skip("current user can write through read-only directory permissions")
				} else if !errors.Is(err, os.ErrPermission) {
					t.Fatal(err)
				}
			}
			before := loadFilesProvisionSession(t, stack, ref)
			current, err := stack.Bots().GetBot(ctx, principal, value.ID)
			if err != nil {
				t.Fatal(err)
			}
			result, err := stack.ControlClient().Prompt(ctx, principal, appserver.PromptRequest{
				WriteBase: appserver.WriteBase{OperationID: "rejected-provision", SessionID: value.SessionID, ExpectedRevision: &current.Revision},
				Input:     "must not run",
			})
			if err == nil || result.Outcome == appserver.OutcomeCommitted {
				t.Fatalf("unprovisionable prompt = %+v, %v", result, err)
			}
			if modelCalls != 0 {
				t.Fatalf("rejected prompt reached the provider %d times", modelCalls)
			}
			if after := loadFilesProvisionSession(t, stack, ref); !reflect.DeepEqual(before, after) {
				t.Fatal("rejected prompt changed canonical history or guarded configuration")
			}

			// Repairing the Store makes the next owner prompt provision and run. The
			// Bot's workspace directory is recreated after the symlink case because
			// activation resolves it, as for any other conversation.
			switch failure {
			case "symlink":
				if err := os.Remove(botDir); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(botDir, 0o700); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.Chmod(botDir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			instance := activateSessionRuntime(t, stack, value.SessionID)
			current, err = stack.Bots().GetBot(ctx, principal, value.ID)
			if err != nil {
				t.Fatal(err)
			}
			result, err = stack.ControlClient().Prompt(ctx, principal, appserver.PromptRequest{
				WriteBase: appserver.WriteBase{OperationID: "repaired-provision", SessionID: value.SessionID, ExpectedRevision: &current.Revision},
				Input:     "runs after repair",
			})
			if err != nil || result.Outcome != appserver.OutcomeCommitted {
				t.Fatalf("prompt after repair = %+v, %v", result, err)
			}
			waitBotRuntimeTestIdle(t, ctx, instance)
			if modelCalls != 1 {
				t.Fatalf("provider calls after repair = %d, want 1", modelCalls)
			}
			root, err := bot.FilesRoot(storeDir, value.ID)
			if err != nil {
				t.Fatal(err)
			}
			if info, err := os.Lstat(filepath.Join(root, bot.NotebookIndex)); err != nil || !info.Mode().IsRegular() {
				t.Fatalf("repaired prompt did not provision the notebook: %v, %v", info, err)
			}
		})
	}
}

// createUnprovisionedFilesTestBot removes a current Bot's private file area to
// exercise lazy provisioning and tamper rejection through public admission.
func createUnprovisionedFilesTestBot(t *testing.T, stack *Stack, name string) bot.Bot {
	t.Helper()
	p := appserver.Principal{ID: stack.composition.authorities.userID}
	result, err := stack.Bots().CreateBot(t.Context(), p, appserver.CreateBotRequest{WriteBase: appserver.WriteBase{OperationID: "files-" + name}, Config: bot.Config{Name: name, Description: "Preserve this original description."}})
	if err != nil {
		t.Fatal(err)
	}
	value, err := stack.Bots().GetBot(t.Context(), p, result.Resource.Ref)
	if err != nil {
		t.Fatal(err)
	}
	root, err := bot.FilesRoot(stack.composition.authorities.storeDir, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	return value
}

func loadFilesProvisionSession(t *testing.T, stack *Stack, ref session.SessionRef) session.LoadedSession {
	t.Helper()
	loaded, err := stack.composition.sessions.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: ref})
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

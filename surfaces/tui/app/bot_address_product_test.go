package tuiapp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
	"github.com/caelis-labs/caelis/internal/gatewayapptest/localclient"
)

// A persistent Host binds each workspace key to exactly one CWD and rejects a
// request that pairs a known key with another directory. The Bot TUI starts in
// the launching (coding) workspace, so attaching a Bot conversation must move
// the whole address - key and CWD - to that conversation's own workspace. These
// product tests bind the real Host and the production AppServerAdapter: a mixed
// address fails the read with "workspace key ... is already bound to ...", so
// the model and connect catalogs the Bot relies on would be unavailable.

func TestProductBotCatalogReadsUseSelectedConversationAddress(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	clients, closeHost, workspace := newAddressProductHost(t, ctx)
	defer func() {
		if err := closeHost(); err != nil {
			t.Error(err)
		}
	}()

	created := createProductBot(t, ctx, clients.Bots, "Ada")

	adapter, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{
		Surface: "cli-bot", WorkspaceKey: "workspace", WorkspaceDir: workspace,
		Sessions: clients.Sessions, Participants: clients.Participants, Status: clients.Status,
		Configuration: clients.Configuration, Agents: clients.Agents, Completion: clients.Completion, Plugins: clients.Plugins,
		RequireExistingSession: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()

	// The conversation is attached through the ordinary Session resume path the
	// Bot surface uses for /bots and /new.
	if _, err := adapter.ResumeSession(ctx, created.SessionID); err != nil {
		t.Fatalf("ResumeSession(%s): %v", created.SessionID, err)
	}

	// The model picker reads the Host model catalog while the conversation is
	// selected.
	models, err := adapter.CompleteSlashArg(ctx, "model", "", 200)
	if err != nil {
		t.Fatalf("model catalog after Bot attach: %v", err)
	}
	if !candidateMentions(models, "observation") {
		t.Fatalf("model catalog after Bot attach = %#v, want the connected provider model", models)
	}

	// The connect wizard's first step reads the same address.
	sources, err := adapter.CompleteSlashArg(ctx, "connect", "", 20)
	if err != nil {
		t.Fatalf("connect catalog after Bot attach: %v", err)
	}
	for _, want := range []string{"account", "api-key", "acp"} {
		if !candidateHasValue(sources, want) {
			t.Fatalf("connect catalog after Bot attach = %#v, want %q", sources, want)
		}
	}

	// File completion addresses the workspace without any Session binding, so it
	// exercises the same key/CWD resolution directly.
	if _, err := adapter.CompleteFile(ctx, "", 5); err != nil {
		t.Fatalf("file completion after Bot attach: %v", err)
	}

	// A failed attach must not disturb the committed address.
	if _, err := adapter.ResumeSession(ctx, "missing-bot-conversation"); err == nil {
		t.Fatal("resuming an unknown Session reported success")
	}
	if _, err := adapter.CompleteSlashArg(ctx, "model", "", 200); err != nil {
		t.Fatalf("model catalog after failed attach: %v", err)
	}
}

func TestProductOrdinaryResumeCatalogReadsUseSelectedSessionAddress(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	clients, closeHost, workspace := newAddressProductHost(t, ctx)
	defer func() {
		if err := closeHost(); err != nil {
			t.Error(err)
		}
	}()

	// An ordinary workspace Session that does not live in the launching
	// workspace proves the address follows the selected Session rather than any
	// Bot-specific route.
	otherWorkspace := t.TempDir()
	created, err := clients.Sessions.CreateSession(ctx, appserver.CreateSessionRequest{
		WriteBase:    appserver.WriteBase{OperationID: "address-product-session"},
		WorkspaceKey: "workspace-other", CWD: otherWorkspace,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	adapter, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{
		Surface: "cli-tui", WorkspaceKey: "workspace", WorkspaceDir: workspace,
		Sessions: clients.Sessions, Participants: clients.Participants, Status: clients.Status,
		Configuration: clients.Configuration, Agents: clients.Agents, Completion: clients.Completion, Plugins: clients.Plugins,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()

	if _, err := adapter.ResumeSession(ctx, created.SessionID); err != nil {
		t.Fatalf("ResumeSession(%s): %v", created.SessionID, err)
	}
	models, err := adapter.CompleteSlashArg(ctx, "model", "", 200)
	if err != nil {
		t.Fatalf("model catalog after resume: %v", err)
	}
	if !candidateMentions(models, "observation") {
		t.Fatalf("model catalog after resume = %#v, want the connected provider model", models)
	}
	if _, err := adapter.CompleteFile(ctx, "", 5); err != nil {
		t.Fatalf("file completion after resume: %v", err)
	}
}

// newAddressProductHost starts an isolated real Host with the launching
// workspace the Bot TUI is given, and returns that workspace path.
func newAddressProductHost(t *testing.T, ctx context.Context) (appserver.AppServerClients, func() error, string) {
	t.Helper()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	t.Cleanup(provider.Close)

	workspace := t.TempDir()
	clients, closeHost, err := localclient.New(ctx, t.TempDir(), workspace, provider.URL, provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	return clients, closeHost, workspace
}

func candidateHasValue(candidates []controlprompt.SlashArgCandidate, value string) bool {
	for _, candidate := range candidates {
		if strings.EqualFold(strings.TrimSpace(candidate.Value), value) {
			return true
		}
	}
	return false
}

func candidateMentions(candidates []controlprompt.SlashArgCandidate, fragment string) bool {
	for _, candidate := range candidates {
		if strings.Contains(strings.ToLower(candidate.Value), fragment) ||
			strings.Contains(strings.ToLower(candidate.Display), fragment) {
			return true
		}
	}
	return false
}

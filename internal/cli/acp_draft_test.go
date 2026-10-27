package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/internal/acpdraft"
	"github.com/caelis-labs/caelis/internal/testenv"
	surfaceacp "github.com/caelis-labs/caelis/surfaces/acp"
)

func TestRunACPDraftUsesProductHostAndCanonicalCommandConversation(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	storeDir := cliTestStoreDir(t)
	seedCLIModel(t, storeDir, gatewayapp.ModelConfig{Provider: "ollama", Model: "llama3"})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()
	var stderr bytes.Buffer
	runDone := make(chan error, 1)
	go func() {
		runDone <- runWithRoundTripEmbeddedControl(t, ctx,
			[]string{"acp", "--embedded", "--store-dir=" + storeDir, "--acp-draft=" + surfaceacp.DraftSchemaTag}, serverSide, serverSide, &stderr)
	}()
	peer := &cliDraftPeer{changed: make(chan struct{}, 1)}
	client, err := v2.NewClientSideConnection(peer, clientSide, clientSide)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	response, err := client.Initialize(ctx, v2.InitializeRequest{
		ProtocolVersion: 2, Info: v2.Implementation{Name: "fixture", Version: "test"}, Meta: acpdraft.InitializeMeta(),
	})
	if err != nil || response.ProtocolVersion != 2 || response.Capabilities.Session == nil {
		t.Fatalf("draft CLI initialize = %#v, %v", response, err)
	}
	if err := acpdraft.ValidatePeer(response); err != nil {
		t.Fatal(err)
	}
	cwd := v2.AbsolutePath(t.TempDir())
	created, err := client.NewSession(ctx, v2.NewSessionRequest{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	var modelValue v2.SessionConfigValueId
	for _, option := range created.ConfigOptions {
		if option.Select != nil && option.Select.ConfigId == "model" {
			modelValue = option.Select.CurrentValue
		}
	}
	if modelValue == "" {
		t.Fatal("draft product omitted native model configuration")
	}
	configured, err := client.SetSessionConfigOption(ctx, v2.SetSessionConfigOptionRequest{Id: &v2.SetSessionConfigOptionId{Type: "id", SessionId: created.SessionId, ConfigId: "model", Value: modelValue}})
	if err != nil || len(configured.ConfigOptions) == 0 {
		t.Fatalf("draft native configuration = %#v, %v", configured, err)
	}
	listed, err := client.ListSessions(ctx, v2.ListSessionsRequest{Cwd: &cwd})
	if err != nil || len(listed.Sessions) != 1 || listed.Sessions[0].SessionId != created.SessionId {
		t.Fatalf("draft native Session list = %#v, %v", listed, err)
	}
	store := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(storeDir, "sessions")})
	ref := session.SessionRef{SessionID: string(created.SessionId)}
	before, err := store.Events(ctx, session.EventsRequest{SessionRef: ref, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := client.Prompt(ctx, v2.PromptRequest{SessionId: created.SessionId, Meta: acpdraft.IdleOnlyMeta(), Prompt: []v2.ContentBlock{{Text: &v2.ContentBlockText{Type: "text", Text: "/status"}}}})
	if err != nil || receipt.MessageId == "" {
		t.Fatalf("draft command receipt = %#v, %v", receipt, err)
	}
	if acpdraft.ActivityID(receipt.Meta) != string(receipt.MessageId) {
		t.Fatalf("advertised native command created a model Turn: %#v", receipt)
	}
	for {
		peer.mu.Lock()
		settled := false
		for _, update := range peer.updates {
			var state struct {
				State      string                     `json:"state"`
				StopReason string                     `json:"stopReason"`
				Meta       map[string]json.RawMessage `json:"_meta"`
			}
			raw, _ := json.Marshal(update.Update)
			_ = json.Unmarshal(raw, &state)
			if state.State == "idle" && state.StopReason == "end_turn" && acpdraft.ActivityID(state.Meta) == string(receipt.MessageId) {
				settled = true
			}
		}
		peer.mu.Unlock()
		if settled {
			break
		}
		select {
		case <-peer.changed:
		case <-ctx.Done():
			t.Fatal("advertised command did not reach true idle(end_turn)")
		}
	}
	after, err := store.Events(ctx, session.EventsRequest{SessionRef: ref, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	var interaction *session.Event
	for _, event := range after {
		if event.ID == string(receipt.MessageId) {
			interaction = event
		}
	}
	if interaction == nil || interaction.Type != session.EventTypeInteraction || !session.IsMirror(interaction) || !session.IsClientReplayEvent(interaction) {
		t.Fatalf("command receipt is not the original durable mirror interaction: %#v", interaction)
	}
	if !reflect.DeepEqual(compact.PromptEventsFromLatestCompact(before), compact.PromptEventsFromLatestCompact(after)) {
		t.Fatal("display-only command changed actual persisted model context")
	}
	peer.mu.Lock()
	found := false
	for _, update := range peer.updates {
		if update.Update.UserMessage != nil && update.Update.UserMessage.MessageId == receipt.MessageId {
			found = true
		}
	}
	peer.mu.Unlock()
	if !found {
		t.Fatal("canonical command receipt lacks same-ID live user echo")
	}
	_, err = client.ResumeSession(ctx, v2.ResumeSessionRequest{SessionId: created.SessionId, Cwd: cwd, ReplayFrom: &v2.ReplayFrom{Start: &v2.ReplayFromStartInline{Type: "start"}}})
	if err != nil {
		t.Fatalf("same Session resume = %v", err)
	}
	peer.mu.Lock()
	replayed := 0
	for _, update := range peer.updates {
		if update.Update.UserMessage != nil && update.Update.UserMessage.MessageId == receipt.MessageId {
			replayed++
		}
	}
	peer.mu.Unlock()
	if replayed != 2 {
		t.Fatalf("live plus replay identity count = %d", replayed)
	}
	_, err = client.ResumeSession(ctx, v2.ResumeSessionRequest{SessionId: created.SessionId, Cwd: v2.AbsolutePath(t.TempDir()), ReplayFrom: &v2.ReplayFrom{Start: &v2.ReplayFromStartInline{Type: "start"}}})
	if err == nil {
		t.Fatal("resume accepted a different workspace")
	}
	if receipt.MessageId == "" {
		t.Fatal("missing durable message identity")
	}
	if _, err := client.CloseSession(ctx, v2.CloseSessionRequest{SessionId: created.SessionId}); err != nil {
		t.Fatalf("draft native Session close = %v", err)
	}
	_ = client.Close()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("draft CLI shutdown = %v; stderr=%q", err, stderr.String())
		}
	case <-ctx.Done():
		t.Fatal("draft CLI did not terminate on disconnect")
	}
}

type cliDraftPeer struct {
	mu      sync.Mutex
	updates []v2.UpdateSessionNotification
	changed chan struct{}
}

func (p *cliDraftPeer) SessionUpdate(_ context.Context, update v2.UpdateSessionNotification) error {
	p.mu.Lock()
	p.updates = append(p.updates, update)
	p.mu.Unlock()
	select {
	case p.changed <- struct{}{}:
	default:
	}
	return nil
}
func (*cliDraftPeer) RequestPermission(context.Context, v2.RequestPermissionRequest) (v2.RequestPermissionResponse, error) {
	return v2.RequestPermissionResponse{Outcome: v2.RequestPermissionOutcome{Cancelled: &v2.RequestPermissionOutcomeCancelled{Outcome: "cancelled"}}}, nil
}

func TestRunRejectsIncompatibleDraftInvocationBeforeOpeningHost(t *testing.T) {
	for _, args := range [][]string{
		{"--acp-draft=" + surfaceacp.DraftSchemaTag},
		{"acp", "--acp-draft=schema-v2.0.0-alpha.7"},
		{"acp", "--acp-draft=schema-v2.0.0-alpha.8"},
		{"acp", "--acp-draft=" + surfaceacp.DraftSchemaTag, "--adapter-grant-file=grant"},
		{"acp", "--acp-draft=" + surfaceacp.DraftSchemaTag, "-p=work"},
		{"acp", "--acp-draft=" + surfaceacp.DraftSchemaTag, "work"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			err := runWithProductClientOpener(t.Context(), args, strings.NewReader(""), io.Discard, io.Discard,
				func(context.Context, gatewayapp.Config, productClientOptions) (*productClients, error) {
					t.Fatal("invalid draft request opened product Host")
					return nil, nil
				})
			if err == nil {
				t.Fatal("invalid draft invocation succeeded")
			}
		})
	}
}

func TestACPDraftHelpAdvertisesAcceptedSchema(t *testing.T) {
	var output bytes.Buffer
	if err := Run(t.Context(), []string{"acp", "--help"}, strings.NewReader(""), io.Discard, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "exact "+surfaceacp.DraftSchemaTag) {
		t.Fatalf("help omitted current schema %s: %s", surfaceacp.DraftSchemaTag, output.String())
	}
}

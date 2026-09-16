package gatewayapp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/control/streamspool"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

func TestHostedChildUserPromptsRetainUserRoleAcrossContextReplay(t *testing.T) {
	provider := newHostedChildInputTestProvider(t, false)
	host := newHostedChildInputTestStack(t, provider)
	parent, child, _ := newHostedChildInputTestTopology(t, host, "user-context")
	for _, text := range []string{"user guidance one", "user followup two"} {
		current, err := host.composition.sessions.Session(t.Context(), child.SessionRef)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runHeadlessOnceForGatewayAppTest(context.Background(), host, current, child.SessionID, text, headless.Options{}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := host.composition.sessions.Events(t.Context(), session.EventsRequest{SessionRef: child.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"user guidance one", "user followup two"} {
		found := false
		for _, event := range events {
			if session.EventText(event) == text {
				found = true
				if event.Type != session.EventTypeUser || session.ProtocolAgentCommunicationOf(event) != nil {
					t.Fatalf("canonical human input=%#v", event)
				}
			}
		}
		if !found {
			t.Fatalf("missing durable user input %q", text)
		}
	}
	payload := string(provider.LastMessages())
	for _, text := range []string{"user guidance one", "user followup two"} {
		if !strings.Contains(payload, text) {
			t.Fatalf("rebuilt model context omitted %q: %s", text, payload)
		}
	}
	events, err = host.composition.sessions.Events(t.Context(), session.EventsRequest{SessionRef: parent.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if strings.Contains(session.EventText(event), "user guidance one") || strings.Contains(session.EventText(event), "user followup two") {
			t.Fatalf("child prompt leaked into parent context: %#v", event)
		}
	}
}

// RunHostedSubagentUserInputTest supplies the production AppServer assembly from
// the external test package, avoiding an import cycle through the local adapter.
func RunHostedSubagentUserInputTest(t *testing.T, assemble func(*Stack) (appserver.AppServerServices, error)) {
	for _, name := range []string{"live", "restored-spool", "retained-spool"} {
		t.Run(name, func(t *testing.T) { runHostedSubagentUserInput(t, assemble, name) })
	}
}

func runHostedSubagentUserInput(t *testing.T, assemble func(*Stack) (appserver.AppServerServices, error), mode string) {
	restored := mode != "live"
	provider := newHostedChildInputTestProvider(t, false)
	finishProvider := make(chan struct{})
	if restored {
		provider.afterChunk = func(ctx context.Context, call int) {
			if call == 2 {
				select {
				case <-finishProvider:
				case <-ctx.Done():
				}
			}
		}
	}
	host := newHostedChildInputTestStack(t, provider)
	services, err := assemble(host)
	if err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "child.token")
	token, err := controlserver.LoadOrCreateBearerToken(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := controlserver.BearerTokenAuthenticator(token, appserver.Principal{ID: "owner", Roles: []string{appserver.RoleACPIngress}})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := controlserver.New(controlserver.HandlerConfig{Services: services, AdapterHost: host.AdapterHost(), Authenticator: auth, AllowedHosts: []string{"127.0.0.1"}, ServerInfo: appserver.ServerInfo{Capabilities: appserver.RequiredManagedHostCapabilities()}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(func() { _ = host.Close(); server.Close() })
	host.SetBuiltInChildControl(server.URL, tokenFile)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	parent, err := startGatewayAppTestSession(ctx, host, "parent-user-input")
	if err != nil {
		t.Fatal(err)
	}
	active := activateSessionRuntime(t, host, parent.SessionID)
	_, err = active.instance.engine.StartSubagentWithOptions(ctx, parent.SessionRef, "self", "initial assignment", "test", runtime.StartSubagentOptions{SpawnID: "human-input-child"})
	if err != nil {
		t.Fatal(err)
	}
	parent, err = host.composition.sessions.Session(ctx, parent.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	var child session.ParticipantBinding
	for _, binding := range parent.Participants {
		if binding.Kind == session.ParticipantKindSubagent {
			child = binding
			break
		}
	}
	if child.ID == "" {
		t.Fatal("Spawn did not attach child")
	}
	// The first provider request proves ACP setup completed before inspecting
	// the effective mode; attachment alone may precede configuration.
	select {
	case <-provider.firstRequest:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	childState, err := host.composition.sessions.SnapshotState(ctx, session.SessionRef{SessionID: child.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if got := approval.CurrentMode(childState); got != approval.ModeAutoReview {
		t.Fatalf("real self ACP child mode=%v, want its own auto-review", got)
	}
	var stream <-chan taskstream.Delivery
	observed := map[string]int{}
	initialComplete, replacements := false, 0
	consume := func(delivery taskstream.Delivery) {
		if delivery.Kind == taskstream.DeliveryReplaceBegin {
			if initialComplete {
				replacements++
				// Replay recovery and Turn-window compaction each publish atomically.
				if replacements > 3 {
					t.Fatal("unexpected repeated history replacement")
				}
			}
			observed = map[string]int{}
		}
		for _, env := range delivery.Events {
			if env.Update == nil || env.Update.SessionUpdateType() != eventstream.UpdateAgentMessage {
				continue
			}
			raw, _ := json.Marshal(env.Update)
			var update struct {
				Content struct {
					Text string `json:"text"`
				} `json:"content"`
			}
			if err := json.Unmarshal(raw, &update); err != nil {
				t.Fatal(err)
			}
			// A canonical-only replay materializes its final once. When the
			// message already arrived as a delta, Final seals that same value.
			if env.Final {
				if observed[update.Content.Text] > 1 {
					t.Fatalf("terminal seal followed duplicate deltas: %q", update.Content.Text)
				}
				observed[update.Content.Text] = 1
				continue
			}
			observed[update.Content.Text]++
		}
	}
	if restored {
		if _, err := active.instance.engine.WaitSubagentTask(ctx, parent.SessionRef, child.DelegationID, 5*time.Second); err != nil {
			t.Fatal(err)
		}
		if err := host.sessionRuntimes.releaseSession(ctx, parent.SessionID); err != nil {
			t.Fatal(err)
		}
		if mode == "restored-spool" {
			if err := host.composition.authorities.taskOutputLifecycle.ReleaseTask(ctx, task.Ref{SessionID: parent.SessionID, TaskID: child.DelegationID}); err != nil {
				t.Fatal(err)
			}
			logical := streamspool.LogicalKey{Namespace: streamspool.NamespaceTask, Digest: streamspool.DigestStrings(parent.SessionID, child.DelegationID)}
			key, _, err := host.composition.authorities.streamSpool.Resolve(ctx, logical)
			if err != nil {
				t.Fatal(err)
			}
			if err := host.composition.authorities.streamSpool.Remove(ctx, key); err != nil {
				t.Fatal(err)
			}
		}
		result, err := services.Tasks.Subscribe(ctx, taskstream.Principal{ID: "owner"}, taskstream.SubscribeRequest{SessionID: parent.SessionID, TaskID: child.DelegationID, Follow: true})
		if err != nil {
			t.Fatal(err)
		}
		defer result.Subscription.Close()
		buffer := make(chan taskstream.Delivery, 256)
		go func() {
			defer close(buffer)
			for delivery := range result.Subscription.Deliveries() {
				select {
				case buffer <- delivery:
				case <-ctx.Done():
					return
				}
			}
		}()
		stream = buffer
		for {
			select {
			case delivery, ok := <-stream:
				if !ok {
					t.Fatalf("recovery stream ended: %v", result.Subscription.Err())
				}
				consume(delivery)
				if delivery.Kind == taskstream.DeliveryAppendPage && delivery.NextCursor != "" && observed["reply-1"] == 1 {
					goto recovered
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
recovered:
	initialComplete = true
	const imageData = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a5l8AAAAASUVORK5CYII="
	for index, text := range []string{"user guidance one", "user followup two"} {
		req := appserver.SubagentInputRequest{OperationID: text, SessionID: parent.SessionID, ParticipantID: child.ID, TaskID: child.DelegationID, Input: text}
		if index == 0 {
			req.ContentParts = []model.ContentPart{{Type: model.ContentPartText, Text: text}, {Type: model.ContentPartImage, MimeType: "image/png", Data: imageData}}
		}
		receipt, sendErr := services.SubagentInputs.Submit(ctx, appserver.Principal{ID: "owner"}, req)
		err = sendErr
		if err != nil {
			t.Fatal(err)
		}
		if restored && index == 0 {
			// The provider is parked before its stop chunk. Observe through the
			// real ACP process, recorder, and public subscription before release.
			for observed["reply-2"] == 0 {
				select {
				case delivery, ok := <-stream:
					if !ok {
						t.Fatal("recovered stream ended before live output")
					}
					consume(delivery)
				case <-ctx.Done():
					t.Fatal("child output did not arrive before provider completion")
				}
			}
			close(finishProvider)
		}
		ticker := time.NewTicker(10 * time.Millisecond)
		found := false
		for !found {
			select {
			case <-ctx.Done():
				ticker.Stop()
				t.Fatal(ctx.Err())
			case <-ticker.C:
				statuses, statusErr := services.SubagentInputs.Statuses(ctx, appserver.Principal{ID: "owner"}, appserver.SubagentInputStatusRequest{SessionID: parent.SessionID, IDs: []string{receipt.ID}})
				if statusErr != nil {
					t.Fatal(statusErr)
				}
				if len(statuses) != 1 || statuses[0].State == "failed" || statuses[0].State == "unknown" {
					t.Fatalf("input receipt=%#v", statuses)
				}
				events, readErr := host.composition.sessions.Events(ctx, session.EventsRequest{SessionRef: session.SessionRef{SessionID: child.SessionID}})
				if readErr != nil {
					ticker.Stop()
					t.Fatal(readErr)
				}
				for _, event := range events {
					if strings.Contains(session.EventText(event), text) {
						if event.Type != session.EventTypeUser || session.ProtocolAgentCommunicationOf(event) != nil {
							t.Fatalf("canonical human input=%#v", event)
						}
						found = true
					}
				}
			}
		}
		ticker.Stop()
		waitHostedChildParentIdle(t, host, child.SessionID)
	}
	if restored {
		for observed["reply-3"] == 0 {
			select {
			case delivery, ok := <-stream:
				if !ok {
					t.Fatal("following subscription stopped between child Turns")
				}
				consume(delivery)
			case <-ctx.Done():
				t.Fatalf("live output missing: %v", observed)
			}
		}
		if mode == "retained-spool" && replacements < 1 {
			t.Fatal("reconnected producer did not refresh its retained cache")
		}
		for _, text := range []string{"reply-1", "reply-2", "reply-3"} {
			if observed[text] != 1 {
				t.Fatalf("child timeline %q count=%d, want once", text, observed[text])
			}
		}
	}
	payload := string(provider.LastMessages())
	if !strings.Contains(payload, imageData) {
		t.Fatal("next Turn model context lost the user's image")
	}
	for _, text := range []string{"user guidance one", "user followup two"} {
		if !strings.Contains(payload, text) {
			t.Fatalf("rebuilt context omitted %q: %s", text, payload)
		}
	}
	events, err := host.composition.sessions.Events(ctx, session.EventsRequest{SessionRef: parent.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if strings.Contains(session.EventText(event), "user guidance one") || strings.Contains(session.EventText(event), "user followup two") {
			t.Fatalf("child prompt entered parent context: %#v", event)
		}
	}
}

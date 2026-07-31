//go:build e2e

package eval

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controlassembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	tuiapp "github.com/caelis-labs/caelis/surfaces/tui/app"
	"github.com/charmbracelet/x/ansi"
)

func TestSideACPAgentDirectRunEnvelopeE2E(t *testing.T) {
	repo := repoRootForGatewayAppTest(t)
	root := t.TempDir()
	workdir := t.TempDir()
	childRoot := filepath.Join(root, "side-agent-sessions")
	launcher := writeAgentHandoffLauncher(t, repo, childRoot)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	stackConfig := gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "side-agent-user",
		StoreDir:     root,
		WorkspaceKey: workdir,
		WorkspaceCWD: workdir,
		ApprovalMode: "auto-review",
		Model:        gatewayapp.ModelConfig{Provider: "minimax", Model: "MiniMax-M2"},
		Assembly:     controlassembly.ResolvedAssembly{},
	}
	stack, err := gatewayapp.NewLocalStack(stackConfig)
	if err != nil {
		t.Fatalf("gatewayapp.NewLocalStack() error = %v", err)
	}
	connectReq := controlagents.ConnectRequest{
		AdapterID:   "custom",
		Launcher:    controlagents.LauncherChoiceCommand,
		CommandLine: launcher,
		CWD:         workdir,
	}
	discovery, err := stack.DiscoverACPConnection(ctx, connectReq)
	if err != nil {
		t.Fatalf("DiscoverACPConnection() error = %v", err)
	}
	connectReq.ModelID = "opus"
	connectReq.ConfigValues = map[string]string{"effort": "max"}
	connectReq.Discovery = &discovery
	connected, err := stack.ConnectACP(ctx, connectReq)
	if err != nil {
		t.Fatalf("ConnectACP() error = %v", err)
	}
	if len(connected.Profiles) != 1 {
		t.Fatalf("ConnectACP() profiles = %#v, want one", connected.Profiles)
	}
	if _, err := stack.AgentBindings().BindAgentBinding(ctx, agentbinding.Binding{
		Handle:    agentbinding.HandleZenith,
		ProfileID: connected.Profiles[0].ID,
		Effort:    "max",
	}); err != nil {
		t.Fatalf("BindAgentBinding(zenith) error = %v", err)
	}

	active := startEvalSession(t, ctx, stack, "side-acp-direct-run")
	driver := newEvalAppServerAdapter(t, stack, active, "side-acp-e2e")
	t.Cleanup(func() {
		_ = stack.Close()
	})
	router := controlprompt.New(controlprompt.RouterConfig{Service: driver})
	result, err := router.Route(ctx, controlprompt.Request{
		Submission: controlprompt.Submission{Text: "/zenith introduce yourself"},
	})
	if err != nil {
		t.Fatalf("Route(/zenith) error = %v", err)
	}
	if result.Turn == nil {
		t.Fatal("Route(/zenith) returned nil Turn")
	}
	defer result.Turn.Close()
	turnID := strings.TrimSpace(result.Turn.TurnID())
	if turnID == "" {
		t.Fatal("Route(/zenith) returned an empty TurnID")
	}
	model := tuiapp.NewModel(tuiapp.Config{NoColor: true, NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 80})
	model = updated.(*tuiapp.Model)
	var envelopes []eventstream.Envelope
	for envelope := range result.Turn.Events() {
		envelopes = append(envelopes, eventstream.CloneEnvelope(envelope))
		updated, _ = model.Update(envelope)
		model = updated.(*tuiapp.Model)
		if envelope.Kind != eventstream.KindSessionUpdate {
			continue
		}
		updateType, text := sideACPNarrative(envelope.Update)
		if updateType != "" {
			t.Logf(
				"narrative type=%s text=%q event=%q projection=%q final=%v scope=%q scope_id=%q turn=%q actor=%q meta=%#v",
				updateType,
				text,
				envelope.EventID,
				envelope.ProjectionID,
				envelope.Final,
				envelope.Scope,
				envelope.ScopeID,
				envelope.TurnID,
				envelope.Actor,
				envelope.Meta,
			)
		}
	}

	var userCount, assistantCount, finalAssistantCount int
	participantID := ""
	displayAddress := ""
	for _, envelope := range envelopes {
		updateType, text := sideACPNarrative(envelope.Update)
		switch updateType {
		case schema.UpdateUserMessage:
			if strings.TrimSpace(text) == "introduce yourself" {
				userCount++
				if got := strings.TrimSpace(envelope.TurnID); got != turnID {
					t.Fatalf("user TurnID = %q, want Control TurnID %q", got, turnID)
				}
				if got := strings.TrimSpace(envelope.ScopeID); got != turnID {
					t.Fatalf("user ScopeID = %q, want participant TurnID %q", got, turnID)
				}
				handle := strings.TrimSpace(asStringValue(envelope.Meta["handle"]))
				wantAddress := "/" + controlagents.FormatRunName("zenith", handle)
				if got := strings.TrimSpace(asStringValue(envelope.Meta["display_address"])); got != wantAddress {
					t.Fatalf("user display_address = %q, want %q", got, wantAddress)
				}
				displayAddress = wantAddress
			}
		case schema.UpdateAgentMessage:
			if strings.Contains(text, "opus owns this turn") {
				assistantCount++
				if got := strings.TrimSpace(envelope.TurnID); got != turnID {
					t.Fatalf("assistant TurnID = %q, want Control TurnID %q", got, turnID)
				}
				if got := strings.TrimSpace(envelope.ScopeID); got != turnID {
					t.Fatalf("assistant ScopeID = %q, want participant TurnID %q", got, turnID)
				}
				if got := strings.TrimSpace(envelope.ParticipantID); got == "" {
					t.Fatal("assistant ParticipantID is empty")
				} else if participantID == "" {
					participantID = got
				} else if got != participantID {
					t.Fatalf("assistant ParticipantID = %q, want %q", got, participantID)
				}
				if envelope.Final {
					finalAssistantCount++
				}
			}
		}
	}
	if userCount != 1 {
		t.Fatalf("user narrative envelopes = %d, want one: %#v", userCount, envelopes)
	}
	if assistantCount != 2 || finalAssistantCount != 1 {
		t.Fatalf("assistant narrative envelopes = %d final = %d, want one live and one final materialization: %#v", assistantCount, finalAssistantCount, envelopes)
	}
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 80})
	model = updated.(*tuiapp.Model)
	frame := ansi.Strip(model.View().Content)
	if got := strings.Count(frame, "introduce yourself"); got != 1 {
		t.Fatalf("rendered user prompt count = %d, want one\n%s", got, frame)
	}
	if displayAddress == "" || !strings.Contains(frame, displayAddress+" introduce yourself") {
		t.Fatalf("rendered transcript lost qualified Side Agent address %q\n%s", displayAddress, frame)
	}
	if got := strings.Count(frame, "opus owns this turn"); got != 1 {
		t.Fatalf("rendered assistant answer count = %d, want one\n%s", got, frame)
	}

	loaded, err := stack.Sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	var durableUserCount, durableAssistantCount int
	for _, event := range loaded.Events {
		if event == nil || event.Scope == nil || event.Scope.Participant.ID == "" {
			continue
		}
		switch session.EventTypeOf(event) {
		case session.EventTypeUser:
			if strings.TrimSpace(session.EventText(event)) == "introduce yourself" {
				durableUserCount++
				if got := strings.TrimSpace(event.Scope.TurnID); got != turnID {
					t.Fatalf("durable user TurnID = %q, want %q", got, turnID)
				}
			}
		case session.EventTypeAssistant:
			if strings.Contains(session.EventText(event), "opus owns this turn") {
				durableAssistantCount++
				if got := strings.TrimSpace(event.Scope.TurnID); got != turnID {
					t.Fatalf("durable assistant TurnID = %q, want %q", got, turnID)
				}
			}
		}
	}
	if durableUserCount != 1 || durableAssistantCount != 1 {
		t.Fatalf("durable participant narratives user=%d assistant=%d, want one each", durableUserCount, durableAssistantCount)
	}
}

func asStringValue(value any) string {
	text, _ := value.(string)
	return text
}

func sideACPNarrative(update schema.Update) (string, string) {
	chunk, ok := update.(schema.ContentChunk)
	if !ok {
		return "", ""
	}
	return strings.TrimSpace(chunk.SessionUpdate), schema.ExtractTextValue(chunk.Content)
}

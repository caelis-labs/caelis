package controlprompt

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func TestRouterStatusModelAndCompactCommands(t *testing.T) {
	svc := &fakeService{status: control.StatusSnapshot{
		Session: control.StatusSession{ID: "session-1", Workspace: "/tmp/work", ModeLabel: "auto-review"},
		ModelStatus: control.StatusModel{
			Display: "ollama/llama3",
		},
		SandboxStatus: control.StatusSandbox{ResolvedBackend: "seatbelt"},
		Usage: control.StatusUsage{
			TotalTokens:         5100,
			ContextWindowTokens: 1000000,
		},
	}}
	router := New(RouterConfig{Service: svc})
	status, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/status"}})
	if err != nil {
		t.Fatalf("Route(/status) error = %v", err)
	}
	if !status.Handled {
		t.Fatalf("Route(/status) = %#v", status)
	}
	if status.SlashResult == nil || status.SlashResult.Kind != control.SlashCommandResultStatus || status.SlashResult.Status.ModelStatus.Display != "ollama/llama3" {
		t.Fatalf("Route(/status).SlashResult = %#v, want structured status payload", status.SlashResult)
	}
	if status.StatusUpdate == nil || status.StatusUpdate.Usage.TotalTokens != 5100 {
		t.Fatalf("Route(/status).StatusUpdate = %#v, want the displayed snapshot applied to surface status", status.StatusUpdate)
	}
	if len(status.Events) != 0 {
		t.Fatalf("Route(/status).Events = %#v, want no eager fallback events", status.Events)
	}
	if text := control.FormatSlashResult(*status.SlashResult); !strings.Contains(text, "ollama/llama3") {
		t.Fatalf("FormatSlashResult(/status) = %q, want model text", text)
	}
	model, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/model use fast high"}})
	if err != nil {
		t.Fatalf("Route(/model use) error = %v", err)
	}
	if svc.usedModel != "fast" || svc.usedReasoning != "high" || model.StatusUpdate == nil {
		t.Fatalf("model route used model=%q reasoning=%q status=%#v", svc.usedModel, svc.usedReasoning, model.StatusUpdate)
	}
	deleted, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/model del fast"}})
	if err != nil {
		t.Fatalf("Route(/model del) error = %v", err)
	}
	if !deleted.RefreshCommands {
		t.Fatalf("Route(/model del).RefreshCommands = false, want refreshed Agent slash commands")
	}
	compactResult, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/compact"}})
	if err != nil {
		t.Fatalf("Route(/compact) error = %v", err)
	}
	if !svc.compacted || firstNotice(compactResult) != compact.CompactNoticeLabel {
		t.Fatalf("compact route compacted=%v notice=%q", svc.compacted, firstNotice(compactResult))
	}
	if compactResult.StatusUpdate == nil || compactResult.StatusUpdate.Usage.TotalTokens != 5100 || compactResult.StatusUpdate.Usage.ContextWindowTokens != 1000000 {
		t.Fatalf("Route(/compact).StatusUpdate = %#v, want post-compact context snapshot", compactResult.StatusUpdate)
	}
}

func TestRouterResumeReturnsLiveReconnectWithoutSuccessNotice(t *testing.T) {
	t.Parallel()

	svc := &fakeService{}
	result, err := New(RouterConfig{Service: svc}).Route(context.Background(), Request{
		Submission: control.Submission{Text: "/resume resumed-session"},
	})
	if err != nil {
		t.Fatalf("Route(/resume) error = %v", err)
	}
	if !result.Handled || !result.ClearHistory || !result.RefreshStatus || !result.RefreshCommands {
		t.Fatalf("Route(/resume) = %#v, want replay with deferred status refresh", result)
	}
	if result.ActiveSessionID != "resumed-session" {
		t.Fatalf("Route(/resume).ActiveSessionID = %q, want resumed-session", result.ActiveSessionID)
	}
	if result.StatusUpdate != nil || svc.statusCalls != 0 {
		t.Fatalf("Route(/resume) status = %#v calls=%d, want no synchronous status read", result.StatusUpdate, svc.statusCalls)
	}
	if result.Reconnect == nil {
		t.Fatalf("Route(/resume) reconnect = %#v, want live reconnect", result.Reconnect)
	}
	if got := firstNotice(result); got != "" {
		t.Fatalf("Route(/resume) notice = %q, want no normal success notice", got)
	}
}

func TestRouterResumePropagatesTypedGapWithoutPersistentNotice(t *testing.T) {
	svc := &fakeService{resumeSnapshot: control.SessionSnapshot{
		SessionID: "resumed-session",
		Reconnect: &routerReconnect{state: controlclient.SessionState{
			SessionID: "resumed-session", ResumeMode: controlclient.ResumeModeDurableFallback, TransientGap: true,
		}},
	}}
	result, err := New(RouterConfig{Service: svc}).Route(context.Background(), Request{
		Submission: control.Submission{Text: "/resume resumed-session"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reconnect == nil || !result.Reconnect.State().TransientGap {
		t.Fatalf("gap reconnect = %#v", result.Reconnect)
	}
	if notice := firstNotice(result); notice != "" {
		t.Fatalf("persistent gap notice = %q, want Surface-local ephemeral warning", notice)
	}
}

func TestRouterResumeBootstrapFailureHasNoDestructiveSideEffects(t *testing.T) {
	svc := &fakeService{resumeErr: errors.New("bootstrap failed")}
	result, err := New(RouterConfig{Service: svc}).Route(context.Background(), Request{
		Submission: control.Submission{Text: "/resume resumed-session"},
	})
	if err == nil {
		t.Fatal("Route(/resume) error = nil")
	}
	if result.ClearHistory || result.ActiveSessionID != "" || result.Reconnect != nil {
		t.Fatalf("failed resume result = %#v, want no Session/transcript mutation", result)
	}
}

func TestRouterNewDefersStatusUntilAfterHistoryClear(t *testing.T) {
	t.Parallel()

	svc := &fakeService{}
	result, err := New(RouterConfig{Service: svc}).Route(context.Background(), Request{
		Submission: control.Submission{Text: "/new"},
	})
	if err != nil {
		t.Fatalf("Route(/new) error = %v", err)
	}
	if !result.Handled || !result.ClearHistory || !result.RefreshStatus || !result.RefreshCommands {
		t.Fatalf("Route(/new) = %#v, want clear with deferred status refresh", result)
	}
	if result.ActiveSessionID != "new-session" {
		t.Fatalf("Route(/new).ActiveSessionID = %q, want new-session", result.ActiveSessionID)
	}
	if result.StatusUpdate != nil || svc.statusCalls != 0 {
		t.Fatalf("Route(/new) status = %#v calls=%d, want no synchronous status read", result.StatusUpdate, svc.statusCalls)
	}
}

func TestRouterHelpReturnsStructuredPayload(t *testing.T) {
	svc := &fakeService{}
	router := New(RouterConfig{
		Service: svc,
		CommandNames: func(context.Context, control.Service) []string {
			return []string{"help", "status", "breeze"}
		},
	})

	help, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/help"}})
	if err != nil {
		t.Fatalf("Route(/help) error = %v", err)
	}
	if help.SlashResult == nil || help.SlashResult.Kind != control.SlashCommandResultHelp {
		t.Fatalf("Route(/help).SlashResult = %#v, want help payload", help.SlashResult)
	}
	if got := len(help.SlashResult.Help.Items); got != 3 {
		t.Fatalf("help items = %d, want 3", got)
	}
	if help.SlashResult.Help.Items[2].Dynamic || help.SlashResult.Help.Items[2].Usage != "/breeze <prompt>" {
		t.Fatalf("profile help item = %#v, want Breeze prompt command", help.SlashResult.Help.Items[2])
	}
	if len(help.Events) != 0 {
		t.Fatalf("Route(/help).Events = %#v, want no eager fallback events", help.Events)
	}
	if text := control.FormatSlashResult(*help.SlashResult); !strings.Contains(text, "/breeze <prompt>") {
		t.Fatalf("FormatSlashResult(/help) = %q, want Breeze command", text)
	}
}

func TestRouterFixedProfileRejectsRawAgentAndRoutesNormalPrompt(t *testing.T) {
	svc := &fakeService{
		status: control.StatusSnapshot{Session: control.StatusSession{ID: "session-1"}},
		agents: []control.AgentCandidate{{Name: "helper", Description: "bounded helper"}},
		turn:   &fakeTurn{id: "turn-1"},
	}
	router := New(RouterConfig{Service: svc})
	raw, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/helper inspect repo"}})
	if err != nil {
		t.Fatalf("Route(/helper) error = %v", err)
	}
	if !raw.Handled || !strings.Contains(firstNotice(raw), "unknown command: /helper") || svc.startedAgent != "" {
		t.Fatalf("raw Agent route = %#v started=%q, want hidden", raw, svc.startedAgent)
	}
	dynamic, err := router.Route(context.Background(), Request{Submission: control.Submission{
		Text:        "/breeze inspect repo",
		Attachments: []control.Attachment{{Name: "img.png", Offset: len([]rune("/breeze inspect "))}},
	}})
	if err != nil {
		t.Fatalf("Route(/breeze) error = %v", err)
	}
	if dynamic.Turn == nil || svc.startedAgent != "breeze" || svc.startedPrompt != "inspect repo" {
		t.Fatalf("dynamic route turn=%#v agent=%q prompt=%q", dynamic.Turn, svc.startedAgent, svc.startedPrompt)
	}
	if len(svc.startedAttachments) != 1 || svc.startedAttachments[0].Offset != len([]rune("inspect ")) {
		t.Fatalf("dynamic attachments = %#v", svc.startedAttachments)
	}
	normalAt, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "@side continue"}})
	if err != nil {
		t.Fatalf("Route(@side) error = %v", err)
	}
	if normalAt.Turn == nil || svc.submitted.Text != "@side continue" || svc.continuedHandle != "" {
		t.Fatalf("@ text route turn=%#v submitted=%#v continued=%q, want normal prompt", normalAt.Turn, svc.submitted, svc.continuedHandle)
	}
	unknown, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/unknown command"}})
	if err != nil {
		t.Fatalf("Route(/unknown) error = %v", err)
	}
	if !unknown.Handled || !strings.Contains(firstNotice(unknown), "unknown command: /unknown") {
		t.Fatalf("Route(/unknown) = %#v, want fail-closed notice", unknown)
	}
	normal, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "hello"}})
	if err != nil {
		t.Fatalf("Route(normal) error = %v", err)
	}
	if normal.Turn == nil || svc.submitted.Text != "hello" {
		t.Fatalf("normal route turn=%#v submitted=%#v", normal.Turn, svc.submitted)
	}
}

func TestRouterDirectAgentRunSlashContinuesAddressableRun(t *testing.T) {
	svc := &fakeService{
		agents: []control.AgentCandidate{{Name: "helper"}},
		agentStatus: control.AgentStatusSnapshot{Participants: []control.AgentParticipantSnapshot{
			{Label: "@lina", AgentName: "helper", Kind: "acp", Role: "sidecar", Source: "slash_profile_breeze"},
			{Label: "@maya", AgentName: "helper", Kind: "acp", Role: "delegated", Source: "slash_profile_breeze"},
		}},
		turn: &fakeTurn{id: "turn-1"},
	}
	router := New(RouterConfig{
		Service: svc,
		DynamicCommandAllowed: func(_ context.Context, command string) bool {
			return command == "breeze"
		},
	})
	result, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/breeze(lina) continue"}})
	if err != nil {
		t.Fatalf("Route(/helper(lina)) error = %v", err)
	}
	if result.Turn == nil || svc.continuedHandle != "breeze(lina)" || svc.continuedPrompt != "continue" || svc.startedAgent != "" {
		t.Fatalf("Route(/breeze(lina)) = %#v continued=%q prompt=%q started=%q", result, svc.continuedHandle, svc.continuedPrompt, svc.startedAgent)
	}
	delegated, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/breeze(maya) continue"}})
	if err != nil {
		t.Fatalf("Route(/helper(maya)) error = %v", err)
	}
	if !delegated.Handled || !strings.Contains(firstNotice(delegated), "unknown command: /breeze(maya)") {
		t.Fatalf("Route(/breeze(maya)) = %#v, want delegated run hidden", delegated)
	}
}

func TestRouterPrioritizesCoreAndAgentRunsBeforeRemoteControllerCommands(t *testing.T) {
	svc := &fakeService{
		status: control.StatusSnapshot{ModelStatus: control.StatusModel{Display: "local/model"}},
		agents: []control.AgentCandidate{{Name: "helper"}},
		agentStatus: control.AgentStatusSnapshot{
			ControllerKind:     "acp",
			ControllerCommands: []string{"/foo", "/helper", "/helper(lina)", "/status"},
			AvailableAgents:    []control.AgentCandidate{{Name: "helper"}},
			Participants: []control.AgentParticipantSnapshot{
				{Label: "@lina", AgentName: "helper", Kind: "acp", Role: "sidecar", Source: "slash_profile_orbit"},
			},
		},
		turn: &fakeTurn{id: "turn-1"},
	}
	router := New(RouterConfig{Service: svc})
	attachment := control.Attachment{Name: "remote.png", Offset: len([]rune("/foo remote"))}
	remote, err := router.Route(context.Background(), Request{Submission: control.Submission{
		Text: "/foo remote", Attachments: []control.Attachment{attachment},
	}})
	if err != nil {
		t.Fatalf("Route(/foo) error = %v", err)
	}
	if remote.Turn == nil || svc.submitted.Text != "/foo remote" || !reflect.DeepEqual(svc.submitted.Attachments, []control.Attachment{attachment}) {
		t.Fatalf("Route(/foo) = %#v submitted=%#v, want original remote prompt", remote, svc.submitted)
	}
	svc.submitted = control.Submission{}
	agent, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/orbit inspect"}})
	if err != nil {
		t.Fatalf("Route(/helper) error = %v", err)
	}
	if agent.Turn == nil || svc.startedAgent != "orbit" || svc.submitted.Text != "" {
		t.Fatalf("Route(/orbit) = %#v started=%q submitted=%#v, want profile run", agent, svc.startedAgent, svc.submitted)
	}
	svc.submitted = control.Submission{}
	run, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/orbit(lina) continue"}})
	if err != nil {
		t.Fatalf("Route(/helper(lina)) error = %v", err)
	}
	if run.Turn == nil || svc.continuedHandle != "orbit(lina)" || svc.submitted.Text != "" {
		t.Fatalf("Route(/orbit(lina)) = %#v continued=%q submitted=%#v, want continuation", run, svc.continuedHandle, svc.submitted)
	}
	svc.submitted = control.Submission{}
	core, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/status"}})
	if err != nil {
		t.Fatalf("Route(/status) error = %v", err)
	}
	if core.SlashResult == nil || core.SlashResult.Kind != control.SlashCommandResultStatus || svc.submitted.Text != "" {
		t.Fatalf("Route(/status) = %#v submitted=%#v, want Caelis core", core, svc.submitted)
	}
}

func TestRouterDoesNotForwardRemovedLeadCommandToRemoteController(t *testing.T) {
	svc := &fakeService{
		agentStatus: control.AgentStatusSnapshot{
			ControllerKind: "acp", ControllerCommands: []string{"/lead"},
		},
		turn: &fakeTurn{id: "turn-1"},
	}
	result, err := New(RouterConfig{Service: svc}).Route(context.Background(), Request{
		Submission: control.Submission{Text: "/lead helper"},
	})
	if err != nil {
		t.Fatalf("Route(/lead) error = %v", err)
	}
	if !result.Handled || !strings.Contains(firstNotice(result), "unknown command: /lead") || svc.submitted.Text != "" {
		t.Fatalf("Route(/lead) = %#v submitted=%#v, want removed command hidden", result, svc.submitted)
	}
}

func TestRouterDynamicCommandAllowedOnlyPermitsFixedProfiles(t *testing.T) {
	svc := &fakeService{
		agents: []control.AgentCandidate{{Name: "reviewer"}, {Name: "helper"}},
		turn:   &fakeTurn{id: "turn-1"},
	}
	router := New(RouterConfig{
		Service: svc,
		DynamicCommandAllowed: func(_ context.Context, command string) bool {
			return command == "breeze"
		},
	})
	hidden, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/reviewer inspect"}})
	if err != nil {
		t.Fatalf("Route(/reviewer) error = %v", err)
	}
	if !hidden.Handled || !strings.Contains(firstNotice(hidden), "unknown command: /reviewer") || svc.startedAgent != "" {
		t.Fatalf("Route(/reviewer) = %#v startedAgent=%q, want fail-closed notice", hidden, svc.startedAgent)
	}
	raw, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/helper inspect"}})
	if err != nil {
		t.Fatalf("Route(/helper) error = %v", err)
	}
	if !raw.Handled || !strings.Contains(firstNotice(raw), "unknown command: /helper") || svc.startedAgent != "" {
		t.Fatalf("Route(/helper) = %#v agent=%q, want raw Agent hidden", raw, svc.startedAgent)
	}
	allowed, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/breeze inspect"}})
	if err != nil {
		t.Fatalf("Route(/breeze) error = %v", err)
	}
	if allowed.Turn == nil || svc.startedAgent != "breeze" || svc.startedPrompt != "inspect" {
		t.Fatalf("Route(/breeze) = %#v agent=%q prompt=%q, want handled profile", allowed, svc.startedAgent, svc.startedPrompt)
	}
}

func TestRouterReviewForwardsAttachmentsForPromptRange(t *testing.T) {
	svc := &fakeService{turn: &fakeTurn{id: "turn-1"}}
	router := New(RouterConfig{Service: svc})
	result, err := router.Route(context.Background(), Request{Submission: control.Submission{
		Text: "/review inspect screenshot",
		Attachments: []control.Attachment{{
			Name:     "inline.png",
			Offset:   len([]rune("/review inspect screenshot")),
			MimeType: "image/png",
			Data:     "aW1n",
		}},
	}})
	if err != nil {
		t.Fatalf("Route(/review) error = %v", err)
	}
	if result.Turn == nil || svc.reviewPrompt != "inspect screenshot" {
		t.Fatalf("review route turn=%#v prompt=%q", result.Turn, svc.reviewPrompt)
	}
	if len(svc.reviewAttachments) != 1 {
		t.Fatalf("review attachments = %#v, want one attachment", svc.reviewAttachments)
	}
	if got, want := svc.reviewAttachments[0].Offset, len([]rune("inspect screenshot")); got != want {
		t.Fatalf("review attachment offset = %d, want %d", got, want)
	}
	if got := svc.reviewAttachments[0].Data; got != "aW1n" {
		t.Fatalf("review attachment data = %q, want preserved inline data", got)
	}
}

func TestRouterRemovedAgentCommandsFailClosed(t *testing.T) {
	svc := &fakeService{}
	router := New(RouterConfig{Service: svc})

	install, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/agent install claude"}})
	if err != nil {
		t.Fatalf("Route(/agent install) error = %v", err)
	}
	if !install.Handled || !strings.Contains(firstNotice(install), "unknown command: /agent") {
		t.Fatalf("Route(/agent install) = %#v, want removed command", install)
	}

	addInstall, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/agent add --install claude"}})
	if err != nil {
		t.Fatalf("Route(/agent add --install) error = %v", err)
	}
	if !addInstall.Handled || !strings.Contains(firstNotice(addInstall), "unknown command: /agent") {
		t.Fatalf("Route(/agent add --install) = %#v, want removed command", addInstall)
	}
}

func TestRouterCoreCommandAllowedFiltersSharedSlash(t *testing.T) {
	svc := &fakeService{status: control.StatusSnapshot{
		ModelStatus: control.StatusModel{Display: "ollama/llama3"},
	}}
	router := New(RouterConfig{
		Service: svc,
		CoreCommandAllowed: func(_ context.Context, command string) bool {
			return command == "status"
		},
	})
	status, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/status"}})
	if err != nil {
		t.Fatalf("Route(/status) error = %v", err)
	}
	if !status.Handled || status.SlashResult == nil || !strings.Contains(control.FormatSlashResult(*status.SlashResult), "ollama/llama3") {
		t.Fatalf("Route(/status) = %#v, want handled status", status)
	}
	newSession, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/new"}})
	if err != nil {
		t.Fatalf("Route(/new) error = %v", err)
	}
	if !newSession.Handled || !strings.Contains(firstNotice(newSession), "unknown command: /new") {
		t.Fatalf("Route(/new) = %#v, want fail-closed notice when core command is filtered", newSession)
	}
}

func TestRouterCoreCommandAllowedBypassesTUIActiveACPGate(t *testing.T) {
	svc := &fakeService{controllerKind: "acp"}
	router := New(RouterConfig{
		Service: svc,
		CoreCommandAllowed: func(_ context.Context, command string) bool {
			return command == "compact"
		},
	})
	result, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/compact"}})
	if err != nil {
		t.Fatalf("Route(/compact) error = %v", err)
	}
	if !result.Handled || !svc.compacted {
		t.Fatalf("Route(/compact) = %#v compacted=%v, want handled despite active ACP controller", result, svc.compacted)
	}
}

func firstNotice(result Result) string {
	for _, env := range result.Events {
		if env.Kind == eventstream.KindNotice {
			return strings.TrimSpace(env.Notice)
		}
	}
	return ""
}

type fakeService struct {
	status             control.StatusSnapshot
	statusCalls        int
	agents             []control.AgentCandidate
	turn               control.Turn
	submitted          control.Submission
	usedModel          string
	usedReasoning      string
	compacted          bool
	startedAgent       string
	startedPrompt      string
	startedAttachments []control.Attachment
	reviewPrompt       string
	reviewAttachments  []control.Attachment
	continuedHandle    string
	continuedPrompt    string
	controllerKind     string
	agentStatus        control.AgentStatusSnapshot
	resumeSnapshot     control.SessionSnapshot
	resumeErr          error
}

func (s *fakeService) Status(context.Context) (control.StatusSnapshot, error) {
	s.statusCalls++
	return s.status, nil
}
func (s *fakeService) WorkspaceDir() string { return "" }
func (s *fakeService) Submit(_ context.Context, sub control.Submission) (control.Turn, error) {
	s.submitted = sub
	return s.turn, nil
}
func (s *fakeService) Interrupt(context.Context) error { return nil }
func (s *fakeService) NewSession(context.Context) (control.SessionSnapshot, error) {
	return control.SessionSnapshot{SessionID: "new-session"}, nil
}
func (s *fakeService) ResumeSession(context.Context, string) (control.SessionSnapshot, error) {
	if s.resumeErr != nil {
		return control.SessionSnapshot{}, s.resumeErr
	}
	if s.resumeSnapshot.SessionID != "" {
		return s.resumeSnapshot, nil
	}
	return control.SessionSnapshot{
		SessionID: "resumed-session",
		Reconnect: &routerReconnect{state: controlclient.SessionState{
			SessionID: "resumed-session", ResumeMode: controlclient.ResumeModeExact,
		}},
	}, nil
}
func (s *fakeService) ListSessions(context.Context, int) ([]control.ResumeCandidate, error) {
	return nil, nil
}
func (s *fakeService) Compact(context.Context) error {
	s.compacted = true
	return nil
}

type routerReconnect struct{ state controlclient.SessionState }

func (r *routerReconnect) State() controlclient.SessionState { return r.state }
func (*routerReconnect) HandleID() string                    { return "" }
func (*routerReconnect) RunID() string                       { return "" }
func (*routerReconnect) TurnID() string                      { return "" }
func (*routerReconnect) Backfill() <-chan eventstream.Envelope {
	closed := make(chan eventstream.Envelope)
	close(closed)
	return closed
}
func (*routerReconnect) Events() <-chan eventstream.Envelope {
	closed := make(chan eventstream.Envelope)
	close(closed)
	return closed
}
func (*routerReconnect) BackfillDone() <-chan struct{} {
	closed := make(chan struct{})
	close(closed)
	return closed
}
func (*routerReconnect) BootstrapEvents() []eventstream.Envelope { return nil }
func (*routerReconnect) SubmitApproval(context.Context, control.ApprovalDecision) error {
	return nil
}
func (*routerReconnect) Cancel()      {}
func (*routerReconnect) Close() error { return nil }
func (*routerReconnect) Err() error   { return nil }
func (s *fakeService) CycleSessionMode(context.Context) (control.StatusSnapshot, error) {
	return s.status, nil
}
func (s *fakeService) SetSessionMode(context.Context, string) (control.StatusSnapshot, error) {
	return s.status, nil
}
func (s *fakeService) Connect(context.Context, control.ConnectConfig) (control.StatusSnapshot, error) {
	return s.status, nil
}
func (s *fakeService) UseModel(_ context.Context, model string, reasoning ...string) (control.StatusSnapshot, error) {
	s.usedModel = model
	if len(reasoning) > 0 {
		s.usedReasoning = reasoning[0]
	}
	s.status.ModelStatus.Display = model
	return s.status, nil
}
func (s *fakeService) DeleteModel(context.Context, string) error { return nil }
func (s *fakeService) SetSandboxBackend(context.Context, string) (control.StatusSnapshot, error) {
	return s.status, nil
}
func (s *fakeService) PrepareSandbox(context.Context) (control.StatusSnapshot, error) {
	return s.status, nil
}
func (s *fakeService) RepairSandbox(context.Context) (control.StatusSnapshot, error) {
	return s.status, nil
}
func (s *fakeService) ListAgents(context.Context, int) ([]control.AgentCandidate, error) {
	return s.agents, nil
}
func (s *fakeService) AgentStatus(context.Context) (control.AgentStatusSnapshot, error) {
	status := s.agentStatus
	if status.ControllerKind == "" {
		status.ControllerKind = s.controllerKind
	}
	return status, nil
}
func (s *fakeService) HandoffAgent(context.Context, string) (control.AgentStatusSnapshot, error) {
	return control.AgentStatusSnapshot{}, nil
}
func (s *fakeService) StartAgentRun(_ context.Context, agent string, prompt string, attachments []control.Attachment) (control.Turn, error) {
	s.startedAgent = agent
	s.startedPrompt = prompt
	s.startedAttachments = attachments
	return s.turn, nil
}
func (s *fakeService) ContinueAgentRun(_ context.Context, handle string, prompt string, attachments []control.Attachment) (control.Turn, error) {
	s.continuedHandle = handle
	s.continuedPrompt = prompt
	return s.turn, nil
}
func (s *fakeService) StartReview(_ context.Context, prompt string, attachments []control.Attachment) (control.Turn, error) {
	s.reviewPrompt = prompt
	s.reviewAttachments = attachments
	return s.turn, nil
}
func (s *fakeService) CompleteFile(context.Context, string, int) ([]control.CompletionCandidate, error) {
	return nil, nil
}
func (s *fakeService) CompleteSkill(context.Context, string, int) ([]control.CompletionCandidate, error) {
	return nil, nil
}
func (s *fakeService) CompleteResume(context.Context, string, int) ([]control.ResumeCandidate, error) {
	return nil, nil
}
func (s *fakeService) CompleteSlashArg(context.Context, string, string, int) ([]control.SlashArgCandidate, error) {
	return nil, nil
}
func (s *fakeService) ListPlugins(context.Context) ([]control.PluginSnapshot, error) { return nil, nil }
func (s *fakeService) AddMarketplace(context.Context, string) (control.MarketplaceSnapshot, error) {
	return control.MarketplaceSnapshot{}, nil
}
func (s *fakeService) ListMarketplaces(context.Context) ([]control.MarketplaceSnapshot, error) {
	return nil, nil
}
func (s *fakeService) UpdateMarketplace(context.Context, string) (control.MarketplaceSnapshot, error) {
	return control.MarketplaceSnapshot{}, nil
}
func (s *fakeService) RemoveMarketplace(context.Context, string) error { return nil }
func (s *fakeService) AddPluginPath(context.Context, string) (control.PluginSnapshot, error) {
	return control.PluginSnapshot{}, nil
}
func (s *fakeService) InstallPlugin(context.Context, string) (control.PluginSnapshot, error) {
	return control.PluginSnapshot{}, nil
}
func (s *fakeService) EnablePlugin(context.Context, string) (control.PluginSnapshot, error) {
	return control.PluginSnapshot{}, nil
}
func (s *fakeService) DisablePlugin(context.Context, string) (control.PluginSnapshot, error) {
	return control.PluginSnapshot{}, nil
}
func (s *fakeService) RemovePlugin(context.Context, string) error { return nil }
func (s *fakeService) InspectPlugin(context.Context, string) (control.PluginSnapshot, error) {
	return control.PluginSnapshot{}, nil
}

type fakeTurn struct {
	id string
}

func (t *fakeTurn) HandleID() string { return t.id }
func (t *fakeTurn) RunID() string    { return t.id }
func (t *fakeTurn) TurnID() string   { return t.id }
func (t *fakeTurn) Events() <-chan eventstream.Envelope {
	ch := make(chan eventstream.Envelope)
	close(ch)
	return ch
}
func (t *fakeTurn) SubmitApproval(context.Context, control.ApprovalDecision) error { return nil }
func (t *fakeTurn) Cancel()                                                        {}
func (t *fakeTurn) Close() error                                                   { return nil }

var _ control.Service = (*fakeService)(nil)
var _ control.Turn = (*fakeTurn)(nil)

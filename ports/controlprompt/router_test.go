package controlprompt

import (
	"context"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/compact"
	"github.com/OnslaughtSnail/caelis/protocol/acp/control"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestParseSlashAndAttachmentRange(t *testing.T) {
	cmd, args, start, ok := ParseSlash("  /review check this  ")
	if !ok || cmd != "review" || args != "check this" || start != len([]rune("/review ")) {
		t.Fatalf("ParseSlash() = %q %q %d %v", cmd, args, start, ok)
	}
	attachments := AttachmentsForPromptRange([]control.Attachment{
		{Name: "before", Offset: 1},
		{Name: "inside", Offset: start + 2},
	}, start, len([]rune("/review check this")))
	if len(attachments) != 1 || attachments[0].Name != "inside" || attachments[0].Offset != 2 {
		t.Fatalf("AttachmentsForPromptRange() = %#v", attachments)
	}
}

func TestRouterStatusModelAndCompactCommands(t *testing.T) {
	svc := &fakeService{status: control.StatusSnapshot{
		Session: control.StatusSession{ID: "session-1", Workspace: "/tmp/work", ModeLabel: "auto-review"},
		ModelStatus: control.StatusModel{
			Display: "ollama/llama3",
		},
		SandboxStatus: control.StatusSandbox{ResolvedBackend: "seatbelt"},
	}}
	router := NewRouter(Config{Service: svc})
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
	compactResult, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/compact"}})
	if err != nil {
		t.Fatalf("Route(/compact) error = %v", err)
	}
	if !svc.compacted || firstNotice(compactResult) != compact.CompactNoticeLabel {
		t.Fatalf("compact route compacted=%v notice=%q", svc.compacted, firstNotice(compactResult))
	}
}

func TestRouterHelpAndSubagentListReturnStructuredPayloads(t *testing.T) {
	svc := &fakeService{
		profileStatus: control.AgentProfileStatusSnapshot{
			Profiles: []control.AgentProfileSnapshot{{
				ID:          "reviewer",
				Enabled:     true,
				Target:      "self",
				Model:       "deepseek/deepseek-v4-flash",
				Description: "review current changes",
			}},
		},
	}
	router := NewRouter(Config{
		Service: svc,
		CommandNames: func(context.Context, control.Service) []string {
			return []string{"help", "status", "subagent", "helper"}
		},
	})

	help, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/help"}})
	if err != nil {
		t.Fatalf("Route(/help) error = %v", err)
	}
	if help.SlashResult == nil || help.SlashResult.Kind != control.SlashCommandResultHelp {
		t.Fatalf("Route(/help).SlashResult = %#v, want help payload", help.SlashResult)
	}
	if got := len(help.SlashResult.Help.Items); got != 4 {
		t.Fatalf("help items = %d, want 4", got)
	}
	if !help.SlashResult.Help.Items[3].Dynamic || help.SlashResult.Help.Items[3].Usage != "/helper <prompt>" {
		t.Fatalf("dynamic help item = %#v, want helper prompt command", help.SlashResult.Help.Items[3])
	}
	if len(help.Events) != 0 {
		t.Fatalf("Route(/help).Events = %#v, want no eager fallback events", help.Events)
	}
	if text := control.FormatSlashResult(*help.SlashResult); !strings.Contains(text, "/helper <prompt>") {
		t.Fatalf("FormatSlashResult(/help) = %q, want helper command", text)
	}

	subagents, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/subagent list"}})
	if err != nil {
		t.Fatalf("Route(/subagent list) error = %v", err)
	}
	if subagents.SlashResult == nil || subagents.SlashResult.Kind != control.SlashCommandResultSubagentProfiles {
		t.Fatalf("Route(/subagent list).SlashResult = %#v, want profile payload", subagents.SlashResult)
	}
	if got := subagents.SlashResult.AgentProfiles.Profiles[0].ID; got != "reviewer" {
		t.Fatalf("subagent profile id = %q, want reviewer", got)
	}
	if len(subagents.Events) != 0 {
		t.Fatalf("Route(/subagent list).Events = %#v, want no eager fallback events", subagents.Events)
	}
}

func TestRouterDynamicAgentMentionUnknownAndNormalPrompt(t *testing.T) {
	svc := &fakeService{
		status: control.StatusSnapshot{Session: control.StatusSession{ID: "session-1"}},
		agents: []control.AgentCandidate{{Name: "helper", Description: "bounded helper"}},
		turn:   &fakeTurn{id: "turn-1"},
	}
	router := NewRouter(Config{Service: svc})
	dynamic, err := router.Route(context.Background(), Request{Submission: control.Submission{
		Text:        "/helper inspect repo",
		Attachments: []control.Attachment{{Name: "img.png", Offset: len([]rune("/helper inspect "))}},
	}})
	if err != nil {
		t.Fatalf("Route(/helper) error = %v", err)
	}
	if dynamic.Turn == nil || svc.startedAgent != "helper" || svc.startedPrompt != "inspect repo" {
		t.Fatalf("dynamic route turn=%#v agent=%q prompt=%q", dynamic.Turn, svc.startedAgent, svc.startedPrompt)
	}
	if len(svc.startedAttachments) != 1 || svc.startedAttachments[0].Offset != len([]rune("inspect ")) {
		t.Fatalf("dynamic attachments = %#v", svc.startedAttachments)
	}
	mention, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "@side continue"}})
	if err != nil {
		t.Fatalf("Route(@side) error = %v", err)
	}
	if mention.Turn == nil || svc.continuedHandle != "side" || svc.continuedPrompt != "continue" {
		t.Fatalf("mention route turn=%#v handle=%q prompt=%q", mention.Turn, svc.continuedHandle, svc.continuedPrompt)
	}
	unknown, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/unknown command"}})
	if err != nil {
		t.Fatalf("Route(/unknown) error = %v", err)
	}
	if unknown.Handled {
		t.Fatalf("Route(/unknown).Handled = true, want false")
	}
	normal, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "hello"}})
	if err != nil {
		t.Fatalf("Route(normal) error = %v", err)
	}
	if normal.Turn == nil || svc.submitted.Text != "hello" {
		t.Fatalf("normal route turn=%#v submitted=%#v", normal.Turn, svc.submitted)
	}
}

func TestRouterDynamicCommandAllowedFiltersRegisteredAgents(t *testing.T) {
	svc := &fakeService{
		agents: []control.AgentCandidate{{Name: "reviewer"}, {Name: "helper"}},
		turn:   &fakeTurn{id: "turn-1"},
	}
	router := NewRouter(Config{
		Service: svc,
		DynamicCommandAllowed: func(_ context.Context, command string) bool {
			return command == "helper"
		},
	})
	hidden, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/reviewer inspect"}})
	if err != nil {
		t.Fatalf("Route(/reviewer) error = %v", err)
	}
	if hidden.Handled || svc.startedAgent != "" {
		t.Fatalf("Route(/reviewer) = %#v startedAgent=%q, want unhandled", hidden, svc.startedAgent)
	}
	allowed, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/helper inspect"}})
	if err != nil {
		t.Fatalf("Route(/helper) error = %v", err)
	}
	if allowed.Turn == nil || svc.startedAgent != "helper" || svc.startedPrompt != "inspect" {
		t.Fatalf("Route(/helper) = %#v agent=%q prompt=%q, want handled helper", allowed, svc.startedAgent, svc.startedPrompt)
	}
}

func TestRouterReviewForwardsAttachmentsForPromptRange(t *testing.T) {
	svc := &fakeService{turn: &fakeTurn{id: "turn-1"}}
	router := NewRouter(Config{Service: svc})
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

func TestRouterAgentInstallRemainsPrivateToSurfaceHandler(t *testing.T) {
	svc := &fakeService{}
	privateCalls := 0
	router := NewRouter(Config{Service: svc})

	install, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/agent install claude"}})
	if err != nil {
		t.Fatalf("Route(/agent install) error = %v", err)
	}
	if !install.Handled || !strings.Contains(firstNotice(install), "usage: /agent") || svc.addedAgent != "" || svc.addedOptions.Install {
		t.Fatalf("Route(/agent install) = %#v added=%q opts=%#v, want usage without install", install, svc.addedAgent, svc.addedOptions)
	}

	addInstall, err := router.Route(context.Background(), Request{Submission: control.Submission{Text: "/agent add --install claude"}})
	if err != nil {
		t.Fatalf("Route(/agent add --install) error = %v", err)
	}
	if !addInstall.Handled || !strings.Contains(firstNotice(addInstall), "usage: /agent add") || svc.addedAgent != "" || svc.addedOptions.Install {
		t.Fatalf("Route(/agent add --install) = %#v added=%q opts=%#v, want usage without install", addInstall, svc.addedAgent, svc.addedOptions)
	}

	privatePayload := struct{ command string }{command: "agent install"}
	privateRouter := NewRouter(Config{
		Service: svc,
		PrivateSlashHandler: func(_ context.Context, req PrivateSlashRequest) (Result, bool, error) {
			if req.Command != "agent" || !strings.HasPrefix(req.Args, "install ") {
				return Result{}, false, nil
			}
			privateCalls++
			return Result{Handled: true, SuppressTurnDivider: true, PrivateResult: privatePayload}, true, nil
		},
	})
	privateInstall, err := privateRouter.Route(context.Background(), Request{Submission: control.Submission{Text: "/agent install claude"}})
	if err != nil {
		t.Fatalf("private Route(/agent install) error = %v", err)
	}
	if !privateInstall.Handled || !privateInstall.SuppressTurnDivider || privateCalls != 1 {
		t.Fatalf("private Route(/agent install) = %#v calls=%d, want private handled once", privateInstall, privateCalls)
	}
	if privateInstall.PrivateResult != privatePayload {
		t.Fatalf("private Route(/agent install).PrivateResult = %#v, want %#v", privateInstall.PrivateResult, privatePayload)
	}
}

func TestRouterCoreCommandAllowedFiltersSharedSlash(t *testing.T) {
	svc := &fakeService{status: control.StatusSnapshot{
		ModelStatus: control.StatusModel{Display: "ollama/llama3"},
	}}
	router := NewRouter(Config{
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
	if newSession.Handled {
		t.Fatalf("Route(/new).Handled = true, want false when core command is filtered")
	}
}

func TestRouterCoreCommandAllowedBypassesTUIActiveACPGate(t *testing.T) {
	svc := &fakeService{controllerKind: "acp"}
	router := NewRouter(Config{
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
	addedAgent         string
	addedOptions       control.AgentAddOptions
	profileStatus      control.AgentProfileStatusSnapshot
}

func (s *fakeService) Status(context.Context) (control.StatusSnapshot, error) { return s.status, nil }
func (s *fakeService) WorkspaceDir() string                                   { return "" }
func (s *fakeService) Submit(_ context.Context, sub control.Submission) (control.Turn, error) {
	s.submitted = sub
	return s.turn, nil
}
func (s *fakeService) Interrupt(context.Context) error { return nil }
func (s *fakeService) NewSession(context.Context) (control.SessionSnapshot, error) {
	return control.SessionSnapshot{SessionID: "new-session"}, nil
}
func (s *fakeService) ResumeSession(context.Context, string) (control.SessionSnapshot, error) {
	return control.SessionSnapshot{SessionID: "resumed-session"}, nil
}
func (s *fakeService) ListSessions(context.Context, int) ([]control.ResumeCandidate, error) {
	return nil, nil
}
func (s *fakeService) ReplayEvents(context.Context) ([]eventstream.Envelope, error) { return nil, nil }
func (s *fakeService) Compact(context.Context) error {
	s.compacted = true
	return nil
}
func (s *fakeService) ListSessionSnapshots(context.Context, schema.SessionListRequest) (schema.SessionListResponse, error) {
	return schema.SessionListResponse{}, nil
}
func (s *fakeService) Replay(context.Context, eventstream.ReplayRequest) (eventstream.ReplayResult, error) {
	return eventstream.ReplayResult{}, nil
}
func (s *fakeService) RunState(context.Context) (eventstream.RunState, error) {
	return eventstream.RunState{}, nil
}
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
	return control.AgentStatusSnapshot{ControllerKind: s.controllerKind}, nil
}
func (s *fakeService) AddAgent(context.Context, string) (control.AgentStatusSnapshot, error) {
	return control.AgentStatusSnapshot{}, nil
}
func (s *fakeService) AddAgentWithOptions(_ context.Context, agent string, opts control.AgentAddOptions) (control.AgentStatusSnapshot, error) {
	s.addedAgent = agent
	s.addedOptions = opts
	return control.AgentStatusSnapshot{}, nil
}
func (s *fakeService) RemoveAgent(context.Context, string) (control.AgentStatusSnapshot, error) {
	return control.AgentStatusSnapshot{}, nil
}
func (s *fakeService) HandoffAgent(context.Context, string) (control.AgentStatusSnapshot, error) {
	return control.AgentStatusSnapshot{}, nil
}
func (s *fakeService) StartAgentSubagent(_ context.Context, agent string, prompt string, attachments []control.Attachment) (control.Turn, error) {
	s.startedAgent = agent
	s.startedPrompt = prompt
	s.startedAttachments = attachments
	return s.turn, nil
}
func (s *fakeService) ContinueSubagent(_ context.Context, handle string, prompt string, attachments []control.Attachment) (control.Turn, error) {
	s.continuedHandle = handle
	s.continuedPrompt = prompt
	return s.turn, nil
}
func (s *fakeService) AgentProfileStatus(context.Context) (control.AgentProfileStatusSnapshot, error) {
	return s.profileStatus, nil
}
func (s *fakeService) BindAgentProfile(context.Context, control.AgentProfileBindingConfig) (control.AgentProfileStatusSnapshot, error) {
	return control.AgentProfileStatusSnapshot{}, nil
}
func (s *fakeService) StartReviewSubagent(_ context.Context, prompt string, attachments []control.Attachment) (control.Turn, error) {
	s.reviewPrompt = prompt
	s.reviewAttachments = attachments
	return s.turn, nil
}
func (s *fakeService) CompleteMention(context.Context, string, int) ([]control.CompletionCandidate, error) {
	return nil, nil
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

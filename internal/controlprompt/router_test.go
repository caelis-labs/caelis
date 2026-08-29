package controlprompt

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	controlstatus "github.com/caelis-labs/caelis/control/status"
)

func TestRouterStatusModelAndCompactCommands(t *testing.T) {
	svc := &fakeService{status: controlstatus.StatusSnapshot{
		Session: controlstatus.StatusSession{ID: "session-1", Workspace: "/tmp/work", ModeLabel: "auto-review"},
		ModelStatus: controlstatus.StatusModel{
			Display: "ollama/llama3",
		},
		SandboxStatus: controlstatus.StatusSandbox{ResolvedBackend: "seatbelt"},
		Usage: controlstatus.StatusUsage{
			TotalTokens:         5100,
			ContextWindowTokens: 1000000,
		},
	}}
	router := New(RouterConfig{Service: svc})
	status, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/status"}})
	if err != nil {
		t.Fatalf("Route(/status) error = %v", err)
	}
	if !status.Handled {
		t.Fatalf("Route(/status) = %#v", status)
	}
	if status.SlashResult == nil || status.SlashResult.Kind != SlashCommandResultStatus || status.SlashResult.Status.ModelStatus.Display != "ollama/llama3" {
		t.Fatalf("Route(/status).SlashResult = %#v, want structured status payload", status.SlashResult)
	}
	if status.StatusUpdate == nil || status.StatusUpdate.Usage.TotalTokens != 5100 {
		t.Fatalf("Route(/status).StatusUpdate = %#v, want the displayed snapshot applied to surface status", status.StatusUpdate)
	}
	if len(status.Events) != 0 {
		t.Fatalf("Route(/status).Events = %#v, want no eager fallback events", status.Events)
	}
	model, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/model use fast high"}})
	if err != nil {
		t.Fatalf("Route(/model use) error = %v", err)
	}
	if svc.usedModel != "fast" || svc.usedReasoning != "high" || model.StatusUpdate == nil || !model.RefreshCommands {
		t.Fatalf("model route used model=%q reasoning=%q status=%#v", svc.usedModel, svc.usedReasoning, model.StatusUpdate)
	}
	deleted, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/model del fast"}})
	if err != nil {
		t.Fatalf("Route(/model del) error = %v", err)
	}
	if !deleted.RefreshCommands {
		t.Fatalf("Route(/model del).RefreshCommands = false, want refreshed Agent slash commands")
	}
	compactResult, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/compact"}})
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

func TestDispatchDoctorReturnsStructuredFinalSnapshot(t *testing.T) {
	t.Parallel()

	svc := &fakeService{status: controlstatus.StatusSnapshot{
		Session:       controlstatus.StatusSession{ID: "session-1"},
		ModelStatus:   controlstatus.StatusModel{Provider: "openai", Name: "gpt-5.6"},
		SandboxStatus: controlstatus.StatusSandbox{ResolvedBackend: "seatbelt"},
	}}
	result, err := (router{service: svc}).dispatchDoctor(context.Background(), "")
	if err != nil {
		t.Fatalf("dispatchDoctor() error = %v", err)
	}
	if result.SlashResult == nil || result.SlashResult.Kind != SlashCommandResultDoctor || result.SlashResult.Status.Session.ID != "session-1" {
		t.Fatalf("dispatchDoctor().SlashResult = %#v, want structured final doctor snapshot", result.SlashResult)
	}
	if len(result.Events) != 0 {
		t.Fatalf("dispatchDoctor().Events = %#v, want no duplicate formatted result", result.Events)
	}
}

func TestRouterRoutesDirectSkillCommandsWithoutShadowingBuiltins(t *testing.T) {
	t.Parallel()

	svc := &fakeService{
		skillResolutions: map[string]SkillResolveResult{
			"lint":       {Canonical: "lint"},
			"brainstorm": {Canonical: "superpowers:brainstorm"},
		},
	}
	router := New(RouterConfig{Service: svc})

	direct, err := router.Route(context.Background(), Request{Submission: Submission{
		Text:        "/lint inspect this",
		DisplayText: "/lint inspect this",
		Mode:        SubmissionModeActiveTurn,
	}})
	if err != nil {
		t.Fatalf("Route(/lint) error = %v", err)
	}
	if !direct.Handled || svc.submitted.Text != "$lint inspect this" || svc.submitted.DisplayText != "/lint inspect this" || svc.submitted.Mode != SubmissionModeActiveTurn {
		t.Fatalf("Route(/lint) = %#v submission=%#v, want canonical skill submission", direct, svc.submitted)
	}

	svc.submitted = Submission{}
	shortPlugin, err := router.Route(context.Background(), Request{Submission: Submission{
		Text:        "/brainstorm compare approaches",
		DisplayText: "/brainstorm compare approaches",
	}})
	if err != nil {
		t.Fatalf("Route(/brainstorm) error = %v", err)
	}
	if !shortPlugin.Handled || svc.submitted.Text != "$superpowers:brainstorm compare approaches" || svc.submitted.DisplayText != "/brainstorm compare approaches" {
		t.Fatalf("Route(/brainstorm) = %#v submission=%#v, want Catalog-aligned canonical skill submission", shortPlugin, svc.submitted)
	}

	builtin, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/status"}})
	if err != nil {
		t.Fatalf("Route(/status) error = %v", err)
	}
	if builtin.SlashResult == nil || builtin.SlashResult.Kind != SlashCommandResultStatus {
		t.Fatalf("Route(/status) = %#v, want built-in command to win over same-name skill", builtin)
	}
}

func TestRouterDirectSkillPropagatesResolutionFailure(t *testing.T) {
	t.Parallel()

	svc := &fakeService{skillResolveErr: errors.New("runtime skill snapshot unavailable")}
	result, err := New(RouterConfig{Service: svc}).Route(context.Background(), Request{
		Submission: Submission{Text: "/lint inspect this"},
	})
	if err == nil || !strings.Contains(err.Error(), "runtime skill snapshot unavailable") {
		t.Fatalf("Route(/lint) result=%#v error=%v, want skill resolution failure", result, err)
	}
	if notice := firstNotice(result); strings.Contains(notice, "Unknown command") {
		t.Fatalf("Route(/lint) notice = %q, must not misreport a resolution failure as unknown", notice)
	}
}

func TestRouterDirectSkillReportsAmbiguousIdentity(t *testing.T) {
	t.Parallel()

	svc := &fakeService{skillResolutions: map[string]SkillResolveResult{
		"lint": {Matches: []string{"one:lint", "two:lint"}},
	}}
	result, err := New(RouterConfig{Service: svc}).Route(context.Background(), Request{
		Submission: Submission{Text: "/lint inspect this"},
	})
	if err != nil {
		t.Fatalf("Route(/lint) error = %v", err)
	}
	notice := firstNotice(result)
	for _, want := range []string{"ambiguous skill: lint", "/one:lint", "/two:lint"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("Route(/lint) notice = %q, want %q", notice, want)
		}
	}
	if svc.submitted.Text != "" {
		t.Fatalf("ambiguous direct skill submitted %#v, want no submission", svc.submitted)
	}
}

func TestRouterBestEffortUnknownSlashFallsBackToOrdinaryPrompt(t *testing.T) {
	t.Parallel()

	svc := &fakeService{turn: &fakeTurn{id: "turn-1"}}
	router := New(RouterConfig{Service: svc})

	glued, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/status这个命令怎么用?"}})
	if err != nil {
		t.Fatalf("Route(/status这个命令怎么用?) error = %v", err)
	}
	if glued.Turn == nil || firstNotice(glued) != "" || svc.submitted.Text != "/status这个命令怎么用?" {
		t.Fatalf("Route(/status这个命令怎么用?) = %#v submitted=%#v, want ordinary prompt", glued, svc.submitted)
	}

	svc.submitted = Submission{}
	unknown, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/unknown command"}})
	if err != nil {
		t.Fatalf("Route(/unknown command) error = %v", err)
	}
	if unknown.Turn == nil || firstNotice(unknown) != "" || svc.submitted.Text != "/unknown command" {
		t.Fatalf("Route(/unknown command) = %#v submitted=%#v, want ordinary prompt", unknown, svc.submitted)
	}
}

func TestRouterExactLineLeadingCommandsStillWin(t *testing.T) {
	t.Parallel()

	svc := &fakeService{status: controlstatus.StatusSnapshot{
		ModelStatus: controlstatus.StatusModel{Display: "ollama/llama3"},
	}}
	router := New(RouterConfig{Service: svc})

	status, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/status"}})
	if err != nil {
		t.Fatalf("Route(/status) error = %v", err)
	}
	if status.SlashResult == nil || status.SlashResult.Kind != SlashCommandResultStatus || svc.submitted.Text != "" {
		t.Fatalf("Route(/status) = %#v submitted=%#v, want exact command", status, svc.submitted)
	}

	usage, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/status 这个命令怎么用?"}})
	if err != nil {
		t.Fatalf("Route(/status with args) error = %v", err)
	}
	if usage.SlashResult != nil || !strings.Contains(firstNotice(usage), "usage: /status") || svc.submitted.Text != "" {
		t.Fatalf("Route(/status with args) = %#v submitted=%#v, want exact command usage", usage, svc.submitted)
	}
}

func TestRouterLeadingSkillKeepsInlineSlashSkillInSubmittedText(t *testing.T) {
	t.Parallel()

	svc := &fakeService{
		turn: &fakeTurn{id: "turn-1"},
		skillResolutions: map[string]SkillResolveResult{
			"lint": {Canonical: "lint"},
		},
	}
	text := "/lint inspect /brainstorm"
	attachment := Attachment{
		Name:     "diagram.png",
		Offset:   len([]rune("/lint inspect /brainstorm")),
		MimeType: "image/png",
		Data:     "encoded",
	}
	result, err := New(RouterConfig{Service: svc}).Route(context.Background(), Request{
		Submission: Submission{Text: text, DisplayText: text, Attachments: []Attachment{attachment}},
	})
	if err != nil {
		t.Fatalf("Route(%q) error = %v", text, err)
	}
	if !result.Handled || svc.submitted.Text != "$lint inspect /brainstorm" || svc.submitted.DisplayText != text {
		t.Fatalf("Route(%q) = %#v submission=%#v, want leading skill rewrite that keeps inline slash skill", text, result, svc.submitted)
	}
	if len(svc.submitted.Attachments) != 1 {
		t.Fatalf("Route(%q) attachments = %#v, want one", text, svc.submitted.Attachments)
	}
	got := svc.submitted.Attachments[0]
	wantOffset := len([]rune("$lint inspect /brainstorm"))
	if got.Offset != wantOffset || got.Name != attachment.Name || got.MimeType != attachment.MimeType || got.Data != attachment.Data {
		t.Fatalf("Route(%q) attachment = %#v, want offset=%d and preserved payload", text, got, wantOffset)
	}
}

func TestRouterSkillRewritePreservesAttachmentOffsets(t *testing.T) {
	t.Parallel()

	svc := &fakeService{skillResolutions: map[string]SkillResolveResult{
		"lint": {Canonical: "lint"},
	}}
	text := "/lint 看图"
	attachment := Attachment{
		Name:     "diagram.png",
		Offset:   len([]rune("/lint 看")),
		MimeType: "image/png",
		Data:     "encoded",
	}
	result, err := New(RouterConfig{Service: svc}).Route(context.Background(), Request{
		Submission: Submission{Text: text, Attachments: []Attachment{attachment}},
	})
	if err != nil {
		t.Fatalf("Route(%q) error = %v", text, err)
	}
	if !result.Handled || svc.submitted.Text != "$lint 看图" {
		t.Fatalf("Route(%q) result=%#v submission=%#v, want canonical skill text", text, result, svc.submitted)
	}
	if svc.submitted.DisplayText != text {
		t.Fatalf("Route(%q) DisplayText = %q, want original user text", text, svc.submitted.DisplayText)
	}
	if len(svc.submitted.Attachments) != 1 {
		t.Fatalf("Route(%q) attachments = %#v, want one", text, svc.submitted.Attachments)
	}
	got := svc.submitted.Attachments[0]
	wantOffset := len([]rune("$lint 看"))
	if got.Offset != wantOffset || got.Name != attachment.Name || got.MimeType != attachment.MimeType || got.Data != attachment.Data {
		t.Fatalf("Route(%q) attachment = %#v, want offset=%d and preserved payload", text, got, wantOffset)
	}
}

func TestRouterResumeReturnsLiveReconnectWithoutSuccessNotice(t *testing.T) {
	t.Parallel()

	svc := &fakeService{}
	result, err := New(RouterConfig{Service: svc}).Route(context.Background(), Request{
		Submission: Submission{Text: "/resume resumed-session"},
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
	svc := &fakeService{resumeSnapshot: SessionSnapshot{
		SessionID: "resumed-session",
		Reconnect: &routerReconnect{state: appserver.SessionState{
			SessionID: "resumed-session", ResumeMode: appserver.ResumeModeDurableFallback, TransientGap: true,
		}},
	}}
	result, err := New(RouterConfig{Service: svc}).Route(context.Background(), Request{
		Submission: Submission{Text: "/resume resumed-session"},
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
		Submission: Submission{Text: "/resume resumed-session"},
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
		Submission: Submission{Text: "/new"},
	})
	if err != nil {
		t.Fatalf("Route(/new) error = %v", err)
	}
	if !result.Handled || !result.ClearHistory || !result.RefreshStatus || !result.RefreshCommands {
		t.Fatalf("Route(/new) = %#v, want clear with deferred status refresh", result)
	}
	if result.ActiveSessionID != "" || svc.resetCalls != 1 {
		t.Fatalf("Route(/new) selection = %q resetCalls=%d, want cleared selection", result.ActiveSessionID, svc.resetCalls)
	}
	if result.StatusUpdate != nil || svc.statusCalls != 0 {
		t.Fatalf("Route(/new) status = %#v calls=%d, want no synchronous status read", result.StatusUpdate, svc.statusCalls)
	}
	if got := firstNotice(result); got != "" {
		t.Fatalf("Route(/new) notice = %q, want no success notice", got)
	}
}

func TestRouterHelpReturnsStructuredPayload(t *testing.T) {
	svc := &fakeService{}
	router := New(RouterConfig{
		Service: svc,
		CommandNames: func(context.Context, RouterService) []string {
			return []string{"help", "status", "breeze"}
		},
	})

	help, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/help"}})
	if err != nil {
		t.Fatalf("Route(/help) error = %v", err)
	}
	if help.SlashResult == nil || help.SlashResult.Kind != SlashCommandResultHelp {
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
}

func TestRouterFixedProfileRejectsRawAgentAndRoutesNormalPrompt(t *testing.T) {
	svc := &fakeService{
		status: controlstatus.StatusSnapshot{Session: controlstatus.StatusSession{ID: "session-1"}},
		agents: []AgentCandidate{{Name: "helper", Description: "bounded helper"}},
		turn:   &fakeTurn{id: "turn-1"},
	}
	router := New(RouterConfig{Service: svc})
	raw, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/helper inspect repo"}})
	if err != nil {
		t.Fatalf("Route(/helper) error = %v", err)
	}
	if raw.Turn == nil || firstNotice(raw) != "" || svc.submitted.Text != "/helper inspect repo" || svc.startedAgent != "" {
		t.Fatalf("raw Agent route = %#v started=%q submitted=%#v, want ordinary prompt without Agent dispatch", raw, svc.startedAgent, svc.submitted)
	}
	dynamic, err := router.Route(context.Background(), Request{Submission: Submission{
		Text:        "/breeze inspect repo",
		Attachments: []Attachment{{Name: "img.png", Offset: len([]rune("/breeze inspect "))}},
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
	normalAt, err := router.Route(context.Background(), Request{Submission: Submission{Text: "@side continue"}})
	if err != nil {
		t.Fatalf("Route(@side) error = %v", err)
	}
	if normalAt.Turn == nil || svc.submitted.Text != "@side continue" || svc.continuedHandle != "" {
		t.Fatalf("@ text route turn=%#v submitted=%#v continued=%q, want normal prompt", normalAt.Turn, svc.submitted, svc.continuedHandle)
	}
	unknown, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/unknown command"}})
	if err != nil {
		t.Fatalf("Route(/unknown) error = %v", err)
	}
	if unknown.Turn == nil || firstNotice(unknown) != "" || svc.submitted.Text != "/unknown command" {
		t.Fatalf("Route(/unknown) = %#v submitted=%#v, want ordinary prompt", unknown, svc.submitted)
	}
	normal, err := router.Route(context.Background(), Request{Submission: Submission{Text: "hello"}})
	if err != nil {
		t.Fatalf("Route(normal) error = %v", err)
	}
	if normal.Turn == nil || svc.submitted.Text != "hello" {
		t.Fatalf("normal route turn=%#v submitted=%#v", normal.Turn, svc.submitted)
	}
}

func TestRouterDirectAgentRunSlashContinuesAddressableRun(t *testing.T) {
	svc := &fakeService{
		agents: []AgentCandidate{{Name: "helper"}},
		agentStatus: AgentStatusSnapshot{Participants: []AgentParticipantSnapshot{
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
	result, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/breeze(lina) continue"}})
	if err != nil {
		t.Fatalf("Route(/helper(lina)) error = %v", err)
	}
	if result.Turn == nil || svc.continuedHandle != "breeze(lina)" || svc.continuedPrompt != "continue" || svc.startedAgent != "" {
		t.Fatalf("Route(/breeze(lina)) = %#v continued=%q prompt=%q started=%q", result, svc.continuedHandle, svc.continuedPrompt, svc.startedAgent)
	}
	delegated, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/breeze(maya) continue"}})
	if err != nil {
		t.Fatalf("Route(/helper(maya)) error = %v", err)
	}
	if delegated.Turn == nil || firstNotice(delegated) != "" || svc.submitted.Text != "/breeze(maya) continue" || svc.continuedHandle != "breeze(lina)" {
		t.Fatalf("Route(/breeze(maya)) = %#v continued=%q submitted=%#v, want ordinary prompt without delegated run", delegated, svc.continuedHandle, svc.submitted)
	}
}

func TestRouterDoesNotExposeRemoteControllerCommandsAndKeepsSideAgentRuns(t *testing.T) {
	svc := &fakeService{
		status: controlstatus.StatusSnapshot{ModelStatus: controlstatus.StatusModel{Display: "local/model"}},
		agents: []AgentCandidate{{Name: "helper"}},
		agentStatus: AgentStatusSnapshot{
			ControllerKind:     "acp",
			ControllerCommands: []string{"/foo", "/helper", "/helper(lina)", "/status"},
			AvailableAgents:    []AgentCandidate{{Name: "helper"}},
			Participants: []AgentParticipantSnapshot{
				{Label: "@lina", AgentName: "helper", Kind: "acp", Role: "sidecar", Source: "slash_profile_orbit"},
			},
		},
		turn: &fakeTurn{id: "turn-1"},
	}
	router := New(RouterConfig{Service: svc})
	attachment := Attachment{Name: "remote.png", Offset: len([]rune("/foo remote"))}
	remote, err := router.Route(context.Background(), Request{Submission: Submission{
		Text: "/foo remote", Attachments: []Attachment{attachment},
	}})
	if err != nil {
		t.Fatalf("Route(/foo) error = %v", err)
	}
	if remote.Turn == nil || firstNotice(remote) != "" || svc.submitted.Text != "/foo remote" || svc.startedAgent != "" {
		t.Fatalf("Route(/foo) = %#v submitted=%#v started=%q, want ordinary prompt without remote command dispatch", remote, svc.submitted, svc.startedAgent)
	}
	svc.submitted = Submission{}
	agent, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/orbit inspect"}})
	if err != nil {
		t.Fatalf("Route(/helper) error = %v", err)
	}
	if agent.Turn == nil || svc.startedAgent != "orbit" || svc.submitted.Text != "" {
		t.Fatalf("Route(/orbit) = %#v started=%q submitted=%#v, want profile run", agent, svc.startedAgent, svc.submitted)
	}
	svc.submitted = Submission{}
	run, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/orbit(lina) continue"}})
	if err != nil {
		t.Fatalf("Route(/helper(lina)) error = %v", err)
	}
	if run.Turn == nil || svc.continuedHandle != "orbit(lina)" || svc.submitted.Text != "" {
		t.Fatalf("Route(/orbit(lina)) = %#v continued=%q submitted=%#v, want continuation", run, svc.continuedHandle, svc.submitted)
	}
	svc.submitted = Submission{}
	core, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/status"}})
	if err != nil {
		t.Fatalf("Route(/status) error = %v", err)
	}
	if core.SlashResult == nil || core.SlashResult.Kind != SlashCommandResultStatus || svc.submitted.Text != "" {
		t.Fatalf("Route(/status) = %#v submitted=%#v, want Caelis core", core, svc.submitted)
	}
}

func TestRouterDoesNotForwardRemovedLeadCommandToRemoteController(t *testing.T) {
	svc := &fakeService{
		agentStatus: AgentStatusSnapshot{
			ControllerKind: "acp", ControllerCommands: []string{"/lead"},
		},
		turn: &fakeTurn{id: "turn-1"},
	}
	result, err := New(RouterConfig{Service: svc}).Route(context.Background(), Request{
		Submission: Submission{Text: "/lead helper"},
	})
	if err != nil {
		t.Fatalf("Route(/lead) error = %v", err)
	}
	if result.Turn == nil || firstNotice(result) != "" || svc.submitted.Text != "/lead helper" || svc.startedAgent != "" {
		t.Fatalf("Route(/lead) = %#v submitted=%#v started=%q, want ordinary prompt without removed command dispatch", result, svc.submitted, svc.startedAgent)
	}
}

func TestRouterDynamicCommandAllowedOnlyPermitsFixedProfiles(t *testing.T) {
	svc := &fakeService{
		agents: []AgentCandidate{{Name: "reviewer"}, {Name: "helper"}},
		turn:   &fakeTurn{id: "turn-1"},
	}
	router := New(RouterConfig{
		Service: svc,
		DynamicCommandAllowed: func(_ context.Context, command string) bool {
			return command == "breeze"
		},
	})
	hidden, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/reviewer inspect"}})
	if err != nil {
		t.Fatalf("Route(/reviewer) error = %v", err)
	}
	if hidden.Turn == nil || firstNotice(hidden) != "" || svc.submitted.Text != "/reviewer inspect" || svc.startedAgent != "" {
		t.Fatalf("Route(/reviewer) = %#v startedAgent=%q submitted=%#v, want ordinary prompt", hidden, svc.startedAgent, svc.submitted)
	}
	raw, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/helper inspect"}})
	if err != nil {
		t.Fatalf("Route(/helper) error = %v", err)
	}
	if raw.Turn == nil || firstNotice(raw) != "" || svc.submitted.Text != "/helper inspect" || svc.startedAgent != "" {
		t.Fatalf("Route(/helper) = %#v agent=%q submitted=%#v, want ordinary prompt", raw, svc.startedAgent, svc.submitted)
	}
	allowed, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/breeze inspect"}})
	if err != nil {
		t.Fatalf("Route(/breeze) error = %v", err)
	}
	if allowed.Turn == nil || svc.startedAgent != "breeze" || svc.startedPrompt != "inspect" {
		t.Fatalf("Route(/breeze) = %#v agent=%q prompt=%q, want handled profile", allowed, svc.startedAgent, svc.startedPrompt)
	}
}

func TestRouterDispatchesConfiguredCustomRoleButRejectsUnknownHandle(t *testing.T) {
	base := &fakeService{turn: &fakeTurn{id: "turn-1"}}
	svc := &bindingRouterService{
		fakeService: base,
		bindingStatus: agentbinding.Status{Handles: []agentbinding.HandleStatus{{
			Definition: agentbinding.Definition{
				Handle: "research", Class: agentbinding.HandleClassDelegation,
				Description: "Investigate unfamiliar systems.", Configurable: true, Custom: true,
			},
			Binding: agentbinding.Binding{Handle: "research", ProfileID: "provider:sol", Effort: "high"},
		}}},
	}
	router := New(RouterConfig{
		Service: svc,
		DynamicCommandAllowed: func(_ context.Context, command string) bool {
			return controlagents.IsRecoverableSourceHandle(agentbinding.Handle(command))
		},
	})
	result, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/research inspect"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Turn == nil || base.startedAgent != "research" || base.startedPrompt != "inspect" {
		t.Fatalf("Route(/research) = %#v agent=%q prompt=%q", result, base.startedAgent, base.startedPrompt)
	}
	base.startedAgent = ""
	unknown, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/unknown inspect"}})
	if err != nil {
		t.Fatal(err)
	}
	if unknown.Turn == nil || firstNotice(unknown) != "" || base.submitted.Text != "/unknown inspect" || base.startedAgent != "" {
		t.Fatalf("Route(/unknown) = %#v agent=%q submitted=%#v, want ordinary prompt", unknown, base.startedAgent, base.submitted)
	}
}

func TestRouterReviewForwardsAttachmentsForPromptRange(t *testing.T) {
	svc := &fakeService{turn: &fakeTurn{id: "turn-1"}}
	router := New(RouterConfig{Service: svc})
	result, err := router.Route(context.Background(), Request{Submission: Submission{
		Text: "/review inspect screenshot",
		Attachments: []Attachment{{
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

func TestRouterRemovedAgentCommandsFallBackToOrdinaryPrompt(t *testing.T) {
	svc := &fakeService{turn: &fakeTurn{id: "turn-1"}}
	router := New(RouterConfig{Service: svc})

	install, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/agent install claude"}})
	if err != nil {
		t.Fatalf("Route(/agent install) error = %v", err)
	}
	if install.Turn == nil || firstNotice(install) != "" || svc.submitted.Text != "/agent install claude" || svc.startedAgent != "" {
		t.Fatalf("Route(/agent install) = %#v submitted=%#v started=%q, want ordinary prompt", install, svc.submitted, svc.startedAgent)
	}

	addInstall, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/agent add --install claude"}})
	if err != nil {
		t.Fatalf("Route(/agent add --install) error = %v", err)
	}
	if addInstall.Turn == nil || firstNotice(addInstall) != "" || svc.submitted.Text != "/agent add --install claude" || svc.startedAgent != "" {
		t.Fatalf("Route(/agent add --install) = %#v submitted=%#v started=%q, want ordinary prompt", addInstall, svc.submitted, svc.startedAgent)
	}
}

func TestRouterCoreCommandAllowedFiltersSharedSlash(t *testing.T) {
	svc := &fakeService{status: controlstatus.StatusSnapshot{
		ModelStatus: controlstatus.StatusModel{Display: "ollama/llama3"},
	}}
	router := New(RouterConfig{
		Service: svc,
		CoreCommandAllowed: func(_ context.Context, command string) bool {
			return command == "status"
		},
	})
	status, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/status"}})
	if err != nil {
		t.Fatalf("Route(/status) error = %v", err)
	}
	if !status.Handled || status.SlashResult == nil || status.SlashResult.Status.ModelStatus.Display != "ollama/llama3" {
		t.Fatalf("Route(/status) = %#v, want handled status", status)
	}
	newSession, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/new"}})
	if err != nil {
		t.Fatalf("Route(/new) error = %v", err)
	}
	if firstNotice(newSession) != "" || svc.submitted.Text != "/new" || svc.resetCalls != 0 {
		t.Fatalf("Route(/new) = %#v submitted=%#v resetCalls=%d, want ordinary prompt when core command is filtered", newSession, svc.submitted, svc.resetCalls)
	}
}

func TestRouterCoreCommandAllowedDispatchesAllowedCommand(t *testing.T) {
	svc := &fakeService{}
	router := New(RouterConfig{
		Service: svc,
		CoreCommandAllowed: func(_ context.Context, command string) bool {
			return command == "compact"
		},
	})
	result, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/compact"}})
	if err != nil {
		t.Fatalf("Route(/compact) error = %v", err)
	}
	if !result.Handled || !svc.compacted {
		t.Fatalf("Route(/compact) = %#v compacted=%v, want allowed command handled", result, svc.compacted)
	}
}

func TestRouterCompactReportsNoop(t *testing.T) {
	t.Parallel()

	svc := &fakeService{compactNoop: true}
	result, err := New(RouterConfig{Service: svc}).Route(context.Background(), Request{
		Submission: Submission{Text: "/compact"},
	})
	if err != nil {
		t.Fatalf("Route(/compact) error = %v", err)
	}
	if !svc.compacted || firstNotice(result) != "Nothing to compact" {
		t.Fatalf("Route(/compact) compacted=%v notice=%q, want explicit no-op", svc.compacted, firstNotice(result))
	}
}

func TestRouterModelDeleteIsAppScoped(t *testing.T) {
	svc := &fakeService{}
	router := New(RouterConfig{Service: svc})
	usage, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/model"}})
	if err != nil {
		t.Fatalf("Route(/model) error = %v", err)
	}
	if got := firstNotice(usage); !strings.Contains(got, "del <alias>") {
		t.Fatalf("Route(/model) notice = %q, want App-scoped deletion usage", got)
	}
	result, err := router.Route(context.Background(), Request{Submission: Submission{Text: "/model del ollama/old"}})
	if err != nil {
		t.Fatalf("Route(/model del) error = %v", err)
	}
	if svc.deletedModel != "ollama/old" || !result.RefreshCommands {
		t.Fatalf("Route(/model del) deleted=%q result=%#v, want App deletion with refreshed commands", svc.deletedModel, result)
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
	status             controlstatus.StatusSnapshot
	statusCalls        int
	agents             []AgentCandidate
	turn               Turn
	submitted          Submission
	usedModel          string
	usedReasoning      string
	deletedModel       string
	compacted          bool
	compactNoop        bool
	startedAgent       string
	startedPrompt      string
	startedAttachments []Attachment
	reviewPrompt       string
	reviewAttachments  []Attachment
	continuedHandle    string
	continuedPrompt    string
	controllerKind     string
	agentStatus        AgentStatusSnapshot
	resumeSnapshot     SessionSnapshot
	resumeErr          error
	resetCalls         int
	skillResolutions   map[string]SkillResolveResult
	skillResolveErr    error
}

type bindingRouterService struct {
	*fakeService
	bindingStatus agentbinding.Status
}

func (s *bindingRouterService) AgentBindingStatus(context.Context) (agentbinding.Status, error) {
	return s.bindingStatus, nil
}

func (s *bindingRouterService) BindAgentBinding(context.Context, agentbinding.Binding) (agentbinding.Status, error) {
	return s.bindingStatus, nil
}

func (s *bindingRouterService) ResetAgentBinding(context.Context, agentbinding.Handle) (agentbinding.Status, error) {
	return s.bindingStatus, nil
}

func (s *fakeService) Status(context.Context) (controlstatus.StatusSnapshot, error) {
	s.statusCalls++
	return s.status, nil
}
func (s *fakeService) WorkspaceDir() string { return "" }
func (s *fakeService) Submit(_ context.Context, sub Submission) (Turn, error) {
	s.submitted = sub
	return s.turn, nil
}
func (s *fakeService) Interrupt(context.Context) error { return nil }
func (s *fakeService) ResetSession(context.Context) error {
	s.resetCalls++
	return nil
}
func (s *fakeService) ResumeSession(context.Context, string) (SessionSnapshot, error) {
	if s.resumeErr != nil {
		return SessionSnapshot{}, s.resumeErr
	}
	if s.resumeSnapshot.SessionID != "" {
		return s.resumeSnapshot, nil
	}
	return SessionSnapshot{
		SessionID: "resumed-session",
		Reconnect: &routerReconnect{state: appserver.SessionState{
			SessionID: "resumed-session", ResumeMode: appserver.ResumeModeExact,
		}},
	}, nil
}
func (s *fakeService) ListSessions(context.Context, int) ([]ResumeCandidate, error) {
	return nil, nil
}
func (s *fakeService) Compact(context.Context) (bool, error) {
	s.compacted = true
	return !s.compactNoop, nil
}

type routerReconnect struct{ state appserver.SessionState }

func (r *routerReconnect) State() appserver.SessionState { return r.state }
func (*routerReconnect) HandleID() string                { return "" }
func (*routerReconnect) RunID() string                   { return "" }
func (*routerReconnect) TurnID() string                  { return "" }
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
func (*routerReconnect) SubmitApproval(context.Context, ApprovalDecision) error {
	return nil
}
func (*routerReconnect) Cancel()      {}
func (*routerReconnect) Close() error { return nil }
func (*routerReconnect) Err() error   { return nil }
func (s *fakeService) CycleSessionMode(context.Context) (controlstatus.StatusSnapshot, error) {
	return s.status, nil
}
func (s *fakeService) SetSessionMode(context.Context, string) (controlstatus.StatusSnapshot, error) {
	return s.status, nil
}
func (s *fakeService) Connect(context.Context, ConnectConfig) (controlstatus.StatusSnapshot, error) {
	return s.status, nil
}
func (s *fakeService) UseModel(_ context.Context, model string, reasoning ...string) (controlstatus.StatusSnapshot, error) {
	s.usedModel = model
	if len(reasoning) > 0 {
		s.usedReasoning = reasoning[0]
	}
	s.status.ModelStatus.Display = model
	return s.status, nil
}
func (s *fakeService) DeleteModel(_ context.Context, model string) error {
	s.deletedModel = model
	return nil
}
func (s *fakeService) SetSandboxBackend(context.Context, string) (controlstatus.StatusSnapshot, error) {
	return s.status, nil
}
func (s *fakeService) PrepareSandbox(context.Context) (controlstatus.StatusSnapshot, error) {
	return s.status, nil
}
func (s *fakeService) RepairSandbox(context.Context) (controlstatus.StatusSnapshot, error) {
	return s.status, nil
}
func (s *fakeService) ListAgents(context.Context, int) ([]AgentCandidate, error) {
	return s.agents, nil
}
func (s *fakeService) AgentStatus(context.Context) (AgentStatusSnapshot, error) {
	status := s.agentStatus
	if status.ControllerKind == "" {
		status.ControllerKind = s.controllerKind
	}
	return status, nil
}
func (s *fakeService) StartAgentRun(_ context.Context, agent string, prompt string, attachments []Attachment) (Turn, error) {
	s.startedAgent = agent
	s.startedPrompt = prompt
	s.startedAttachments = attachments
	return s.turn, nil
}
func (s *fakeService) ContinueAgentRun(_ context.Context, handle string, prompt string, attachments []Attachment) (Turn, error) {
	s.continuedHandle = handle
	s.continuedPrompt = prompt
	return s.turn, nil
}
func (s *fakeService) StartReview(_ context.Context, prompt string, attachments []Attachment) (Turn, error) {
	s.reviewPrompt = prompt
	s.reviewAttachments = attachments
	return s.turn, nil
}
func (s *fakeService) CompleteFile(context.Context, string, int) ([]CompletionCandidate, error) {
	return nil, nil
}
func (s *fakeService) CompleteSkill(context.Context, string, int) ([]CompletionCandidate, error) {
	return nil, nil
}
func (s *fakeService) ResolveSkill(_ context.Context, name string) (SkillResolveResult, error) {
	if s.skillResolveErr != nil {
		return SkillResolveResult{}, s.skillResolveErr
	}
	return s.skillResolutions[strings.ToLower(strings.TrimSpace(name))], nil
}
func (s *fakeService) CompleteResume(context.Context, string, int) ([]ResumeCandidate, error) {
	return nil, nil
}
func (s *fakeService) CompleteSlashArg(context.Context, string, string, int) ([]SlashArgCandidate, error) {
	return nil, nil
}
func (s *fakeService) ListPlugins(context.Context) ([]PluginSnapshot, error) { return nil, nil }
func (s *fakeService) AddMarketplace(context.Context, string) (MarketplaceSnapshot, error) {
	return MarketplaceSnapshot{}, nil
}
func (s *fakeService) ListMarketplaces(context.Context) ([]MarketplaceSnapshot, error) {
	return nil, nil
}
func (s *fakeService) UpdateMarketplace(context.Context, string) (MarketplaceSnapshot, error) {
	return MarketplaceSnapshot{}, nil
}
func (s *fakeService) RemoveMarketplace(context.Context, string) error { return nil }
func (s *fakeService) AddPluginPath(context.Context, string) (PluginSnapshot, error) {
	return PluginSnapshot{}, nil
}
func (s *fakeService) InstallPlugin(context.Context, string) (PluginSnapshot, error) {
	return PluginSnapshot{}, nil
}
func (s *fakeService) EnablePlugin(context.Context, string) (PluginSnapshot, error) {
	return PluginSnapshot{}, nil
}
func (s *fakeService) DisablePlugin(context.Context, string) (PluginSnapshot, error) {
	return PluginSnapshot{}, nil
}
func (s *fakeService) RemovePlugin(context.Context, string) error { return nil }
func (s *fakeService) InspectPlugin(context.Context, string) (PluginSnapshot, error) {
	return PluginSnapshot{}, nil
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
func (t *fakeTurn) SubmitApproval(context.Context, ApprovalDecision) error { return nil }
func (t *fakeTurn) Cancel()                                                {}
func (t *fakeTurn) Close() error                                           { return nil }

var _ RouterService = (*fakeService)(nil)
var _ Turn = (*fakeTurn)(nil)

package tuiapp

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/ports/controlprompt/connectwizard"
)

func TestConnectEnterStartsInteractiveWizardAndIgnoresTypedArgs(t *testing.T) {
	called := ""
	m := NewModel(Config{
		ExecuteLine: func(submission Submission) TaskResultMsg {
			called = submission.Text
			return TaskResultMsg{}
		},
		Wizards: DefaultWizards(),
		SlashArgComplete: func(_ context.Context, command string, _ string, _ int) ([]SlashArgCandidate, error) {
			if command == "connect" {
				return []SlashArgCandidate{{Value: "model", Display: "Model provider"}}, nil
			}
			return nil, nil
		},
	})
	m.setInputText("/connect model")
	m.syncTextareaFromInput()
	_, cmd := m.Update(keyPress("enter"))
	if cmd != nil {
		findAndRunTaskResult(cmd(), m)
	}
	if called != "" {
		t.Fatalf("ExecuteLine called with %q, want interactive wizard instead", called)
	}
	if !m.isWizardActive() {
		t.Fatal("expected connect wizard to start")
	}
	if strings.TrimSpace(m.slashArgCommand) != "connect" {
		t.Fatalf("slashArgCommand = %q, want connect", m.slashArgCommand)
	}
	if got := m.textarea.Value(); got != "model" {
		t.Fatalf("textarea = %q, want model", got)
	}
	if got := strings.TrimSpace(m.slashArgQuery); got != "model" {
		t.Fatalf("slashArgQuery = %q, want model", got)
	}
}

func TestConnectEnterSubmitsWhenCommandUnavailable(t *testing.T) {
	called := ""
	m := NewModel(Config{
		Commands: []string{"help", "review", "status", "resume", "model"},
		ExecuteLine: func(submission Submission) TaskResultMsg {
			called = submission.Text
			return TaskResultMsg{}
		},
		Wizards: DefaultWizards(),
		SlashArgComplete: func(_ context.Context, command string, _ string, _ int) ([]SlashArgCandidate, error) {
			if command == "connect" {
				return []SlashArgCandidate{{Value: "openai-compatible", Display: "openai-compatible"}}, nil
			}
			return nil, nil
		},
	})
	m.setInputText("/connect openai-compatible")
	m.syncTextareaFromInput()
	_, cmd := m.Update(keyPress("enter"))
	if cmd != nil {
		findAndRunTaskResult(cmd(), m)
	}
	if m.isWizardActive() {
		t.Fatal("connect wizard should not start when /connect is unavailable")
	}
	if called != "/connect openai-compatible" {
		t.Fatalf("ExecuteLine called with %q, want submitted ACP command", called)
	}
}

func TestConnectTypingTrailingSpaceDoesNotOpenGenericPicker(t *testing.T) {
	m := NewModel(Config{
		ExecuteLine:      func(Submission) TaskResultMsg { return TaskResultMsg{} },
		Wizards:          DefaultWizards(),
		SlashArgComplete: func(context.Context, string, string, int) ([]SlashArgCandidate, error) { return nil, nil },
	})
	m.setInputText("/connect ")
	if len(m.slashArgCandidates) != 0 {
		t.Fatalf("unexpected slash arg candidates: %#v", m.slashArgCandidates)
	}
	if m.isWizardActive() {
		t.Fatal("wizard should not auto-open while still typing")
	}
}

func TestConnectWizardACPFlowPicksLauncherModelAndDefaults(t *testing.T) {
	called := ""
	m := NewModel(Config{
		Wizards: DefaultWizards(),
		ExecuteLine: func(submission Submission) TaskResultMsg {
			called = submission.Text
			return TaskResultMsg{}
		},
		SlashArgComplete: func(_ context.Context, command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch {
			case command == "connect":
				return []SlashArgCandidate{{Value: "acp", Display: "Local ACP Agent"}}, nil
			case command == "connect-acp-agent":
				return []SlashArgCandidate{{Value: "claude", Display: "Claude Code"}}, nil
			case command == "connect-acp-launcher:claude":
				return []SlashArgCandidate{{Value: "npx", Display: "npx"}}, nil
			case strings.HasPrefix(command, "connect-acp-model:"):
				return []SlashArgCandidate{{Value: "opus", Display: "Opus"}}, nil
			case strings.HasPrefix(command, "connect-acp-config:"):
				return []SlashArgCandidate{{Value: "default", Display: "Agent default"}}, nil
			default:
				return nil, nil
			}
		},
	})
	m.openSlashArgPicker("connect")
	for i := 0; i < 5; i++ {
		handled, cmd := m.handleWizardEnter()
		if !handled {
			t.Fatalf("wizard step %d was not handled; command=%q", i, m.slashArgCommand)
		}
		if cmd != nil {
			findAndRunTaskResult(cmd(), m)
		}
	}
	if !strings.HasPrefix(called, "/connect acp ") {
		t.Fatalf("ExecuteLine called with %q, want hidden ACP connect payload", called)
	}
	encoded := strings.TrimSpace(strings.TrimPrefix(called, "/connect acp "))
	payload, err := parseACPConnectWizardPayload(encoded)
	if err != nil {
		t.Fatalf("parseACPConnectWizardPayload() error = %v", err)
	}
	if payload.Agent != "claude" || payload.Launcher != "npx" || payload.Model != "opus" || len(payload.ConfigValues) != 0 {
		t.Fatalf("ACP connect payload = %#v", payload)
	}
}

func TestConnectWizardDisconnectRequiresAgentAndConfirmation(t *testing.T) {
	called := ""
	m := NewModel(Config{
		Wizards: DefaultWizards(),
		ExecuteLine: func(submission Submission) TaskResultMsg {
			called = submission.Text
			return TaskResultMsg{}
		},
		SlashArgComplete: func(_ context.Context, command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "disconnect", Display: "Disconnect local ACP Agent"}}, nil
			case "connect-disconnect-agent":
				return []SlashArgCandidate{{Value: "codex", Display: "/codex", Detail: "codex(gpt-5.6-sol)"}}, nil
			case "connect-disconnect-confirm:codex":
				return []SlashArgCandidate{{Value: "confirm", Display: "Disconnect /codex"}}, nil
			default:
				return nil, nil
			}
		},
	})

	m.setInputText("/connect disconnect")
	m.syncTextareaFromInput()
	if _, cmd := m.Update(keyPress("enter")); cmd != nil {
		findAndRunTaskResult(cmd(), m)
	}
	if !m.isWizardActive() {
		t.Fatal("/connect disconnect did not open the guided disconnect flow")
	}
	if handled, cmd := m.handleWizardEnter(); !handled || cmd != nil {
		t.Fatalf("source selection = handled:%v cmd:%v", handled, cmd)
	}
	if got := m.slashArgCommand; got != "connect-disconnect-agent" {
		t.Fatalf("command after source = %q, want Agent picker", got)
	}
	if handled, cmd := m.handleWizardEnter(); !handled || cmd != nil {
		t.Fatalf("Agent selection = handled:%v cmd:%v", handled, cmd)
	}
	if called != "" {
		t.Fatalf("Agent selection submitted %q before confirmation", called)
	}
	if got := m.slashArgCommand; got != "connect-disconnect-confirm:codex" {
		t.Fatalf("command after Agent = %q, want confirmation", got)
	}

	handled, cmd := m.handleWizardEnter()
	if !handled || cmd == nil {
		t.Fatalf("confirmation = handled:%v cmd:%v", handled, cmd)
	}
	if !findAndRunTaskResult(cmd(), m) {
		t.Fatal("expected TaskResultMsg after disconnect confirmation")
	}
	if called != "/connect disconnect codex confirmed" {
		t.Fatalf("ExecuteLine called with %q", called)
	}
}

func TestConnectWizardLoadsACPModelsInBackgroundWithRunningSpinner(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	m := NewModel(Config{
		Wizards: DefaultWizards(),
		SlashArgComplete: func(_ context.Context, command string, _ string, _ int) ([]SlashArgCandidate, error) {
			if strings.HasPrefix(command, "connect-acp-model:") {
				close(started)
				<-release
				return []SlashArgCandidate{{Value: "opus", Display: "Opus"}}, nil
			}
			return nil, nil
		},
	})
	m.slashArgActive = true
	m.slashArgCommand = "connect-acp-model:" + buildACPConnectWizardPayload(map[string]string{
		"acp_agent": "claude", "acp_launcher": "managed",
	})

	cmd := m.beginSlashArgLoad()
	if cmd == nil || !m.slashArgLoadPending || !m.runningIndicatorActive() {
		t.Fatalf("beginSlashArgLoad() = cmd:%v pending:%v running:%v", cmd, m.slashArgLoadPending, m.runningIndicatorActive())
	}
	if hint := ansi.Strip(m.buildHintText()); !strings.Contains(hint, "Preparing Claude Code ACP Agent") || !strings.Contains(hint, "Esc cancels") {
		t.Fatalf("buildHintText() = %q, want setup activity", hint)
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("beginSlashArgLoad() message is %T, want tea.BatchMsg", msg)
	}
	results := make(chan tea.Msg, len(batch))
	for _, one := range batch {
		go func(run tea.Cmd) { results <- run() }(one)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("ACP completion did not start in the background")
	}
	close(release)
	deadline := time.After(2 * time.Second)
	for m.slashArgLoadPending {
		select {
		case msg := <-results:
			if _, ok := msg.(slashArgLoadResultMsg); ok {
				m.Update(msg)
			}
		case <-deadline:
			t.Fatal("ACP completion did not finish")
		}
	}
	if len(m.slashArgCandidates) != 1 || m.slashArgCandidates[0].Value != "opus" {
		t.Fatalf("slashArgCandidates = %#v, want loaded Opus", m.slashArgCandidates)
	}
}

func TestConnectWizardStreamsManagedInstallProgress(t *testing.T) {
	messages := make(chan tea.Msg, 4)
	release := make(chan struct{})
	sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
	m := NewModel(Config{
		Context:       context.Background(),
		ProgramSender: sender,
		SlashArgComplete: func(ctx context.Context, _ string, _ string, _ int) ([]SlashArgCandidate, error) {
			controlagents.ReportSetupProgress(ctx, controlagents.SetupProgress{
				AdapterID: "claude", Phase: controlagents.SetupPhaseDownloading, Bytes: 128 * 1024 * 1024,
			})
			<-release
			return []SlashArgCandidate{{Value: "opus"}}, nil
		},
	})
	m.slashArgActive = true
	m.slashArgCommand = "connect-acp-model:" + buildACPConnectWizardPayload(map[string]string{
		"acp_agent": "claude", "acp_launcher": "managed",
	})

	msg := m.beginSlashArgLoad()()
	batch, ok := msg.(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("beginSlashArgLoad() message = %T, want tea.BatchMsg", msg)
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- batch[0]() }()
	select {
	case progress := <-messages:
		m.Update(progress)
	case <-time.After(time.Second):
		close(release)
		t.Fatal("managed install progress was not forwarded to the TUI")
	}
	hint := ansi.Strip(m.buildHintText())
	for _, want := range []string{"Downloading and unpacking Claude Code ACP Agent", "128.0 MB written", "Esc cancels"} {
		if !strings.Contains(hint, want) {
			close(release)
			t.Fatalf("buildHintText() = %q, want %q", hint, want)
		}
	}
	close(release)
	select {
	case loaded := <-result:
		m.Update(loaded)
	case <-time.After(time.Second):
		t.Fatal("ACP model load did not finish")
	}
}

func TestConnectWizardRunsCodexAuthenticationInBackgroundAndShowsBrowserGuidance(t *testing.T) {
	messages := make(chan tea.Msg, 4)
	release := make(chan struct{})
	sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
	m := NewModel(Config{
		Context:       context.Background(),
		ProgramSender: sender,
		Wizards:       DefaultWizards(),
		SlashArgComplete: func(ctx context.Context, command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "model", Display: "Model provider"}}, nil
			case "connect-provider":
				return []SlashArgCandidate{{Value: "codex", Display: "codex", NoAuth: true}}, nil
			default:
				modelconfig.ReportAuthProgress(ctx, modelconfig.AuthProgress{
					Provider:        "openai-codex",
					Phase:           modelconfig.AuthProgressWaitingForBrowser,
					VerificationURL: "https://auth.openai.com/oauth/authorize?test=1",
				})
				<-release
				return []SlashArgCandidate{{Value: "gpt-5.1-codex", Display: "GPT-5.1 Codex"}}, nil
			}
		},
	})
	m.width = 120
	m.openSlashArgPicker("connect")
	if handled, cmd := m.handleWizardEnter(); !handled || cmd != nil {
		t.Fatalf("model source selection = handled:%v cmd:%v", handled, cmd)
	}
	if got := m.slashArgCommand; got != "connect-provider" {
		t.Fatalf("command after model source = %q", got)
	}
	handled, cmd := m.handleWizardEnter()
	if !handled {
		t.Fatal("Codex provider selection was not handled")
	}
	if cmd == nil || !m.slashArgLoadPending || len(m.slashArgCandidates) != 0 {
		t.Fatalf("Codex provider selection = cmd:%v pending:%v candidates:%#v", cmd, m.slashArgLoadPending, m.slashArgCandidates)
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("beginSlashArgLoad() message = %T, want non-empty tea.BatchMsg", msg)
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- batch[0]() }()
	select {
	case progress := <-messages:
		m.Update(progress)
	case <-time.After(time.Second):
		close(release)
		t.Fatal("Codex auth progress was not forwarded to the TUI")
	}
	if got := ansi.Strip(m.renderModelAuthDrawer()); !strings.Contains(got, "Finish signing in via your browser") || !strings.Contains(got, "https://auth.openai.com/oauth/authorize?test=1") || !strings.Contains(got, "Esc cancels") {
		close(release)
		t.Fatalf("renderModelAuthDrawer() = %q", got)
	}
	if hint := ansi.Strip(m.buildHintText()); !strings.Contains(hint, "Finish signing in to Codex in your browser") || !strings.Contains(hint, "Esc cancels") {
		close(release)
		t.Fatalf("buildHintText() = %q", hint)
	}
	close(release)
	select {
	case loaded := <-result:
		m.Update(loaded)
	case <-time.After(time.Second):
		t.Fatal("Codex model load did not finish")
	}
	if m.slashArgLoadPending || len(m.slashArgCandidates) != 1 || m.slashArgCandidates[0].Value != "gpt-5.1-codex" {
		t.Fatalf("completed auth state = pending:%v candidates:%#v", m.slashArgLoadPending, m.slashArgCandidates)
	}
}

func TestConnectWizardShowsCodexDeviceCodeGuidance(t *testing.T) {
	m := NewModel(Config{})
	m.width = 120
	m.slashArgActive = true
	m.slashArgLoadPending = true
	m.slashArgLoadSeq = 7
	m.handleModelAuthProgress(modelAuthProgressMsg{seq: 7, progress: modelconfig.AuthProgress{
		Provider:        "openai-codex",
		Phase:           modelconfig.AuthProgressWaitingForDevice,
		VerificationURL: "https://auth.openai.com/codex/device",
		UserCode:        "ABCD-EFGH",
	}})

	got := ansi.Strip(m.renderModelAuthDrawer())
	for _, want := range []string{"Finish signing in with a device code", "https://auth.openai.com/codex/device", "ABCD-EFGH", "expires in 15 minutes", "Esc cancels"} {
		if !strings.Contains(got, want) {
			t.Fatalf("renderModelAuthDrawer() = %q, want %q", got, want)
		}
	}
}

func TestConnectWizardIgnoresStaleACPModelLoadAfterCancel(t *testing.T) {
	m := NewModel(Config{SlashArgComplete: func(context.Context, string, string, int) ([]SlashArgCandidate, error) {
		return []SlashArgCandidate{{Value: "opus"}}, nil
	}})
	m.slashArgActive = true
	m.slashArgCommand = "connect-acp-model:" + buildACPConnectWizardPayload(map[string]string{
		"acp_agent": "claude", "acp_launcher": "managed",
	})
	if cmd := m.beginSlashArgLoad(); cmd == nil {
		t.Fatal("beginSlashArgLoad() command = nil")
	}
	seq := m.slashArgLoadSeq
	command := m.slashArgCommand
	m.clearSlashArg()
	m.Update(slashArgLoadResultMsg{
		seq: seq, command: command, candidates: []SlashArgCandidate{{Value: "opus"}},
	})
	if m.slashArgLoadPending || len(m.slashArgCandidates) != 0 || m.runningIndicatorActive() {
		t.Fatalf("stale load restored canceled wizard: pending=%v candidates=%#v", m.slashArgLoadPending, m.slashArgCandidates)
	}
}

func TestConnectWizardCancelCancelsACPDiscoveryRequest(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	m := NewModel(Config{
		Context: context.Background(),
		SlashArgComplete: func(ctx context.Context, _ string, _ string, _ int) ([]SlashArgCandidate, error) {
			close(started)
			<-ctx.Done()
			close(canceled)
			return nil, ctx.Err()
		},
	})
	m.slashArgActive = true
	m.slashArgCommand = "connect-acp-model:" + buildACPConnectWizardPayload(map[string]string{
		"acp_agent": "claude", "acp_launcher": "managed",
	})

	msg := m.beginSlashArgLoad()()
	batch, ok := msg.(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("beginSlashArgLoad() message = %T, want non-empty tea.BatchMsg", msg)
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- batch[0]() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("ACP discovery request did not start")
	}

	m.clearSlashArg()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("clearing the wizard did not cancel the ACP discovery request")
	}
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("canceled ACP discovery request did not return")
	}
}

func TestACPCompletionCacheMissDoesNotCallCompleterOnUpdateLoop(t *testing.T) {
	calls := 0
	m := NewModel(Config{SlashArgComplete: func(context.Context, string, string, int) ([]SlashArgCandidate, error) {
		calls++
		return nil, nil
	}})
	m.slashArgActive = true
	m.slashArgCommand = "connect-acp-model:" + buildACPConnectWizardPayload(map[string]string{
		"acp_agent": "claude", "acp_launcher": "managed",
	})
	m.setInputText("/" + m.slashArgCommand + " ")
	m.syncTextareaFromInput()

	m.updateSlashArgCandidates()
	m.applySlashArgCompletion()
	if calls != 0 {
		t.Fatalf("completion calls during synchronous update = %d, want 0", calls)
	}
	if cmd := m.beginSlashArgLoad(); cmd == nil {
		t.Fatal("beginSlashArgLoad() = nil, want background request")
	}
	if calls != 0 {
		t.Fatalf("completion calls before background command runs = %d, want 0", calls)
	}
	m.clearSlashArg()
}

func TestACPConfigSelectionIsExclusiveByConfigID(t *testing.T) {
	step := &WizardStepDef{MultiSelect: true, MergeMultiSelect: mergeACPConfigSelection, FormatMultiSelect: formatACPConfigSelections}
	if !wizardCandidateSupportsMultiSelect(step, SlashArgCandidate{Value: "reasoning_effort=max"}) {
		t.Fatal("ACP config candidate without model metadata should support multi-select")
	}

	values := mergeWizardMultiSelectValue(step, nil, "default")
	values = mergeWizardMultiSelectValue(step, values, "reasoning_effort=max")
	if got := strings.Join(values, ","); got != "reasoning_effort=max" {
		t.Fatalf("explicit value after default = %q", got)
	}
	values = mergeWizardMultiSelectValue(step, values, "mode=manual")
	values = mergeWizardMultiSelectValue(step, values, "reasoning_effort=high")
	if got := strings.Join(values, ","); got != "mode=manual,reasoning_effort=high" {
		t.Fatalf("values after same-ID replacement = %q", got)
	}
	values = mergeWizardMultiSelectValue(step, values, "default")
	if got := strings.Join(values, ","); got != "default" {
		t.Fatalf("default after explicit values = %q", got)
	}
	if got := formatWizardMultiSelect(step, []string{"instructions=short, exact=a=b"}); got != "{\"instructions\":\"short, exact=a=b\"}" {
		t.Fatalf("formatted ACP config = %q", got)
	}
}

func TestConnectWizardGlobalLauncherDoesNotAskForCommand(t *testing.T) {
	m := NewModel(Config{
		Wizards: DefaultWizards(),
		SlashArgComplete: func(_ context.Context, command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "acp"}}, nil
			case "connect-acp-agent":
				return []SlashArgCandidate{{Value: "claude"}}, nil
			case "connect-acp-launcher:claude":
				return []SlashArgCandidate{{Value: "global"}}, nil
			default:
				if strings.HasPrefix(command, "connect-acp-model:") {
					return []SlashArgCandidate{{Value: "opus"}}, nil
				}
				return nil, nil
			}
		},
	})
	m.openSlashArgPicker("connect")
	for i := 0; i < 3; i++ {
		handled, cmd := m.handleWizardEnter()
		if !handled {
			t.Fatalf("wizard step %d was not handled", i)
		}
		if cmd != nil {
			cmd()
		}
	}
	if !strings.HasPrefix(m.slashArgCommand, "connect-acp-model:") {
		t.Fatalf("slashArgCommand = %q, global launcher should skip command step", m.slashArgCommand)
	}
}

func TestConnectWizardSkipsDirectlyToAPIKeyForMiniMax(t *testing.T) {
	m := NewModel(Config{
		Wizards: DefaultWizards(),
		SlashArgComplete: func(_ context.Context, command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "model", Display: "Model provider"}}, nil
			case "connect-provider":
				return []SlashArgCandidate{{Value: "minimax", Display: "minimax"}}, nil
			case "connect-apikey:minimax":
				return nil, nil
			default:
				return nil, nil
			}
		},
	})
	openModelConnectWizard(t, m)
	if !m.isWizardActive() {
		t.Fatal("expected connect wizard to start")
	}
	handled, cmd := m.handleWizardEnter()
	if !handled {
		t.Fatal("provider selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	if got := strings.TrimSpace(m.slashArgCommand); got != "connect-apikey:minimax" {
		t.Fatalf("slashArgCommand after minimax provider = %q, want connect-apikey:minimax", got)
	}
	if got := m.textarea.Value(); got != "" {
		t.Fatalf("textarea after minimax provider = %q, want empty wizard input", got)
	}
}

func TestConnectWizardSkipsAPIKeyForNoAuthProvider(t *testing.T) {
	m := NewModel(Config{
		Wizards: DefaultWizards(),
		SlashArgComplete: func(_ context.Context, command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "model", Display: "Model provider"}}, nil
			case "connect-provider":
				return []SlashArgCandidate{{Value: "ollama", Display: "ollama", NoAuth: true}}, nil
			default:
				state, ok := connectModelCommandState(command)
				if ok && state.Provider == "ollama" {
					return []SlashArgCandidate{{Value: "qwen2.5:7b", Display: "ollama/qwen2.5:7b"}}, nil
				}
				return nil, nil
			}
		},
	})
	openModelConnectWizard(t, m)
	if !m.isWizardActive() {
		t.Fatal("expected connect wizard to start")
	}
	handled, cmd := m.handleWizardEnter()
	if !handled {
		t.Fatal("provider selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	state := requireConnectModelCommandState(t, m.slashArgCommand)
	if state.Provider != "ollama" || state.TimeoutSeconds != connectwizard.DefaultConnectTimeoutSeconds || state.TokenRef != "" {
		t.Fatalf("connect model state after ollama provider = %#v, want provider without auth", state)
	}
	if got := m.textarea.Value(); got != "" {
		t.Fatalf("textarea after no-auth provider = %q, want empty wizard input", got)
	}
}

func TestConnectWizardKeepsBaseURLStepForCompatibleProviders(t *testing.T) {
	m := NewModel(Config{
		Wizards: DefaultWizards(),
		SlashArgComplete: func(_ context.Context, command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "model", Display: "Model provider"}}, nil
			case "connect-provider":
				return []SlashArgCandidate{{Value: "openai-compatible", Display: "openai-compatible"}}, nil
			case "connect-baseurl:openai-compatible":
				return []SlashArgCandidate{{Value: "https://api.openai.com/v1", Display: "https://api.openai.com/v1"}}, nil
			default:
				return nil, nil
			}
		},
	})
	openModelConnectWizard(t, m)
	if !m.isWizardActive() {
		t.Fatal("expected connect wizard to start")
	}
	handled, cmd := m.handleWizardEnter()
	if !handled {
		t.Fatal("provider selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	if got := strings.TrimSpace(m.slashArgCommand); got != "connect-baseurl:openai-compatible" {
		t.Fatalf("slashArgCommand after openai-compatible provider = %q, want connect-baseurl:openai-compatible", got)
	}
	if got := m.textarea.Value(); got != "" {
		t.Fatalf("textarea after openai-compatible provider = %q, want empty wizard input", got)
	}
}

func TestConnectWizardSkipsAPIKeyForReusableBaseURLAuth(t *testing.T) {
	const baseURL = "https://api.openai.com/v1"
	m := NewModel(Config{
		Wizards: DefaultWizards(),
		SlashArgComplete: func(_ context.Context, command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "model", Display: "Model provider"}}, nil
			case "connect-provider":
				return []SlashArgCandidate{{Value: "openai-compatible", Display: "openai-compatible"}}, nil
			case "connect-baseurl:openai-compatible":
				return []SlashArgCandidate{{Value: baseURL, Display: baseURL, Detail: "configured auth", NoAuth: true}}, nil
			default:
				state, ok := connectModelCommandState(command)
				if ok && state.Provider == "openai-compatible" && state.BaseURL == baseURL {
					return []SlashArgCandidate{{Value: "gpt-4o-mini", Display: "openai-compatible/gpt-4o-mini"}}, nil
				}
				return nil, nil
			}
		},
	})
	openModelConnectWizard(t, m)
	if !m.isWizardActive() {
		t.Fatal("expected connect wizard to start")
	}
	handled, cmd := m.handleWizardEnter() // provider -> baseurl
	if !handled {
		t.Fatal("provider selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	handled, cmd = m.handleWizardEnter() // reusable base URL -> model
	if !handled {
		t.Fatal("baseurl selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	state := requireConnectModelCommandState(t, m.slashArgCommand)
	if state.Provider != "openai-compatible" || state.BaseURL != baseURL || state.TokenRef != "" {
		t.Fatalf("connect model state after reusable baseurl = %#v, want model step without API key", state)
	}
}

func TestConnectWizardTypedXiaomiAdvancesToEndpointStep(t *testing.T) {
	m := NewModel(Config{
		Wizards: DefaultWizards(),
		SlashArgComplete: func(_ context.Context, command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "model", Display: "Model provider"}}, nil
			case "connect-provider":
				return []SlashArgCandidate{{Value: "xiaomi", Display: "xiaomi"}}, nil
			case "connect-baseurl:xiaomi":
				return []SlashArgCandidate{
					{Value: "https://api.xiaomimimo.com/v1", Display: "api cn"},
					{Value: "https://token-plan-cn.xiaomimimo.com/v1", Display: "token plan cn"},
				}, nil
			default:
				return nil, nil
			}
		},
	})
	m.setInputText("/connect model")
	m.syncTextareaFromInput()
	_, cmd := m.Update(keyPress("enter"))
	if cmd != nil {
		findAndRunTaskResult(cmd(), m)
	}
	if !m.isWizardActive() {
		t.Fatal("expected connect wizard to start")
	}
	advanceConnectWizardSourceToProvider(t, m)
	m.setInputText("xiaomi")
	m.syncTextareaFromInput()
	m.updateSlashArgCandidates()
	handled, cmd := m.handleWizardEnter()
	if !handled {
		t.Fatal("provider selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	if got := strings.TrimSpace(m.slashArgCommand); got != "connect-baseurl:xiaomi" {
		t.Fatalf("slashArgCommand after typed xiaomi provider = %q, want connect-baseurl:xiaomi", got)
	}
	if got := m.wizardHintText(); !strings.Contains(got, "/connect endpoint") {
		t.Fatalf("wizard hint = %q, want endpoint hint", got)
	}
}

func TestConnectWizardPrefixSelectsXiaomiCandidateAndKeepsModelCandidates(t *testing.T) {
	const tokenPlanBaseURL = "https://token-plan-cn.xiaomimimo.com/v1"
	m := NewModel(Config{
		Wizards: DefaultWizards(),
		SlashArgComplete: func(_ context.Context, command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "model", Display: "Model provider"}}, nil
			case "connect-provider":
				return []SlashArgCandidate{{Value: "xiaomi", Display: "xiaomi"}}, nil
			case "connect-baseurl:xiaomi":
				return []SlashArgCandidate{
					{Value: "https://api.xiaomimimo.com/v1", Display: "api cn"},
					{Value: tokenPlanBaseURL, Display: "token plan cn"},
				}, nil
			case "connect-apikey:xiaomi":
				return nil, nil
			default:
				state, ok := connectModelCommandState(command)
				if ok && state.Provider == "xiaomi" && state.BaseURL == tokenPlanBaseURL && state.TokenRef == "sk-test" {
					return []SlashArgCandidate{{Value: "mimo-v2.5-pro", Display: "xiaomi/mimo-v2.5-pro", ModelMetadataComplete: true}}, nil
				}
				return nil, nil
			}
		},
	})
	m.setInputText("/connect model")
	m.syncTextareaFromInput()
	_, cmd := m.Update(keyPress("enter"))
	if cmd != nil {
		findAndRunTaskResult(cmd(), m)
	}
	if !m.isWizardActive() {
		t.Fatal("expected connect wizard to start")
	}
	advanceConnectWizardSourceToProvider(t, m)
	m.setInputText("x")
	m.syncTextareaFromInput()
	m.updateSlashArgCandidates()
	handled, cmd := m.handleWizardEnter()
	if !handled {
		t.Fatal("provider prefix selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	if got := strings.TrimSpace(m.slashArgCommand); got != "connect-baseurl:xiaomi" {
		t.Fatalf("slashArgCommand after xiaomi prefix = %q, want connect-baseurl:xiaomi", got)
	}

	m.setInputText("token")
	m.syncTextareaFromInput()
	m.updateSlashArgCandidates()
	handled, cmd = m.handleWizardEnter()
	if !handled {
		t.Fatal("endpoint prefix selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	if got := strings.TrimSpace(m.slashArgCommand); got != "connect-apikey:xiaomi" {
		t.Fatalf("slashArgCommand after token-plan prefix = %q, want connect-apikey:xiaomi", got)
	}

	m.setInputText("sk-test")
	m.syncTextareaFromInput()
	m.updateSlashArgCandidates()
	handled, cmd = m.handleWizardEnter()
	if !handled {
		t.Fatal("apikey step was not handled")
	}
	if cmd != nil {
		cmd()
	}
	state := requireConnectModelCommandState(t, m.slashArgCommand)
	if state.Provider != "xiaomi" || state.BaseURL != tokenPlanBaseURL || state.TokenRef != "sk-test" {
		t.Fatalf("connect model state after api key = %#v, want selected endpoint and token", state)
	}
	if len(m.slashArgCandidates) == 0 || m.slashArgCandidates[0].Value != "mimo-v2.5-pro" {
		t.Fatalf("model candidates after api key = %#v, want mimo-v2.5-pro", m.slashArgCandidates)
	}
}

func TestConnectWizardDoesNotAcceptUnknownProviderFreeform(t *testing.T) {
	m := NewModel(Config{
		Wizards: DefaultWizards(),
		SlashArgComplete: func(_ context.Context, command string, _ string, _ int) ([]SlashArgCandidate, error) {
			if command == "connect" {
				return []SlashArgCandidate{{Value: "model", Display: "Model provider"}}, nil
			}
			if command == "connect-provider" {
				return []SlashArgCandidate{{Value: "xiaomi", Display: "xiaomi"}}, nil
			}
			return nil, nil
		},
	})
	m.setInputText("/connect model")
	m.syncTextareaFromInput()
	_, cmd := m.Update(keyPress("enter"))
	if cmd != nil {
		findAndRunTaskResult(cmd(), m)
	}
	if !m.isWizardActive() {
		t.Fatal("expected connect wizard to start")
	}
	advanceConnectWizardSourceToProvider(t, m)
	m.setInputText("xiaomi token")
	m.syncTextareaFromInput()
	m.updateSlashArgCandidates()
	handled, cmd := m.handleWizardEnter()
	if !handled {
		t.Fatal("provider step enter was not handled")
	}
	if cmd != nil {
		cmd()
	}
	if got := strings.TrimSpace(m.slashArgCommand); got != "connect-provider" {
		t.Fatalf("slashArgCommand after unknown provider input = %q, want to stay on connect-provider step", got)
	}
	if got := strings.TrimSpace(m.wizard.state["provider"]); got != "" {
		t.Fatalf("provider state = %q, want empty after rejected provider input", got)
	}
}

func TestConnectWizardAddsEndpointStepForXiaomiTokenPlan(t *testing.T) {
	const tokenPlanBaseURL = "https://token-plan-cn.xiaomimimo.com/v1"
	called := ""
	m := NewModel(Config{
		ExecuteLine: func(submission Submission) TaskResultMsg {
			called = submission.Text
			return TaskResultMsg{}
		},
		Wizards: DefaultWizards(),
		SlashArgComplete: func(_ context.Context, command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "model", Display: "Model provider"}}, nil
			case "connect-provider":
				return []SlashArgCandidate{{Value: "xiaomi", Display: "xiaomi"}}, nil
			case "connect-baseurl:xiaomi":
				return []SlashArgCandidate{
					{Value: "https://api.xiaomimimo.com/v1", Display: "api cn"},
					{Value: tokenPlanBaseURL, Display: "token plan cn"},
				}, nil
			case "connect-apikey:xiaomi":
				return nil, nil
			default:
				state, ok := connectModelCommandState(command)
				if ok && state.Provider == "xiaomi" && state.BaseURL == tokenPlanBaseURL && state.TokenRef == "env:MIMO_TOKEN_PLAN_API_KEY" {
					return []SlashArgCandidate{{Value: "mimo-v2.5-pro", Display: "xiaomi/mimo-v2.5-pro", ModelMetadataComplete: true}}, nil
				}
				return nil, nil
			}
		},
	})
	openModelConnectWizard(t, m)
	if !m.isWizardActive() {
		t.Fatal("expected connect wizard to start")
	}
	handled, cmd := m.handleWizardEnter() // provider -> endpoint
	if !handled {
		t.Fatal("provider selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	if got := strings.TrimSpace(m.slashArgCommand); got != "connect-baseurl:xiaomi" {
		t.Fatalf("slashArgCommand after xiaomi provider = %q, want connect-baseurl:xiaomi", got)
	}

	m.slashArgIndex = 1
	handled, cmd = m.handleWizardEnter() // token plan endpoint -> api key
	if !handled {
		t.Fatal("endpoint selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	if got := strings.TrimSpace(m.slashArgCommand); got != "connect-apikey:xiaomi" {
		t.Fatalf("slashArgCommand after xiaomi endpoint = %q, want connect-apikey:xiaomi", got)
	}

	m.slashArgQuery = "env:MIMO_TOKEN_PLAN_API_KEY"
	handled, cmd = m.handleWizardEnter() // api key -> model
	if !handled {
		t.Fatal("apikey step was not handled")
	}
	if cmd != nil {
		cmd()
	}
	state := requireConnectModelCommandState(t, m.slashArgCommand)
	if state.Provider != "xiaomi" || state.BaseURL != tokenPlanBaseURL || state.TokenRef != "env:MIMO_TOKEN_PLAN_API_KEY" {
		t.Fatalf("connect model state after token-plan api key = %#v, want token-plan endpoint and env token", state)
	}

	handled, cmd = m.handleWizardEnter() // model -> submit
	if !handled {
		t.Fatal("model step was not handled")
	}
	if cmd == nil {
		t.Fatal("expected submit command after known model selection")
	}
	msg := cmd()
	if !findAndRunTaskResult(msg, m) {
		t.Fatal("expected TaskResultMsg in batch")
	}
	want := "/connect xiaomi mimo-v2.5-pro " + tokenPlanBaseURL + " 60 env:MIMO_TOKEN_PLAN_API_KEY - - auto"
	if called != want {
		t.Fatalf("called = %q, want %q", called, want)
	}
}

func TestConnectWizardSkipsAPIKeyForReusableEndpointAuth(t *testing.T) {
	const apiBaseURL = "https://api.xiaomimimo.com/v1"
	m := NewModel(Config{
		Wizards: DefaultWizards(),
		SlashArgComplete: func(_ context.Context, command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "model", Display: "Model provider"}}, nil
			case "connect-provider":
				return []SlashArgCandidate{{Value: "xiaomi", Display: "xiaomi"}}, nil
			case "connect-baseurl:xiaomi":
				return []SlashArgCandidate{{Value: apiBaseURL, Display: "api cn", Detail: "configured auth", NoAuth: true}}, nil
			default:
				state, ok := connectModelCommandState(command)
				if ok && state.Provider == "xiaomi" && state.BaseURL == apiBaseURL {
					return []SlashArgCandidate{{Value: "mimo-v2-pro", Display: "xiaomi/mimo-v2-pro"}}, nil
				}
				return nil, nil
			}
		},
	})
	openModelConnectWizard(t, m)
	handled, cmd := m.handleWizardEnter() // provider -> endpoint
	if !handled {
		t.Fatal("provider selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	handled, cmd = m.handleWizardEnter() // reusable endpoint -> model
	if !handled {
		t.Fatal("endpoint selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	state := requireConnectModelCommandState(t, m.slashArgCommand)
	if state.Provider != "xiaomi" || state.BaseURL != apiBaseURL || state.TokenRef != "" {
		t.Fatalf("connect model state after reusable endpoint = %#v, want model step without API key", state)
	}
}

func TestConnectWizardAPIKeyHintUsesSelectedXiaomiEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		wantEnv string
	}{
		{name: "api cn", baseURL: "https://api.xiaomimimo.com/v1", wantEnv: "XIAOMI_API_KEY"},
		{name: "token plan cn", baseURL: "https://token-plan-cn.xiaomimimo.com/v1", wantEnv: "MIMO_TOKEN_PLAN_API_KEY"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModel(Config{
				Wizards: DefaultWizards(),
			})
			def := connectModelWizard()
			m.wizard = &wizardRuntime{
				def:       &def,
				stepIndex: 3,
				state: map[string]string{
					"provider": "xiaomi",
					"baseurl":  tt.baseURL,
				},
			}
			got := m.wizardHintText()
			if !strings.Contains(got, "env:"+tt.wantEnv) {
				t.Fatalf("wizard hint = %q, want %s", got, tt.wantEnv)
			}
			if strings.Contains(got, "OPENAI_API_KEY") {
				t.Fatalf("wizard hint = %q, should not mention OPENAI_API_KEY", got)
			}
		})
	}
}

func TestConnectWizardAPIKeyHintRecognizesCustomXiaomiTokenPlanHost(t *testing.T) {
	m := NewModel(Config{
		Wizards: DefaultWizards(),
	})
	def := connectModelWizard()
	m.wizard = &wizardRuntime{
		def:       &def,
		stepIndex: 3,
		state: map[string]string{
			"provider": "xiaomi",
			"baseurl":  "https://token-plan-cn.xiaomimimo.com/custom/v1",
		},
	}
	got := m.wizardHintText()
	if !strings.Contains(got, "env:MIMO_TOKEN_PLAN_API_KEY") {
		t.Fatalf("wizard hint = %q, want token-plan env hint", got)
	}
	if strings.Contains(got, "env:XIAOMI_API_KEY") {
		t.Fatalf("wizard hint = %q, should not prefer generic Xiaomi env", got)
	}
}

func TestConnectWizardSkipsAdvancedStepsForKnownModelCandidate(t *testing.T) {
	called := ""
	m := NewModel(Config{
		ExecuteLine: func(submission Submission) TaskResultMsg {
			called = submission.Text
			return TaskResultMsg{}
		},
		Wizards: DefaultWizards(),
		SlashArgComplete: func(_ context.Context, command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "model", Display: "Model provider"}}, nil
			case "connect-provider":
				return []SlashArgCandidate{{Value: "minimax", Display: "minimax"}}, nil
			default:
				state, ok := connectModelCommandState(command)
				if ok && state.Provider == "minimax" && state.TokenRef == "sk-test" {
					return []SlashArgCandidate{{Value: "MiniMax-M2.7-highspeed", Display: "minimax/MiniMax-M2.7-highspeed", ModelMetadataComplete: true}}, nil
				}
				return nil, nil
			}
		},
	})
	openModelConnectWizard(t, m)
	handled, cmd := m.handleWizardEnter() // provider -> apikey
	if !handled {
		t.Fatal("provider selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	m.slashArgQuery = "sk-test"
	handled, cmd = m.handleWizardEnter() // api key -> model
	if !handled {
		t.Fatal("apikey step was not handled")
	}
	if cmd != nil {
		cmd()
	}
	state := requireConnectModelCommandState(t, m.slashArgCommand)
	if state.Provider != "minimax" || state.TokenRef != "sk-test" {
		t.Fatalf("connect model state after api key = %#v, want minimax token state", state)
	}
	handled, cmd = m.handleWizardEnter() // model -> submit
	if !handled {
		t.Fatal("model step was not handled")
	}
	if cmd == nil {
		t.Fatal("expected submit command after known model selection")
	}
	msg := cmd()
	if !findAndRunTaskResult(msg, m) {
		t.Fatal("expected TaskResultMsg in batch")
	}
	if called != "/connect minimax MiniMax-M2.7-highspeed - 60 sk-test - - auto" {
		t.Fatalf("called = %q", called)
	}
}

func TestConnectWizardSelectsMultipleMetadataBackedModels(t *testing.T) {
	called := ""
	m := NewModel(Config{
		ExecuteLine: func(submission Submission) TaskResultMsg {
			called = submission.Text
			return TaskResultMsg{}
		},
		Wizards: DefaultWizards(),
		SlashArgComplete: func(_ context.Context, command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "model", Display: "Model provider"}}, nil
			case "connect-provider":
				return []SlashArgCandidate{{Value: "minimax", Display: "minimax"}}, nil
			default:
				state, ok := connectModelCommandState(command)
				if ok && state.Provider == "minimax" && state.TokenRef == "sk-test" {
					return []SlashArgCandidate{
						{Value: "MiniMax-M2.7", Display: "minimax/MiniMax-M2.7", ModelMetadataComplete: true},
						{Value: "MiniMax-M2.7-highspeed", Display: "minimax/MiniMax-M2.7-highspeed", ModelMetadataComplete: true},
					}, nil
				}
				return nil, nil
			}
		},
	})
	openModelConnectWizard(t, m)
	if handled, cmd := m.handleWizardEnter(); !handled {
		t.Fatal("provider selection was not handled")
	} else if cmd != nil {
		cmd()
	}
	m.slashArgQuery = "sk-test"
	if handled, cmd := m.handleWizardEnter(); !handled {
		t.Fatal("api key was not handled")
	} else if cmd != nil {
		cmd()
	}
	if len(m.slashArgCandidates) != 2 {
		t.Fatalf("model candidates = %#v", m.slashArgCandidates)
	}
	m.applySlashArgCompletion()
	if got := m.wizard.state["model"]; got != "MiniMax-M2.7" {
		t.Fatalf("first selected model = %q", got)
	}
	m.applySlashArgCompletion()
	if got := m.wizard.state["model"]; got != "MiniMax-M2.7,MiniMax-M2.7-highspeed" {
		t.Fatalf("selected models = %q", got)
	}
	handled, cmd := m.handleWizardEnter()
	if !handled || cmd == nil {
		t.Fatalf("multi-model confirmation = handled:%v cmd:%v", handled, cmd)
	}
	if !findAndRunTaskResult(cmd(), m) {
		t.Fatal("expected TaskResultMsg in batch")
	}
	if called != "/connect minimax MiniMax-M2.7,MiniMax-M2.7-highspeed - 60 sk-test - - auto" {
		t.Fatalf("called = %q", called)
	}
}

func TestConnectWizardKeepsAdvancedStepsForCustomCompatibleModel(t *testing.T) {
	const baseURL = "https://models.acme.example/v1"
	m := NewModel(Config{
		Wizards: DefaultWizards(),
		SlashArgComplete: func(_ context.Context, command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "model", Display: "Model provider"}}, nil
			case "connect-provider":
				return []SlashArgCandidate{{Value: "openai-compatible", Display: "openai-compatible"}}, nil
			case "connect-baseurl:openai-compatible":
				return []SlashArgCandidate{{Value: baseURL, Display: baseURL, NoAuth: true}}, nil
			default:
				return nil, nil
			}
		},
	})
	openModelConnectWizard(t, m)

	handled, cmd := m.handleWizardEnter() // provider -> baseurl
	if !handled {
		t.Fatal("provider selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	handled, cmd = m.handleWizardEnter() // reusable base URL -> model
	if !handled {
		t.Fatal("baseurl selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	if len(m.slashArgCandidates) != 0 {
		t.Fatalf("custom compatible model candidates = %#v, want free-form input", m.slashArgCandidates)
	}

	m.slashArgQuery = "acme-reasoning-model"
	handled, cmd = m.handleWizardEnter() // custom model -> context window
	if !handled {
		t.Fatal("custom model step was not handled")
	}
	if cmd != nil {
		t.Fatal("custom model should continue to advanced configuration instead of submitting")
	}
	if step := m.wizard.currentStep(); step == nil || step.Key != "context_window_tokens" {
		t.Fatalf("current wizard step = %#v, want context_window_tokens", step)
	}
	if got := m.wizard.state["_known_model"]; got != "" {
		t.Fatalf("_known_model = %q, want unset for custom compatible model", got)
	}
	if got := strings.TrimSpace(m.slashArgCommand); !strings.HasPrefix(got, "connect-context:") {
		t.Fatalf("slashArgCommand after custom model = %q, want connect-context command", got)
	}
}

func TestConnectWizardTypedKnownModelAlsoSkipsAdvancedSteps(t *testing.T) {
	called := ""
	m := NewModel(Config{
		ExecuteLine: func(submission Submission) TaskResultMsg {
			called = submission.Text
			return TaskResultMsg{}
		},
		Wizards: DefaultWizards(),
		SlashArgComplete: func(_ context.Context, command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "model", Display: "Model provider"}}, nil
			case "connect-provider":
				return []SlashArgCandidate{{Value: "minimax", Display: "minimax"}}, nil
			default:
				state, ok := connectModelCommandState(command)
				if ok && state.Provider == "minimax" && state.TokenRef == "sk-test" {
					return []SlashArgCandidate{{Value: "MiniMax-M2.7-highspeed", Display: "minimax/MiniMax-M2.7-highspeed", ModelMetadataComplete: true}}, nil
				}
				return nil, nil
			}
		},
	})
	openModelConnectWizard(t, m)
	handled, cmd := m.handleWizardEnter() // provider -> apikey
	if !handled {
		t.Fatal("provider selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	m.slashArgQuery = "sk-test"
	handled, cmd = m.handleWizardEnter() // api key -> model
	if !handled {
		t.Fatal("apikey step was not handled")
	}
	if cmd != nil {
		cmd()
	}
	m.slashArgQuery = "minimax/MiniMax-M2.7-highspeed"
	handled, cmd = m.handleWizardEnter() // typed display match -> submit
	if !handled {
		t.Fatal("typed model step was not handled")
	}
	if cmd == nil {
		t.Fatal("expected submit command after known model typed value")
	}
	msg := cmd()
	if !findAndRunTaskResult(msg, m) {
		t.Fatal("expected TaskResultMsg in batch")
	}
	if called != "/connect minimax MiniMax-M2.7-highspeed - 60 sk-test - - auto" {
		t.Fatalf("called = %q", called)
	}
}

func openModelConnectWizard(t *testing.T, m *Model) {
	t.Helper()
	m.openSlashArgPicker("connect")
	if !m.isWizardActive() {
		t.Fatal("expected connect wizard to start")
	}
	advanceConnectWizardSourceToProvider(t, m)
}

func advanceConnectWizardSourceToProvider(t *testing.T, m *Model) {
	t.Helper()
	handled, cmd := m.handleWizardEnter()
	if !handled {
		t.Fatal("model source selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	if got := strings.TrimSpace(m.slashArgCommand); got != "connect-provider" {
		t.Fatalf("slashArgCommand after model source = %q, want connect-provider", got)
	}
}

func requireConnectModelCommandState(t *testing.T, command string) connectwizard.ConnectWizardState {
	t.Helper()
	state, ok := connectModelCommandState(command)
	if !ok {
		t.Fatalf("slashArgCommand = %q, want connect-model command", command)
	}
	return state
}

func connectModelCommandState(command string) (connectwizard.ConnectWizardState, bool) {
	const prefix = "connect-model:"
	command = strings.TrimSpace(command)
	if !strings.HasPrefix(command, prefix) {
		return connectwizard.ConnectWizardState{}, false
	}
	return connectwizard.ParseConnectWizardStatePayload(strings.TrimPrefix(command, prefix)), true
}

func findAndRunTaskResult(msg tea.Msg, m *Model) bool {
	if _, ok := msg.(TaskResultMsg); ok {
		m.Update(msg)
		return true
	}
	if _, ok := msg.(slashArgLoadResultMsg); ok {
		m.Update(msg)
		return false
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, cmd := range batch {
			if cmd == nil {
				continue
			}
			subMsg := cmd()
			if subMsg == nil {
				continue
			}
			if findAndRunTaskResult(subMsg, m) {
				return true
			}
		}
	}
	return false
}

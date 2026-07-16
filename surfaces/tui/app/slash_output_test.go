package tuiapp

import (
	"context"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	controlprompt "github.com/caelis-labs/caelis/ports/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func TestSlashStatusOutputRendersStructuredSnapshot(t *testing.T) {
	t.Parallel()

	result := control.SlashCommandResult{
		Kind: control.SlashCommandResultStatus,
		Status: control.StatusSnapshot{
			Session: control.StatusSession{
				ID:        "s-123",
				Workspace: "~/WorkDir/code/caelis",
				ModeLabel: "auto-review",
			},
			ModelStatus: control.StatusModel{
				Display:         "deepseek/deepseek-v4-flash",
				ReasoningEffort: "high",
			},
			SandboxStatus: control.StatusSandbox{
				ResolvedBackend: "bwrap",
				Route:           "sandbox",
			},
			Usage: control.StatusUsage{
				TotalTokens:         16000,
				ContextWindowTokens: 1000000,
				SessionUsageTotal: control.UsageSnapshot{
					PromptTokens:      100,
					CachedInputTokens: 25,
					CompletionTokens:  40,
					ReasoningTokens:   5,
					TotalTokens:       140,
				},
				SessionUsageByModel: []control.ModelUsageSnapshot{{
					Provider: "deepseek",
					Model:    "deepseek-v4-flash",
					Usage: control.UsageSnapshot{
						PromptTokens:     100,
						CompletionTokens: 40,
						TotalTokens:      140,
					},
				}},
			},
		},
	}

	lines := renderSlashCommandResultLines(result)
	if len(lines) == 0 || strings.TrimSpace(lines[0].Text) != "Status" {
		t.Fatalf("status lines = %#v, want Status heading", lines)
	}
	plain := slashOutputPlainForTest(lines)
	if strings.HasPrefix(plain, "Model:") || strings.HasPrefix(plain, "  Model:") {
		t.Fatalf("status output should not start with a Model field:\n%s", plain)
	}
	want := strings.Join([]string{
		"Status",
		"  Model:     deepseek/deepseek-v4-flash [high]",
		"  Mode:      auto-review",
		"  Sandbox:   bwrap sandbox",
		"  Workspace: ~/WorkDir/code/caelis",
		"  Session:   s-123",
		"  Context:   16k / 1.0m \u00b7 1%",
		"",
		"Usage",
		"  Scope                       Total  Input  Cached  Output  Reasoning",
		"  ──────────────────────────  ─────  ─────  ──────  ──────  ─────────",
		"  total                       140    100    25      40      5",
		"  deepseek/deepseek-v4-flash  140    100    -       40      -",
	}, "\n")
	if plain != want {
		t.Fatalf("status output mismatch:\n--- got ---\n%s\n--- want ---\n%s", plain, want)
	}
	if strings.Contains(plain, "Workspace:~/") {
		t.Fatalf("status output should separate labels from values:\n%s", plain)
	}
}

func TestSlashHelpOutputUsesTUIGrouping(t *testing.T) {
	t.Parallel()

	lines := renderSlashCommandResultLines(control.SlashCommandResult{
		Kind: control.SlashCommandResultHelp,
		Help: control.CommandHelpSnapshot{Items: []control.CommandHelpItem{
			{Name: "help", Usage: "/help", Description: "Show commands and shortcuts", Known: true},
			{Name: "model", Usage: "/model <action>", Description: "Switch model", Details: []string{"actions: use <alias> [effort], del <alias>"}, Known: true},
			{Name: "helper", Usage: "/helper <prompt>", Description: "Send a prompt to the registered ACP agent", Dynamic: true},
		}},
	})
	plain := slashOutputPlainForTest(lines)
	want := strings.Join([]string{
		"Commands",
		"Core",
		"  /help  Show commands and shortcuts",
		"",
		"Model & Session",
		"  /model <action>  Switch model",
		"                   actions: use <alias> [effort], del <alias>",
		"",
		"Agents",
		"  /helper <prompt>  Send a prompt to the registered ACP agent",
	}, "\n")
	if plain != want {
		t.Fatalf("help output mismatch:\n--- got ---\n%s\n--- want ---\n%s", plain, want)
	}
}

func TestSlashTableOutputUsesSectionAndTableStyles(t *testing.T) {
	t.Parallel()

	lines := renderSlashCommandResultLines(control.NewTableSlashResult("subagent", control.SlashTableSnapshot{
		Title: "Subagents",
		Sections: []control.SlashTableSection{{
			Title:   "Delegation Profiles",
			Columns: []string{"Profile", "Binding"},
			Rows:    [][]string{{"breeze", "Unbound"}, {"orbit", "openai-codex/gpt-5.6-sol [high]"}},
		}},
	}))
	want := strings.Join([]string{
		"Subagents",
		"Delegation Profiles",
		"  Profile  Binding",
		"  ───────  ───────────────────────────────",
		"  breeze   Unbound",
		"  orbit    openai-codex/gpt-5.6-sol [high]",
	}, "\n")
	if got := slashOutputPlainForTest(lines); got != want {
		t.Fatalf("table output mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if lines[0].Style != tuikit.LineStyleSection || lines[2].Style != tuikit.LineStyleTableHeader || lines[3].Style != tuikit.LineStyleTableDivider {
		t.Fatalf("table styles = %#v", lines)
	}
}

func TestSlashOutputKeepsBlankLinesBeforeAndAfter(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{})
	model.handleUserMessageMsg(UserMessageMsg{Text: "/help"})
	next, _ := model.handleSlashCommandResultMsg(SlashCommandResultMsg{
		Result: control.SlashCommandResult{
			Kind: control.SlashCommandResultHelp,
			Help: control.CommandHelpSnapshot{Items: []control.CommandHelpItem{
				{Name: "help", Usage: "/help", Description: "Show commands and shortcuts", Known: true},
			}},
		},
	})
	model = next.(*Model)

	if model.doc.Len() != 4 {
		t.Fatalf("document blocks = %d, want user, spacer, slash output, spacer", model.doc.Len())
	}
	spacer, ok := model.doc.blocks[1].(*TranscriptBlock)
	if !ok || strings.TrimSpace(spacer.Raw) != "" {
		t.Fatalf("middle block = %#v, want blank spacer after user command", model.doc.blocks[1])
	}
	if _, ok := model.doc.blocks[2].(*slashOutputBlock); !ok {
		t.Fatalf("third block = %#v, want slash output block", model.doc.blocks[2])
	}
	trailing, ok := model.doc.blocks[3].(*TranscriptBlock)
	if !ok || strings.TrimSpace(trailing.Raw) != "" {
		t.Fatalf("fourth block = %#v, want trailing spacer", model.doc.blocks[3])
	}

	model.handleUserMessageMsg(UserMessageMsg{Text: "/subagent"})
	if model.doc.Len() != 5 {
		t.Fatalf("document blocks after next command = %d, want existing trailing spacer reused", model.doc.Len())
	}
	if _, ok := model.doc.blocks[4].(*UserNarrativeBlock); !ok {
		t.Fatalf("fifth block = %#v, want next user command", model.doc.blocks[4])
	}
}

func TestSlashNoticeOutputUsesAlignedPlainText(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{})
	model.handleUserMessageMsg(UserMessageMsg{Text: "/connect"})
	next, _ := model.handleSlashNoticeMsg(SlashNoticeMsg{
		Text: "connected: openai-codex/gpt-5.6-sol\nnext: /model use <model> [effort]",
	})
	model = next.(*Model)

	block, ok := model.doc.blocks[2].(*slashOutputBlock)
	if !ok {
		t.Fatalf("third block = %#v, want slash output block", model.doc.blocks[2])
	}
	if got := slashOutputPlainForTest(block.lines); got != "connected: openai-codex/gpt-5.6-sol\nnext: /model use <model> [effort]" {
		t.Fatalf("notice = %q", got)
	}
	if !block.lines[0].Plain || !block.lines[1].Plain {
		t.Fatalf("notice lines = %#v, want aligned explicit plain text", block.lines)
	}

	single := renderSlashNoticeLines(SlashNoticeMsg{Text: "model switched to: openai-codex/gpt-5.6-sol"})
	if len(single) != 1 || single[0].Text != "model switched to: openai-codex/gpt-5.6-sol" || !single[0].Plain {
		t.Fatalf("single-line notice = %#v", single)
	}
}

func TestExecuteControlPromptResultForwardsSlashResultAndEvents(t *testing.T) {
	t.Parallel()

	status := control.NewStatusSlashResult(control.StatusSnapshot{
		ModelStatus: control.StatusModel{Display: "ollama/llama3"},
	})
	var got []tea.Msg
	sender := &ProgramSender{Send: func(msg tea.Msg) {
		got = append(got, msg)
	}}
	result := controlprompt.Result{
		Handled:     true,
		SlashResult: &status,
		Events: []eventstream.Envelope{{
			Kind:   eventstream.KindNotice,
			Notice: "extra notice",
		}},
	}

	executeControlPromptResult(context.Background(), nil, sender, result)
	if len(got) != 2 {
		t.Fatalf("sent messages = %#v, want slash result and extra event", got)
	}
	if msg, ok := got[0].(SlashCommandResultMsg); !ok || msg.Result.Kind != control.SlashCommandResultStatus {
		t.Fatalf("first message = %#v, want SlashCommandResultMsg", got[0])
	}
	if notice, ok := got[1].(SlashNoticeMsg); !ok || notice.Text != "extra notice" {
		t.Fatalf("second message = %#v, want structured extra notice", got[1])
	}
}

func TestExecuteControlPromptResultAppliesPostCompactContextStatus(t *testing.T) {
	t.Parallel()

	status := control.StatusSnapshot{
		ModelStatus: control.StatusModel{Display: "xiaomi/mimo-v2.5"},
		Usage: control.StatusUsage{
			TotalTokens:         5100,
			ContextWindowTokens: 1000000,
		},
	}
	var got []tea.Msg
	sender := &ProgramSender{Send: func(msg tea.Msg) { got = append(got, msg) }}
	executeControlPromptResult(context.Background(), nil, sender, controlprompt.Result{
		Handled: true,
		Events: []eventstream.Envelope{{
			Kind:   eventstream.KindNotice,
			Notice: "Context compacted",
		}},
		StatusUpdate: &status,
	})

	if len(got) != 2 {
		t.Fatalf("sent messages = %#v, want compact notice followed by status update", got)
	}
	if notice, ok := got[0].(SlashNoticeMsg); !ok || notice.Text != "Context compacted" {
		t.Fatalf("first message = %#v, want structured compact notice", got[0])
	}
	update, ok := got[1].(SetStatusMsg)
	if !ok {
		t.Fatalf("second message = %#v, want SetStatusMsg", got[1])
	}
	if want := "5.1k / 1.0m · 0%"; update.Context != want || update.Status.Tokens != want {
		t.Fatalf("post-compact context = %q / %q, want %q", update.Context, update.Status.Tokens, want)
	}
}

func TestExecuteControlPromptResultDefersNewSessionStatusAfterClearAndNotice(t *testing.T) {
	t.Parallel()

	var got []tea.Msg
	sender := &ProgramSender{Send: func(msg tea.Msg) { got = append(got, msg) }}
	executeControlPromptResult(context.Background(), &modelConnectControlStub{}, sender, controlprompt.Result{
		Handled:      true,
		ClearHistory: true,
		Events: []eventstream.Envelope{{
			Kind:   eventstream.KindNotice,
			Notice: "new session: session-2",
		}},
		ActiveSessionID: "session-2",
		RefreshStatus:   true,
		RefreshCommands: true,
	})

	if len(got) != 4 {
		t.Fatalf("sent messages = %#v, want clear, notice, deferred status, commands", got)
	}
	if _, ok := got[0].(ClearHistoryMsg); !ok {
		t.Fatalf("first message = %#v, want ClearHistoryMsg", got[0])
	}
	if notice, ok := got[1].(SlashNoticeMsg); !ok || notice.Text != "new session: session-2" {
		t.Fatalf("second message = %#v, want structured new-session notice", got[1])
	}
	if _, ok := got[2].(statusRefreshRequestMsg); !ok {
		t.Fatalf("third message = %#v, want deferred status refresh", got[2])
	}
	commands, ok := got[3].(SetCommandsMsg)
	if !ok || slices.Contains(commands.Commands, "breeze") || slices.Contains(commands.Commands, "orbit") || slices.Contains(commands.Commands, "zenith") || slices.Contains(commands.Commands, "sol") {
		t.Fatalf("last message = %#v, want unbound profiles and raw Agent IDs hidden", got[3])
	}
}

func slashOutputPlainForTest(lines []slashOutputLine) string {
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		parts = append(parts, line.Text)
	}
	return strings.Join(parts, "\n")
}

package tuiapp

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	controlprompt "github.com/caelis-labs/caelis/ports/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
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
			{Name: "model", Usage: "/model <action>", Description: "Switch model", Details: []string{"actions: use <alias>, del <alias>"}, Known: true},
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
		"                   actions: use <alias>, del <alias>",
		"",
		"Agents",
		"  /helper <prompt>  Send a prompt to the registered ACP agent",
	}, "\n")
	if plain != want {
		t.Fatalf("help output mismatch:\n--- got ---\n%s\n--- want ---\n%s", plain, want)
	}
}

func TestSlashOutputAddsBlankLineAfterUserCommand(t *testing.T) {
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

	if model.doc.Len() != 3 {
		t.Fatalf("document blocks = %d, want user, spacer, slash output", model.doc.Len())
	}
	spacer, ok := model.doc.blocks[1].(*TranscriptBlock)
	if !ok || strings.TrimSpace(spacer.Raw) != "" {
		t.Fatalf("middle block = %#v, want blank spacer after user command", model.doc.blocks[1])
	}
	if _, ok := model.doc.blocks[2].(*slashOutputBlock); !ok {
		t.Fatalf("third block = %#v, want slash output block", model.doc.blocks[2])
	}
}

func TestSlashSubagentOutputRendersProfileData(t *testing.T) {
	t.Parallel()

	lines := renderSlashCommandResultLines(control.SlashCommandResult{
		Kind: control.SlashCommandResultSubagentProfiles,
		AgentProfiles: control.AgentProfileStatusSnapshot{
			Profiles: []control.AgentProfileSnapshot{
				{ID: "reviewer", Enabled: true, Target: "self", Model: "deepseek/deepseek-v4-flash", ReasoningEffort: "high", Description: "review changes"},
				{ID: "explorer", Enabled: false, Target: "self", Warning: "disabled for this workspace"},
			},
		},
	})
	plain := slashOutputPlainForTest(lines)
	want := strings.Join([]string{
		"Subagents",
		"  Profile   Binding                            Status    Description",
		"  ────────  ─────────────────────────────────  ────────  ──────────────",
		"  explorer  disabled                           disabled",
		"  reviewer  deepseek/deepseek-v4-flash [high]  ready     review changes",
		"",
		"Warnings",
		"  Warning:   explorer: disabled for this workspace",
	}, "\n")
	if plain != want {
		t.Fatalf("subagent output mismatch:\n--- got ---\n%s\n--- want ---\n%s", plain, want)
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
	if env, ok := got[1].(eventstream.Envelope); !ok || env.Kind != eventstream.KindNotice || env.Notice != "extra notice" {
		t.Fatalf("second message = %#v, want extra notice event", got[1])
	}
}

func slashOutputPlainForTest(lines []slashOutputLine) string {
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		parts = append(parts, line.Text)
	}
	return strings.Join(parts, "\n")
}

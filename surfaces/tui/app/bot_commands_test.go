package tuiapp

import (
	"slices"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func TestBotCommandRefreshPreservesSurfaceCatalog(t *testing.T) {
	model := newBotTestModel(t, 80, 24, &fakeBotClient{}, nil)
	model.setInputText("/")
	for _, update := range []SetCommandsMsg{
		{Commands: append(DefaultCommands(), "custom-agent"), Details: map[string]string{"new": "Start a coding Session", "custom-agent": "Run an Agent"}},
		{Commands: []string{"help", "breeze"}},
	} {
		model.Update(update)
		if !slices.Equal(model.cfg.Commands, BotCommands()) {
			t.Fatalf("refreshed commands = %v, want %v", model.cfg.Commands, BotCommands())
		}
		assertBotCommandCandidates(t, model)
		if got := model.commandCompletionDetail("new"); got != BotCommandDetails()["new"] {
			t.Fatalf("refreshed /new detail = %q", got)
		}
		if len(model.palette.Items()) != len(BotCommands()) {
			t.Fatalf("palette has %d items, want %d", len(model.palette.Items()), len(BotCommands()))
		}
		for _, item := range model.palette.Items() {
			if command := item.(commandItem); !isBotCommand(command.name) {
				t.Fatalf("palette leaked %q", command.name)
			}
		}
	}

	// Work mode still accepts the dynamic Host catalog and its descriptions.
	work := NewModel(Config{Commands: DefaultCommands()})
	work.Update(SetCommandsMsg{Commands: []string{"custom-agent"}, Details: map[string]string{"custom-agent": "Custom Agent"}})
	if !slices.Equal(work.cfg.Commands, []string{"custom-agent"}) || work.commandCompletionDetail("custom-agent") != "Custom Agent" {
		t.Fatalf("Work refresh lost its dynamic catalog: %#v", work.cfg.Commands)
	}
}

func assertBotCommandCandidates(t *testing.T, model *Model) {
	t.Helper()
	model.setInputText("/")
	model.refreshSlashCommands()
	want := make([]string, 0, len(BotCommands()))
	for _, command := range BotCommands() {
		want = append(want, "/"+command)
	}
	slices.Sort(want)
	got := slices.Clone(model.slashCandidates)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("Bot slash candidates = %v, want %v", got, want)
	}
}

func TestBotSlashCompletionDoesNotLoadWorkspaceSkills(t *testing.T) {
	model := NewModel(Config{
		Bot: &BotSurface{Client: &fakeBotClient{}},
		SkillComplete: func(string, int) ([]CompletionCandidate, error) {
			t.Fatal("Bot requested workspace skills")
			return nil, nil
		},
	})
	for _, input := range []string{"/", "/skill:", "Please use /"} {
		model.setInputText(input)
		if cmd := model.requestSlashSkillCatalog(); cmd != nil {
			t.Fatalf("Bot scheduled a workspace skill request for %q", input)
		}
		model.refreshSlashCommands()
		if input != "/" && len(model.slashCandidates) != 0 {
			t.Fatalf("Bot skill reference candidates for %q = %v", input, model.slashCandidates)
		}
	}
	assertBotCommandCandidates(t, model)
}

func TestBotSharedConfigurationCommandsAndAliases(t *testing.T) {
	for _, name := range []string{"connect", "disconnect", "team", "subagent", "theme", "status", "quit", "exit"} {
		if !isBotCommand(name) {
			t.Fatalf("shared command or alias %q is blocked", name)
		}
	}
	model := newBotTestModel(t, 80, 24, &fakeBotClient{}, nil)
	model.setInputText("/sub")
	model.refreshSlashCommands()
	if !slices.Equal(model.slashCandidates, []string{"/team"}) {
		t.Fatalf("team alias completion = %v", model.slashCandidates)
	}
	for _, name := range []string{"breeze", "orbit", "zenith", "review", "compact", "sandbox", "plugin", "memory"} {
		if isBotCommand(name) {
			t.Fatalf("execution command %q is allowed", name)
		}
	}
	for _, name := range BotCommands() {
		if spec, ok := controlprompt.Lookup(name); ok {
			for _, alias := range spec.Aliases {
				if !isBotCommand(alias) {
					t.Fatalf("Bot command %q advertises blocked alias %q", name, alias)
				}
			}
		}
		if strings.TrimSpace(model.commandCompletionDetail(name)) == "" {
			t.Fatalf("Bot command %q has no completion detail", name)
		}
	}
}

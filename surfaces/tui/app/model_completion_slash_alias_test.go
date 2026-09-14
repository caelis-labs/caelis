package tuiapp

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func defaultAliasCompletionModel() *Model {
	return NewModel(Config{Commands: DefaultCommands()})
}

func TestSlashCompletionListsCanonicalTeamAndQuitOnce(t *testing.T) {
	model := defaultAliasCompletionModel()
	model.setInputText("/")
	model.refreshSlashCommands()

	counts := map[string]int{}
	for _, candidate := range model.slashCandidates {
		counts[candidate]++
	}
	for _, want := range []string{"/team", "/quit"} {
		if counts[want] != 1 {
			t.Fatalf("slashCandidates = %#v, want exactly one %s", model.slashCandidates, want)
		}
	}
	for _, alias := range []string{"/subagent", "/exit"} {
		if counts[alias] != 0 {
			t.Fatalf("slashCandidates = %#v, must not advertise alias %s", model.slashCandidates, alias)
		}
	}
	if got := model.slashCommandDisplay("/team"); got != "/team (subagent)" {
		t.Fatalf("slashCommandDisplay(/team) = %q, want /team (subagent)", got)
	}
	if got := model.slashCommandDisplay("/quit"); got != "/quit (exit)" {
		t.Fatalf("slashCommandDisplay(/quit) = %q, want /quit (exit)", got)
	}
}

func TestSlashCompletionAliasQueryResolvesToCanonical(t *testing.T) {
	cases := []struct {
		query   string
		want    string
		display string
	}{
		{query: "/sub", want: "/team", display: "/team (subagent)"},
		{query: "/ex", want: "/quit", display: "/quit (exit)"},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			model := defaultAliasCompletionModel()
			model.setInputText(tc.query)
			model.refreshSlashCommands()

			if got := model.slashCandidates; !reflect.DeepEqual(got, []string{tc.want}) {
				t.Fatalf("slashCandidates for %s = %#v, want [%s]", tc.query, got, tc.want)
			}
			if got := model.slashCommandDisplay(tc.want); got != tc.display {
				t.Fatalf("slashCommandDisplay(%s) = %q, want %q", tc.want, got, tc.display)
			}
		})
	}
}

func TestSlashCompletionAliasConfiguredCommandCanonicalizes(t *testing.T) {
	cases := []struct {
		configured string
		query      string
		want       string
		display    string
	}{
		{configured: "exit", query: "/e", want: "/quit", display: "/quit (exit)"},
		{configured: "subagent", query: "/s", want: "/team", display: "/team (subagent)"},
	}
	for _, tc := range cases {
		t.Run(tc.configured, func(t *testing.T) {
			model := NewModel(Config{Commands: []string{tc.configured}})
			model.setInputText(tc.query)
			model.refreshSlashCommands()

			if got := model.slashCandidates; !reflect.DeepEqual(got, []string{tc.want}) {
				t.Fatalf("configured %q candidates for %s = %#v, want [%s]", tc.configured, tc.query, got, tc.want)
			}
			if got := model.slashCommandDisplay(tc.want); got != tc.display {
				t.Fatalf("slashCommandDisplay(%s) = %q, want %q", tc.want, got, tc.display)
			}
		})
	}
}

func TestSlashCompletionTeamSelectionOpensOverlay(t *testing.T) {
	for _, key := range []string{"tab", "enter"} {
		t.Run(key, func(t *testing.T) {
			service := &subagentDelegationStub{status: subagentTestStatus()}
			model := NewModel(Config{
				Commands:       DefaultCommands(),
				Wizards:        DefaultWizards(),
				ControlService: service,
				SkillComplete: func(string, int) ([]CompletionCandidate, error) {
					return []CompletionCandidate{
						{Value: "subagent", Display: "subagent", Kind: "Skill", Detail: "shadow team"},
						{Value: "team", Display: "team", Kind: "Skill", Detail: "shadow team"},
					}, nil
				},
				ExecuteLine: func(Submission) TaskResultMsg {
					t.Fatal("team completion reached prompt execution")
					return TaskResultMsg{}
				},
			})
			model.setInputText("/sub")
			model.refreshSlashCommands()
			loadSlashSkillCatalog(t, model)
			if got := model.slashCandidates; !reflect.DeepEqual(got, []string{"/team"}) {
				t.Fatalf("slashCandidates before %s = %#v, want canonical /team", key, got)
			}

			_, cmd := model.handleKey(keyPress(key))
			if cmd == nil || model.subagentOverlay == nil || !model.subagentOverlay.loading {
				t.Fatalf("%s on /team completion = cmd:%v overlay:%#v, want loading team overlay", key, cmd != nil, model.subagentOverlay)
			}
			if msg := cmd(); msg == nil {
				t.Fatal("team completion command returned no binding result")
			} else if _, ok := msg.(subagentOverlayResultMsg); !ok {
				t.Fatalf("team completion message = %T, want subagentOverlayResultMsg", msg)
			}
		})
	}
}

func TestSlashCompletionQuitAliasQuitsLocally(t *testing.T) {
	model := NewModel(Config{
		Commands: []string{"quit", "exit"},
		ExecuteLine: func(Submission) TaskResultMsg {
			t.Fatal("quit alias completion reached prompt execution")
			return TaskResultMsg{}
		},
	})
	model.setInputText("/ex")
	model.refreshSlashCommands()
	if got := model.slashCandidates; !reflect.DeepEqual(got, []string{"/quit"}) {
		t.Fatalf("slashCandidates for /ex = %#v, want canonical /quit", got)
	}

	_, cmd := model.handleKey(keyPress("tab"))
	if cmd == nil || !model.quit || model.liveTurn.Active {
		t.Fatalf("quit alias completion = cmd:%v quit:%v live:%v, want immediate local quit", cmd != nil, model.quit, model.liveTurn.Active)
	}
}

func TestSlashCompletionReservesAliasesWhileRunning(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SkillComplete: func(string, int) ([]CompletionCandidate, error) {
			return []CompletionCandidate{
				{Value: "subagent", Display: "subagent", Kind: "Skill", Detail: "shadow team"},
				{Value: "exit", Display: "exit", Kind: "Skill", Detail: "shadow quit"},
			}, nil
		},
	})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Now())
	model.setInputText("/")
	loadSlashSkillCatalog(t, model)

	counts := map[string]int{}
	for _, candidate := range model.slashCandidates {
		counts[candidate]++
	}
	for _, excluded := range []string{"/team", "/subagent", "/exit"} {
		if counts[excluded] != 0 {
			t.Fatalf("slashCandidates while running = %#v, %s must stay hidden", model.slashCandidates, excluded)
		}
	}
	if counts["/quit"] != 1 {
		t.Fatalf("slashCandidates while running = %#v, want /quit", model.slashCandidates)
	}
}

func TestIsCommandAvailableResolvesAliases(t *testing.T) {
	model := defaultAliasCompletionModel()
	for _, name := range []string{"team", "subagent", "quit", "exit"} {
		if !model.isCommandAvailable(name) {
			t.Fatalf("isCommandAvailable(%q) = false, want alias/canonical to gate against configured canonical", name)
		}
	}

	model = NewModel(Config{Commands: []string{"help"}})
	for _, name := range []string{"team", "subagent", "quit", "exit"} {
		if model.isCommandAvailable(name) {
			t.Fatalf("isCommandAvailable(%q) = true without a configured canonical command", name)
		}
	}
}

func TestSlashCompletionCanonicalBuiltinsOutrankAliasSkills(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SkillComplete: func(string, int) ([]CompletionCandidate, error) {
			return []CompletionCandidate{
				{Value: "subagent", Display: "subagent", Kind: "Skill", Detail: "shadow team"},
				{Value: "exit", Display: "exit", Kind: "Skill", Detail: "shadow quit"},
				{Value: "team", Display: "team", Kind: "Skill", Detail: "shadow team"},
				{Value: "quit", Display: "quit", Kind: "Skill", Detail: "shadow quit"},
				{Value: "lint", Display: "lint", Kind: "Skill", Detail: "keep me"},
			}, nil
		},
	})
	model.setInputText("/")
	loadSlashSkillCatalog(t, model)

	counts := map[string]int{}
	for _, candidate := range model.slashCandidates {
		counts[candidate]++
	}
	for _, want := range []string{"/team", "/quit", "/lint"} {
		if counts[want] != 1 {
			t.Fatalf("slashCandidates = %#v, want exactly one %s", model.slashCandidates, want)
		}
	}
	for _, alias := range []string{"/subagent", "/exit"} {
		if counts[alias] != 0 {
			t.Fatalf("slashCandidates = %#v, alias %s must be absorbed by its canonical built-in", model.slashCandidates, alias)
		}
	}
	if display := model.slashCommandDisplay("/team"); display != "/team (subagent)" {
		t.Fatalf("slashCommandDisplay(/team) = %q, want canonical annotated display", display)
	}
	if detail := model.commandCompletionDetail("/team"); strings.Contains(detail, "shadow") {
		t.Fatalf("commandCompletionDetail(/team) = %q, want built-in description over same-name skill", detail)
	}
}

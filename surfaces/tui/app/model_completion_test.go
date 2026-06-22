package tuiapp

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestModelDeleteSelectionOpensAliasPicker(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "model":
				return []SlashArgCandidate{
					{Value: "use", Display: "use"},
					{Value: "del", Display: "del"},
				}, nil
			case "model use", "model del":
				return []SlashArgCandidate{
					{Value: "minimax/minimax-m1", Display: "minimax/minimax-m1"},
					{Value: "alt-model", Display: "alt-model"},
				}, nil
			default:
				return nil, nil
			}
		},
	})

	model.openSlashArgPicker("model")
	model.slashArgIndex = 1
	model.applySlashArgCompletion()

	if got := string(model.input); got != "/model del " {
		t.Fatalf("input after /model del selection = %q, want %q", got, "/model del ")
	}
	if got := model.slashArgCommand; got != "model del" {
		t.Fatalf("slashArgCommand = %q, want model del", got)
	}
	if len(model.slashArgCandidates) != 2 {
		t.Fatalf("slashArgCandidates count = %d, want 2", len(model.slashArgCandidates))
	}
	if got := model.slashArgCandidates[0].Value; got != "minimax/minimax-m1" {
		t.Fatalf("first alias candidate = %q, want minimax/minimax-m1", got)
	}
}

func TestPluginCommandShowsRootCompletion(t *testing.T) {
	for _, input := range []string{"/plugin", "/plugin "} {
		t.Run(fmt.Sprintf("%q", input), func(t *testing.T) {
			model := NewModel(Config{
				Commands: DefaultCommands(),
				SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
					if command != "plugin" {
						t.Fatalf("SlashArgComplete command = %q, want plugin", command)
					}
					return []SlashArgCandidate{
						{Value: "install", Display: "install"},
						{Value: "manage", Display: "manage"},
						{Value: "rm", Display: "rm"},
					}, nil
				},
			})

			model.setInputText(input)
			model.syncTextareaFromInput()
			model.syncSlashInputOverlays()

			if got := string(model.input); got != input {
				t.Fatalf("input = %q, want %q", got, input)
			}
			if got := model.slashArgCommand; got != "plugin" {
				t.Fatalf("slashArgCommand = %q, want plugin", got)
			}
			got := make([]string, 0, len(model.slashArgCandidates))
			for _, candidate := range model.slashArgCandidates {
				got = append(got, candidate.Value)
			}
			if strings.Join(got, ",") != "install,manage,rm" {
				t.Fatalf("plugin candidates = %#v, want install/manage/rm", model.slashArgCandidates)
			}
		})
	}
}

func TestPluginMarketplaceSelectionOpensNestedPicker(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "plugin":
				return []SlashArgCandidate{{Value: "marketplace", Display: "marketplace"}}, nil
			case "plugin marketplace":
				return []SlashArgCandidate{
					{Value: "add", Display: "add"},
					{Value: "list", Display: "list"},
					{Value: "update", Display: "update"},
					{Value: "rm", Display: "rm"},
				}, nil
			case "plugin marketplace update":
				return []SlashArgCandidate{{Value: "demo-market", Display: "demo-market"}}, nil
			default:
				return nil, nil
			}
		},
	})

	model.openSlashArgPicker("plugin")
	model.applySlashArgCompletion()

	if got := string(model.input); got != "/plugin marketplace " {
		t.Fatalf("input after marketplace selection = %q, want /plugin marketplace ", got)
	}
	if got := model.slashArgCommand; got != "plugin marketplace" {
		t.Fatalf("slashArgCommand = %q, want plugin marketplace", got)
	}
	if got := candidateValuesForTUITest(model.slashArgCandidates); strings.Join(got, ",") != "add,list,update,rm" {
		t.Fatalf("marketplace action candidates = %#v, want add/list/update/rm", model.slashArgCandidates)
	}

	model.slashArgIndex = 2
	model.applySlashArgCompletion()

	if got := string(model.input); got != "/plugin marketplace update " {
		t.Fatalf("input after marketplace update selection = %q, want /plugin marketplace update ", got)
	}
	if got := model.slashArgCommand; got != "plugin marketplace update" {
		t.Fatalf("slashArgCommand = %q, want plugin marketplace update", got)
	}
	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "demo-market" {
		t.Fatalf("marketplace name candidates = %#v, want demo-market", model.slashArgCandidates)
	}
}

func TestPluginMarketplaceInputShowsNestedCompletion(t *testing.T) {
	for _, tt := range []struct {
		input   string
		command string
		value   string
	}{
		{input: "/plugin marketplace ", command: "plugin marketplace", value: "update"},
		{input: "/plugin marketplace update d", command: "plugin marketplace update", value: "demo-market"},
	} {
		t.Run(tt.input, func(t *testing.T) {
			model := NewModel(Config{
				Commands: DefaultCommands(),
				SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
					switch command {
					case "plugin marketplace":
						return []SlashArgCandidate{{Value: "update", Display: "update"}}, nil
					case "plugin marketplace update":
						return []SlashArgCandidate{{Value: "demo-market", Display: "demo-market"}}, nil
					default:
						return nil, nil
					}
				},
			})

			model.setInputText(tt.input)
			model.syncTextareaFromInput()
			model.syncSlashInputOverlays()

			if got := model.slashArgCommand; got != tt.command {
				t.Fatalf("slashArgCommand = %q, want %q", got, tt.command)
			}
			if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != tt.value {
				t.Fatalf("slashArgCandidates = %#v, want %q", model.slashArgCandidates, tt.value)
			}
		})
	}
}

func TestPluginMarketplaceListCompletionSubmitsPickerCommand(t *testing.T) {
	var submitted string
	model := NewModel(Config{
		Commands: DefaultCommands(),
		ExecuteLine: func(submission Submission) TaskResultMsg {
			submitted = submission.Text
			return TaskResultMsg{SuppressTurnDivider: true}
		},
		SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
			if command != "plugin marketplace" {
				return nil, nil
			}
			return []SlashArgCandidate{{Value: "add"}, {Value: "list"}, {Value: "update"}, {Value: "rm"}}, nil
		},
	})

	model.openSlashArgPicker("plugin marketplace")
	model.slashArgIndex = 1
	handled, cmd := model.handleSlashArgKey(keyPress("enter"))
	if !handled {
		t.Fatal("handleSlashArgKey(enter) = false, want true")
	}
	if cmd == nil {
		t.Fatal("handleSlashArgKey(enter) command = nil, want /plugin marketplace list submit command")
	}
	findAndRunTaskResult(cmd(), model)
	if submitted != "/plugin marketplace list" {
		t.Fatalf("submitted input = %q, want /plugin marketplace list", submitted)
	}
}

func candidateValuesForTUITest(candidates []SlashArgCandidate) []string {
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, strings.TrimSpace(candidate.Value))
	}
	return out
}

func TestTryOpenSlashArgPickerUsesCommandRegistry(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
			if command != "plugin" {
				return nil, nil
			}
			return []SlashArgCandidate{{Value: "install"}, {Value: "manage"}, {Value: "rm"}}, nil
		},
	})

	if !model.tryOpenSlashArgPicker("/plugin") {
		t.Fatal("tryOpenSlashArgPicker(/plugin) = false, want true")
	}
	if got := model.slashArgCommand; got != "plugin" {
		t.Fatalf("slashArgCommand = %q, want plugin", got)
	}
}

func TestPluginManageCompletionSubmitsPickerCommand(t *testing.T) {
	var submitted string
	model := NewModel(Config{
		Commands: DefaultCommands(),
		ExecuteLine: func(submission Submission) TaskResultMsg {
			submitted = submission.Text
			return TaskResultMsg{SuppressTurnDivider: true}
		},
		SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
			if command != "plugin" {
				return nil, nil
			}
			return []SlashArgCandidate{{Value: "install"}, {Value: "manage"}, {Value: "rm"}}, nil
		},
	})

	model.openSlashArgPicker("plugin")
	model.slashArgIndex = 1
	handled, cmd := model.handleSlashArgKey(keyPress("enter"))
	if !handled {
		t.Fatal("handleSlashArgKey(enter) = false, want true")
	}
	if cmd == nil {
		t.Fatal("handleSlashArgKey(enter) command = nil, want /plugin manage submit command")
	}
	findAndRunTaskResult(cmd(), model)
	if submitted != "/plugin manage" {
		t.Fatalf("submitted input = %q, want /plugin manage", submitted)
	}
}

func TestModelUseSelectionOpensReasoningPicker(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "model":
				return []SlashArgCandidate{{Value: "use", Display: "use"}}, nil
			case "model use":
				return []SlashArgCandidate{{Value: "deepseek/deepseek-v4-pro", Display: "deepseek/deepseek-v4-pro"}}, nil
			case "model use deepseek/deepseek-v4-pro":
				return []SlashArgCandidate{{Value: "none", Display: "none"}, {Value: "high", Display: "high"}}, nil
			default:
				return nil, nil
			}
		},
	})

	model.openSlashArgPicker("model")
	model.applySlashArgCompletion()
	if got := string(model.input); got != "/model use " {
		t.Fatalf("input after model action = %q, want /model use ", got)
	}
	model.applySlashArgCompletion()
	if got := string(model.input); got != "/model use deepseek/deepseek-v4-pro " {
		t.Fatalf("input after model alias = %q, want alias plus trailing space", got)
	}
	if got := model.slashArgCommand; got != "model use deepseek/deepseek-v4-pro" {
		t.Fatalf("slashArgCommand = %q, want reasoning picker command", got)
	}
	if len(model.slashArgCandidates) != 2 || model.slashArgCandidates[1].Value != "high" {
		t.Fatalf("reasoning candidates = %#v, want none/high", model.slashArgCandidates)
	}
	model.slashArgIndex = 1
	model.applySlashArgCompletion()
	if got := string(model.input); got != "/model use deepseek/deepseek-v4-pro high" {
		t.Fatalf("input after reasoning selection = %q, want high reasoning", got)
	}
}

func TestModelUseExactAliasInputOpensReasoningPicker(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "model use":
				return []SlashArgCandidate{{Value: "gpt-5.5", Display: "GPT-5.5"}}, nil
			case "model use gpt-5.5":
				return []SlashArgCandidate{{Value: "high", Display: "High"}, {Value: "xhigh", Display: "Xhigh"}}, nil
			default:
				return nil, nil
			}
		},
	})

	model.setInputText("/model use gpt-5.5")
	model.syncTextareaFromInput()
	model.syncSlashInputOverlays()

	if got := model.slashArgCommand; got != "model use gpt-5.5" {
		t.Fatalf("slashArgCommand = %q, want reasoning picker command", got)
	}
	if len(model.slashArgCandidates) != 2 || model.slashArgCandidates[1].Value != "xhigh" {
		t.Fatalf("reasoning candidates = %#v, want high/xhigh", model.slashArgCandidates)
	}
}

func TestSubagentBindSelectionOpensNestedPickers(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "subagent":
				return []SlashArgCandidate{
					{Value: "list", Display: "list"},
					{Value: "run", Display: "run"},
					{Value: "bind", Display: "bind"},
				}, nil
			case "subagent bind":
				return []SlashArgCandidate{{Value: "guardian", Display: "guardian"}}, nil
			case "subagent bind guardian":
				return []SlashArgCandidate{
					{Value: "default", Display: "default"},
					{Value: "model", Display: "model"},
				}, nil
			case "subagent bind guardian model":
				return []SlashArgCandidate{{Value: "guardian-model", Display: "guardian-model"}}, nil
			case "subagent bind guardian model guardian-model":
				return []SlashArgCandidate{
					{Value: "none", Display: "none"},
					{Value: "high", Display: "high"},
				}, nil
			default:
				return nil, nil
			}
		},
	})

	model.openSlashArgPicker("subagent")
	model.slashArgIndex = 2
	model.applySlashArgCompletion()
	if got := string(model.input); got != "/subagent bind " {
		t.Fatalf("input after bind action = %q, want /subagent bind ", got)
	}
	if got := model.slashArgCommand; got != "subagent bind" {
		t.Fatalf("slashArgCommand = %q, want subagent bind", got)
	}

	model.applySlashArgCompletion()
	if got := string(model.input); got != "/subagent bind guardian " {
		t.Fatalf("input after guardian profile = %q, want /subagent bind guardian ", got)
	}
	if got := model.slashArgCommand; got != "subagent bind guardian" {
		t.Fatalf("slashArgCommand = %q, want subagent bind guardian", got)
	}

	model.slashArgIndex = 1
	model.applySlashArgCompletion()
	if got := string(model.input); got != "/subagent bind guardian model " {
		t.Fatalf("input after model target = %q, want /subagent bind guardian model ", got)
	}
	if got := model.slashArgCommand; got != "subagent bind guardian model" {
		t.Fatalf("slashArgCommand = %q, want model alias picker", got)
	}

	model.applySlashArgCompletion()
	if got := string(model.input); got != "/subagent bind guardian model guardian-model " {
		t.Fatalf("input after model alias = %q, want alias plus trailing space", got)
	}
	if got := model.slashArgCommand; got != "subagent bind guardian model guardian-model" {
		t.Fatalf("slashArgCommand = %q, want reasoning picker command", got)
	}

	model.slashArgIndex = 1
	model.applySlashArgCompletion()
	if got := string(model.input); got != "/subagent bind guardian model guardian-model high" {
		t.Fatalf("input after reasoning = %q, want high reasoning", got)
	}
	if model.slashArgActive {
		t.Fatal("slash arg picker should close after final reasoning selection")
	}
}

func TestSubagentSlashTypingActivatesNestedCompletion(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "subagent":
				return []SlashArgCandidate{{Value: "bind", Display: "bind"}}, nil
			case "subagent bind":
				return []SlashArgCandidate{{Value: "guardian", Display: "guardian"}}, nil
			case "subagent bind guardian":
				return []SlashArgCandidate{{Value: "model", Display: "model"}}, nil
			case "subagent bind guardian model":
				return []SlashArgCandidate{{Value: "guardian-model", Display: "guardian-model"}}, nil
			case "subagent bind guardian model guardian-model":
				return []SlashArgCandidate{{Value: "high", Display: "high"}}, nil
			default:
				return nil, nil
			}
		},
	})

	tests := []struct {
		input   string
		command string
		value   string
	}{
		{input: "/subagent bi", command: "subagent", value: "bind"},
		{input: "/subagent bind gu", command: "subagent bind", value: "guardian"},
		{input: "/subagent bind guardian mo", command: "subagent bind guardian", value: "model"},
		{input: "/subagent bind guardian model guardian", command: "subagent bind guardian model", value: "guardian-model"},
		{input: "/subagent bind guardian model guardian-model h", command: "subagent bind guardian model guardian-model", value: "high"},
	}
	for _, tt := range tests {
		model.setInputText(tt.input)
		model.syncTextareaFromInput()
		model.syncSlashInputOverlays()
		if got := model.slashArgCommand; got != tt.command {
			t.Fatalf("slashArgCommand for %q = %q, want %q", tt.input, got, tt.command)
		}
		if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != tt.value {
			t.Fatalf("slashArgCandidates for %q = %#v, want %q", tt.input, model.slashArgCandidates, tt.value)
		}
	}
}

func TestSubagentBindFinalCandidateEnterSubmitsCompletedInput(t *testing.T) {
	var submitted string
	model := NewModel(Config{
		Commands: DefaultCommands(),
		ExecuteLine: func(submission Submission) TaskResultMsg {
			submitted = submission.Text
			return TaskResultMsg{SuppressTurnDivider: true}
		},
		SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "subagent bind guardian":
				return []SlashArgCandidate{{Value: "default", Display: "default"}}, nil
			default:
				return nil, nil
			}
		},
	})
	model.setInputText("/subagent bind guardian ")
	model.syncTextareaFromInput()
	model.syncSlashInputOverlays()
	if got := model.slashArgCommand; got != "subagent bind guardian" {
		t.Fatalf("slashArgCommand = %q, want subagent bind guardian", got)
	}
	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "default" {
		t.Fatalf("slashArgCandidates = %#v, want default", model.slashArgCandidates)
	}

	handled, cmd := model.handleSlashArgKey(keyPress("enter"))
	if !handled {
		t.Fatal("handleSlashArgKey(enter) = false, want true")
	}
	if cmd == nil {
		t.Fatalf("handleSlashArgKey(enter) command = nil, want submit command; input=%q active=%v command=%q candidates=%#v running=%v executeLineNil=%v", model.textarea.Value(), model.slashArgActive, model.slashArgCommand, model.slashArgCandidates, model.running, model.cfg.ExecuteLine == nil)
	}
	findAndRunTaskResult(cmd(), model)
	if submitted != "/subagent bind guardian default" {
		t.Fatalf("submitted input = %q, want /subagent bind guardian default", submitted)
	}
}

func TestDestructiveSlashArgEnterCompletesBeforeSubmit(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		input     string
		candidate string
		wantInput string
	}{
		{
			name:      "model delete",
			command:   "model del",
			input:     "/model del ",
			candidate: "stale-model",
			wantInput: "/model del stale-model ",
		},
		{
			name:      "agent remove",
			command:   "agent remove",
			input:     "/agent remove ",
			candidate: "copilot",
			wantInput: "/agent remove copilot",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var submitted []string
			model := NewModel(Config{
				Commands: DefaultCommands(),
				ExecuteLine: func(submission Submission) TaskResultMsg {
					submitted = append(submitted, submission.Text)
					return TaskResultMsg{SuppressTurnDivider: true}
				},
				SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
					if command != tt.command {
						return nil, nil
					}
					return []SlashArgCandidate{{Value: tt.candidate, Display: tt.candidate}}, nil
				},
			})

			model.setInputText(tt.input)
			model.syncTextareaFromInput()
			model.syncSlashInputOverlays()
			handled, cmd := model.handleSlashArgKey(keyPress("enter"))
			if !handled {
				t.Fatal("handleSlashArgKey(enter) = false, want true")
			}
			if cmd != nil {
				t.Fatal("handleSlashArgKey(empty final arg) returned submit command, want completion only")
			}
			if len(submitted) != 0 {
				t.Fatalf("submitted = %#v, want no submission", submitted)
			}
			if got := string(model.input); got != tt.wantInput {
				t.Fatalf("input after completion = %q, want %q", got, tt.wantInput)
			}

			model.setInputText("/" + tt.command + " " + tt.candidate)
			model.syncTextareaFromInput()
			model.syncSlashInputOverlays()
			handled, cmd = model.handleSlashArgKey(keyPress("enter"))
			if !handled {
				t.Fatal("handleSlashArgKey(exact enter) = false, want true")
			}
			if cmd == nil {
				t.Fatal("handleSlashArgKey(exact enter) command = nil, want submit command")
			}
			findAndRunTaskResult(cmd(), model)
			wantSubmitted := "/" + tt.command + " " + tt.candidate
			if len(submitted) != 1 || submitted[0] != wantSubmitted {
				t.Fatalf("submitted = %#v, want [%q]", submitted, wantSubmitted)
			}
		})
	}
}

func TestSlashCommandSelectionMovesWithArrowKeys(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
	})
	model.setInputText("/")
	model.refreshSlashCommands()
	if len(model.slashCandidates) < 2 {
		t.Fatalf("slashCandidates = %#v, want at least 2", model.slashCandidates)
	}
	if model.slashIndex != 0 {
		t.Fatalf("initial slashIndex = %d, want 0", model.slashIndex)
	}
	handled, _ := model.handleSlashCommandKey(keyPress("down"))
	if !handled {
		t.Fatal("handleSlashCommandKey(down) = false, want true")
	}
	if model.slashIndex != 1 {
		t.Fatalf("slashIndex after down = %d, want 1", model.slashIndex)
	}
}

func TestSlashCommandCompletionRefreshesBeforeAcceptingStaleCandidates(t *testing.T) {
	model := NewModel(Config{
		Commands: []string{"agent", "doctor"},
	})

	_, cmd := model.handleKey(keyPress("/"))
	runCompletionCmd(t, model, cmd)
	if got := model.slashCandidates; len(got) != 2 || got[0] != "/agent" {
		t.Fatalf("slashCandidates after / = %#v, want stale list starting with /agent", got)
	}
	_, cmd = model.handleKey(keyPress("do"))
	if cmd == nil {
		t.Fatal("handleKey(do) should schedule a debounced completion refresh")
	}

	_, cmd = model.handleKey(keyPress("tab"))
	runCompletionCmd(t, model, cmd)
	if got := string(model.input); got != "/doctor " {
		t.Fatalf("input after /do<Tab> = %q, want /doctor ", got)
	}
}

func TestSlashCommandCompletionOpensSubagentArgPicker(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
			if command != "subagent" {
				return nil, nil
			}
			return []SlashArgCandidate{
				{Value: "list", Display: "list"},
				{Value: "run", Display: "run"},
				{Value: "bind", Display: "bind"},
			}, nil
		},
	})

	model.setInputText("/sub")
	model.syncTextareaFromInput()
	model.refreshSlashCommands()
	if len(model.slashCandidates) != 1 || model.slashCandidates[0] != "/subagent" {
		t.Fatalf("slashCandidates = %#v, want only /subagent", model.slashCandidates)
	}
	model.applySlashCommandCompletion()
	model.syncTextareaFromInput()

	if got := string(model.input); got != "/subagent " {
		t.Fatalf("input after /sub<Tab> = %q, want /subagent ", got)
	}
	if got := model.slashArgCommand; got != "subagent" {
		t.Fatalf("slashArgCommand = %q, want subagent", got)
	}
	if len(model.slashArgCandidates) != 3 || model.slashArgCandidates[0].Value != "list" {
		t.Fatalf("slashArgCandidates = %#v, want list/run/bind", model.slashArgCandidates)
	}
}

func TestSlashCommandTabKeepsArrowSelectedCandidateAcrossRefresh(t *testing.T) {
	model := NewModel(Config{
		Commands: []string{"agent", "doctor"},
	})
	model.setInputText("/")
	model.syncTextareaFromInput()
	model.refreshSlashCommands()
	if got := model.slashCandidates; len(got) != 2 || got[0] != "/agent" || got[1] != "/doctor" {
		t.Fatalf("slashCandidates = %#v, want /agent then /doctor", got)
	}

	handled, _ := model.handleSlashCommandKey(keyPress("down"))
	if !handled {
		t.Fatal("handleSlashCommandKey(down) = false, want true")
	}
	if model.slashIndex != 1 {
		t.Fatalf("slashIndex after down = %d, want 1", model.slashIndex)
	}

	_, cmd := model.handleKey(keyPress("tab"))
	runCompletionCmd(t, model, cmd)
	if got := string(model.input); got != "/doctor " {
		t.Fatalf("input after selecting /doctor then <Tab> = %q, want /doctor ", got)
	}
}

func TestModelActionPrefixTypingOpensMatchingAliasPicker(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "model":
				return []SlashArgCandidate{
					{Value: "use", Display: "use"},
					{Value: "del", Display: "del"},
				}, nil
			case "model del":
				return []SlashArgCandidate{
					{Value: "minimax/minimax-m2.7-highspeed", Display: "minimax/minimax-m2.7-highspeed"},
				}, nil
			default:
				return nil, nil
			}
		},
	})

	model.setInputText("/model de")
	model.syncTextareaFromInput()
	model.syncSlashInputOverlays()

	if got := model.slashArgCommand; got != "model" {
		t.Fatalf("slashArgCommand = %q, want model", got)
	}
	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "del" {
		t.Fatalf("slashArgCandidates = %#v, want only del candidate", model.slashArgCandidates)
	}

	handled, cmd := model.handleSlashArgKey(keyPress("enter"))
	if !handled {
		t.Fatal("handleSlashArgKey(enter) = false, want true")
	}
	if cmd != nil {
		cmd()
	}
	if got := string(model.input); got != "/model del " {
		t.Fatalf("input after /model de enter = %q, want /model del ", got)
	}
	if got := model.slashArgCommand; got != "model del" {
		t.Fatalf("slashArgCommand after /model de enter = %q, want model del", got)
	}
}

func TestModelActionPrefixTypingFiltersCandidatesWhenCursorLags(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "model":
				return []SlashArgCandidate{
					{Value: "use", Display: "use"},
					{Value: "del", Display: "del"},
				}, nil
			default:
				return nil, nil
			}
		},
	})

	model.setInputText("/model de")
	model.syncTextareaFromInput()
	model.cursor = len([]rune("/model "))
	model.syncSlashInputOverlays()

	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "del" {
		t.Fatalf("slashArgCandidates with lagging cursor = %#v, want only del candidate", model.slashArgCandidates)
	}
}

func TestModelActionPrefixTypingResetsSelectionToFirstFilteredCandidate(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "model":
				return []SlashArgCandidate{
					{Value: "use", Display: "use"},
					{Value: "del", Display: "del"},
				}, nil
			default:
				return nil, nil
			}
		},
	})

	model.openSlashArgPicker("model")
	model.slashArgIndex = 1
	model.setInputText("/model us")
	model.syncTextareaFromInput()
	model.syncSlashInputOverlays()
	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "use" {
		t.Fatalf("slashArgCandidates after /model us = %#v, want only use", model.slashArgCandidates)
	}
	if model.currentSlashArgIndex(model.slashArgCandidates) != 0 {
		t.Fatalf("currentSlashArgIndex after /model us = %d, want 0", model.currentSlashArgIndex(model.slashArgCandidates))
	}

	model.slashArgIndex = 0
	model.setInputText("/model de")
	model.syncTextareaFromInput()
	model.syncSlashInputOverlays()
	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "del" {
		t.Fatalf("slashArgCandidates after /model de = %#v, want only del", model.slashArgCandidates)
	}
	if model.currentSlashArgIndex(model.slashArgCandidates) != 0 {
		t.Fatalf("currentSlashArgIndex after /model de = %d, want 0", model.currentSlashArgIndex(model.slashArgCandidates))
	}
}

func TestResumePrefixTypingFiltersCandidates(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		ResumeComplete: func(query string, limit int) ([]ResumeCandidate, error) {
			return []ResumeCandidate{
				{SessionID: "alpha-session", Prompt: "work on gateway", Age: "1m"},
				{SessionID: "beta-session", Prompt: "resume model work", Age: "2m"},
			}, nil
		},
	})

	model.setInputText("/resume be")
	model.syncTextareaFromInput()
	model.syncSlashInputOverlays()

	if !model.resumeActive {
		t.Fatal("resume picker not activated")
	}
	if len(model.resumeCandidates) != 1 || model.resumeCandidates[0].SessionID != "beta-session" {
		t.Fatalf("resumeCandidates = %#v, want only beta-session", model.resumeCandidates)
	}
}

func TestResumePrefixTypingFiltersCandidatesWhenCursorLags(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		ResumeComplete: func(query string, limit int) ([]ResumeCandidate, error) {
			return []ResumeCandidate{
				{SessionID: "alpha-session", Prompt: "work on gateway", Age: "1m"},
				{SessionID: "beta-session", Prompt: "resume model work", Age: "2m"},
			}, nil
		},
	})

	model.setInputText("/resume be")
	model.syncTextareaFromInput()
	model.cursor = len([]rune("/resume "))
	model.syncSlashInputOverlays()

	if !model.resumeActive {
		t.Fatal("resume picker not activated")
	}
	if len(model.resumeCandidates) != 1 || model.resumeCandidates[0].SessionID != "beta-session" {
		t.Fatalf("resumeCandidates with lagging cursor = %#v, want only beta-session", model.resumeCandidates)
	}
}

func TestResumePrefixTypingResetsSelectionToFirstFilteredCandidate(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		ResumeComplete: func(query string, limit int) ([]ResumeCandidate, error) {
			return []ResumeCandidate{
				{SessionID: "alpha-session", Prompt: "work on gateway", Age: "1m"},
				{SessionID: "beta-session", Prompt: "resume model work", Age: "2m"},
			}, nil
		},
	})

	model.activateResumePickerFromInput()
	model.resumeIndex = 1
	model.setInputText("/resume al")
	model.syncTextareaFromInput()
	model.syncSlashInputOverlays()

	if len(model.resumeCandidates) != 1 || model.resumeCandidates[0].SessionID != "alpha-session" {
		t.Fatalf("resumeCandidates after /resume al = %#v, want only alpha-session", model.resumeCandidates)
	}
	if model.resumeIndex != 0 {
		t.Fatalf("resumeIndex after query change = %d, want 0", model.resumeIndex)
	}
}

func TestAgentActionPrefixTypingFiltersCandidates(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "agent":
				return []SlashArgCandidate{
					{Value: "add", Display: "add"},
					{Value: "remove", Display: "remove"},
					{Value: "use", Display: "use"},
					{Value: "list", Display: "list"},
				}, nil
			default:
				return nil, nil
			}
		},
	})

	model.setInputText("/agent us")
	model.syncTextareaFromInput()
	model.syncSlashInputOverlays()

	if got := model.slashArgCommand; got != "agent" {
		t.Fatalf("slashArgCommand = %q, want agent", got)
	}
	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "use" {
		t.Fatalf("slashArgCandidates = %#v, want only use candidate", model.slashArgCandidates)
	}
}

func TestAgentActionPrefixTypingFiltersCandidatesWhenCursorLags(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "agent":
				return []SlashArgCandidate{
					{Value: "add", Display: "add"},
					{Value: "remove", Display: "remove"},
					{Value: "use", Display: "use"},
					{Value: "list", Display: "list"},
				}, nil
			default:
				return nil, nil
			}
		},
	})

	model.setInputText("/agent us")
	model.syncTextareaFromInput()
	model.cursor = len([]rune("/agent "))
	model.syncSlashInputOverlays()

	if got := model.slashArgCommand; got != "agent" {
		t.Fatalf("slashArgCommand = %q, want agent", got)
	}
	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "use" {
		t.Fatalf("slashArgCandidates with lagging cursor = %#v, want only use candidate", model.slashArgCandidates)
	}
}

func TestModelActionPrefixTypingFiltersCandidatesDuringLiveInput(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "model":
				return []SlashArgCandidate{
					{Value: "use", Display: "use"},
					{Value: "del", Display: "del"},
				}, nil
			default:
				return nil, nil
			}
		},
	})

	for _, ch := range []string{"/", "m", "o", "d", "e", "l", " ", "d", "e"} {
		var cmd tea.Cmd
		_, cmd = model.handleKey(keyPress(ch))
		runCompletionCmd(t, model, cmd)
	}

	if got := string(model.input); got != "/model de" {
		t.Fatalf("input = %q, want /model de", got)
	}
	if got := model.slashArgCommand; got != "model" {
		t.Fatalf("slashArgCommand = %q, want model", got)
	}
	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "del" {
		t.Fatalf("slashArgCandidates after live input = %#v, want only del candidate", model.slashArgCandidates)
	}
}

func TestCompletionRefreshDoesNotBlockTypingPath(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
			time.Sleep(200 * time.Millisecond)
			return []SlashArgCandidate{{Value: "del", Display: "del"}}, nil
		},
	})

	start := time.Now()
	_, cmd := model.handleKey(keyPress("/model de"))
	elapsed := time.Since(start)
	if elapsed > 30*time.Millisecond {
		t.Fatalf("handleKey() took %s, want non-blocking completion path", elapsed)
	}
	if cmd == nil {
		t.Fatal("handleKey() should schedule async completion refresh")
	}
}

func TestModelActionPrefixTypingUsesTextareaValueAsSourceOfTruth(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "model":
				return []SlashArgCandidate{
					{Value: "use", Display: "use"},
					{Value: "del", Display: "del"},
				}, nil
			default:
				return nil, nil
			}
		},
	})

	model.input = []rune("/model ")
	model.cursor = len(model.input)
	model.textarea.SetValue("/model de")
	model.textarea.CursorEnd()
	model.syncSlashInputOverlays()

	if got := model.slashArgCommand; got != "model" {
		t.Fatalf("slashArgCommand = %q, want model", got)
	}
	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "del" {
		t.Fatalf("slashArgCandidates from textarea source = %#v, want only del candidate", model.slashArgCandidates)
	}
}

func TestResumePrefixTypingUsesTextareaValueAsSourceOfTruth(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		ResumeComplete: func(query string, limit int) ([]ResumeCandidate, error) {
			return []ResumeCandidate{
				{SessionID: "alpha-session", Prompt: "work on gateway", Age: "1m"},
				{SessionID: "beta-session", Prompt: "resume model work", Age: "2m"},
			}, nil
		},
	})

	model.input = []rune("/resume ")
	model.cursor = len(model.input)
	model.textarea.SetValue("/resume be")
	model.textarea.CursorEnd()
	model.syncSlashInputOverlays()

	if !model.resumeActive {
		t.Fatal("resume picker not activated")
	}
	if len(model.resumeCandidates) != 1 || model.resumeCandidates[0].SessionID != "beta-session" {
		t.Fatalf("resumeCandidates from textarea source = %#v, want only beta-session", model.resumeCandidates)
	}
}

func TestSkillCompletionRendersMetadataAndUsesCandidateValue(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SkillComplete: func(query string, limit int) ([]CompletionCandidate, error) {
			return []CompletionCandidate{
				{Value: "lint", Display: "lint", Detail: "Run lint checks · ~/.agents/skills/lint/SKILL.md"},
			}, nil
		},
	})

	model.input = []rune("$li")
	model.cursor = len(model.input)
	model.refreshSkill()
	if len(model.skillCandidates) != 1 {
		t.Fatalf("skillCandidates = %#v, want one candidate", model.skillCandidates)
	}
	if !strings.Contains(model.renderSkillList(), "Run lint checks") {
		t.Fatalf("renderSkillList() = %q, want detail text", model.renderSkillList())
	}
	model.applySkillCompletion()
	if got := string(model.input); got != "$lint " {
		t.Fatalf("input after skill completion = %q, want $lint ", got)
	}
}

func TestSkillCompletionFetchesBeyondVisibleWindowAndScrolls(t *testing.T) {
	var gotLimit int
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SkillComplete: func(query string, limit int) ([]CompletionCandidate, error) {
			gotLimit = limit
			return numberedCompletionCandidates("skill", 12), nil
		},
	})

	model.setInputText("$")
	model.syncTextareaFromInput()
	model.refreshSkill()
	if gotLimit != completionCandidateFetchLimit {
		t.Fatalf("SkillComplete limit = %d, want %d", gotLimit, completionCandidateFetchLimit)
	}
	if len(model.skillCandidates) != 12 {
		t.Fatalf("skillCandidates = %d, want 12", len(model.skillCandidates))
	}
	for i := 0; i < 10; i++ {
		handled, _ := model.handleSkillKey(keyPress("down"))
		if !handled {
			t.Fatal("handleSkillKey(down) = false, want true")
		}
	}

	rendered := ansi.Strip(model.renderSkillList())
	if !strings.Contains(rendered, "skill-10") {
		t.Fatalf("renderSkillList() = %q, want selected skill-10 visible", rendered)
	}
	if strings.Contains(rendered, "skill-00") {
		t.Fatalf("renderSkillList() = %q, should have scrolled past skill-00", rendered)
	}
	if !strings.Contains(rendered, "earlier") {
		t.Fatalf("renderSkillList() = %q, want earlier-page indicator", rendered)
	}
}

func TestSkillCompletionLoadsNextPageAtBottomThenWrapsWhenExhausted(t *testing.T) {
	var limits []int
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SkillComplete: func(query string, limit int) ([]CompletionCandidate, error) {
			limits = append(limits, limit)
			return numberedCompletionCandidates("skill", minInt(limit, 65)), nil
		},
	})

	model.setInputText("$")
	model.syncTextareaFromInput()
	model.refreshSkill()
	if len(model.skillCandidates) != completionCandidateFetchLimit {
		t.Fatalf("skillCandidates = %d, want initial page of %d", len(model.skillCandidates), completionCandidateFetchLimit)
	}

	model.skillIndex = len(model.skillCandidates) - 1
	handled, _ := model.handleSkillKey(keyPress("down"))
	if !handled {
		t.Fatal("handleSkillKey(down) = false, want true")
	}
	if want := completionCandidateFetchLimit * 2; limits[len(limits)-1] != want {
		t.Fatalf("SkillComplete second limit = %d, want %d", limits[len(limits)-1], want)
	}
	if len(model.skillCandidates) != 65 {
		t.Fatalf("skillCandidates after paging = %d, want 65", len(model.skillCandidates))
	}
	if model.skillIndex != completionCandidateFetchLimit {
		t.Fatalf("skillIndex after paging = %d, want %d", model.skillIndex, completionCandidateFetchLimit)
	}

	callCount := len(limits)
	model.skillIndex = len(model.skillCandidates) - 1
	handled, _ = model.handleSkillKey(keyPress("down"))
	if !handled {
		t.Fatal("handleSkillKey(down exhausted) = false, want true")
	}
	if len(limits) != callCount {
		t.Fatalf("SkillComplete called after exhausted page: %v", limits)
	}
	if model.skillIndex != 0 {
		t.Fatalf("skillIndex after exhausted down = %d, want wrap to 0", model.skillIndex)
	}
}

func TestSkillCompletionListKeepsRowsCompact(t *testing.T) {
	model := NewModel(Config{})
	longDetail := "This skill should be used when the user asks to generate a very detailed report with many phases and validation requirements. · ~/.agents/skills/report/SKILL.md"
	model.skillCandidates = []CompletionCandidate{
		{Value: "story-init", Display: "story-init", Detail: "Start a new story project. · ~/.agents/skills/story-init/SKILL.md"},
		{Value: "report-builder", Display: "report-builder", Detail: longDetail},
	}
	model.skillIndex = 0

	rendered := model.renderSkillList()
	plain := ansi.Strip(rendered)
	if strings.Contains(plain, "$report-builder") || strings.Contains(plain, "$story-init") {
		t.Fatalf("renderSkillList() = %q, should not show $ prefixes in skill rows", plain)
	}
	if !strings.Contains(rendered, model.theme.CommandStyle().Render("report-builder")) {
		t.Fatalf("renderSkillList() = %q, want non-selected skill name rendered with command style", rendered)
	}
	if strings.Contains(plain, longDetail) {
		t.Fatalf("renderSkillList() = %q, non-selected detail should be truncated", plain)
	}
	if strings.Contains(plain, "SKILL.md") {
		t.Fatalf("renderSkillList() = %q, should not show skill file paths in picker rows", plain)
	}
}

func TestSkillCompletionListKeepsHeightStableAcrossSelection(t *testing.T) {
	model := NewModel(Config{})
	fullDetail := "Assist writers with story planning, character development, plot structuring, chapter writing, timeline tracking, and consistency checking. · ~/.agents/skills/storyboard-manager/SKILL.md"
	model.skillCandidates = []CompletionCandidate{
		{Value: "story-init", Display: "story-init", Detail: "Start a new story project."},
		{Value: "storyboard-manager", Display: "storyboard-manager", Detail: fullDetail},
	}
	model.skillIndex = 0
	first := strings.Count(model.renderSkillList(), "\n")
	model.skillIndex = 1
	secondRendered := model.renderSkillList()
	second := strings.Count(secondRendered, "\n")

	plain := ansi.Strip(secondRendered)

	if first != second {
		t.Fatalf("renderSkillList() line count changed across selection: %d != %d", first, second)
	}
	if !strings.Contains(plain, "Assist writer") {
		t.Fatalf("renderSkillList() = %q, want selected skill short description", plain)
	}
	if strings.Contains(plain, "consistency checking.") {
		t.Fatalf("renderSkillList() = %q, selected detail should stay truncated", plain)
	}
	if strings.Contains(plain, "SKILL.md") {
		t.Fatalf("renderSkillList() = %q, selected detail should not include path metadata", plain)
	}
}

func TestInputCompletionSelectionsAvoidFocusAccent(t *testing.T) {
	model := NewModel(Config{Commands: []string{"help", "model"}})
	applyCompletionAccentTestTheme(model)
	cases := map[string]string{}

	model.slashCandidates = []string{"/help", "/model"}
	model.slashIndex = 1
	cases["slash"] = model.renderSlashCommandList()

	model.mentionPrefix = "@"
	model.mentionCandidates = []CompletionCandidate{
		{Value: "agent-a", Display: "agent-a", Detail: "Agent A"},
	}
	model.mentionIndex = 0
	cases["mention"] = model.renderMentionList()

	model.mentionPrefix = "#"
	model.mentionCandidates = []CompletionCandidate{
		{Value: "docs/readme.md", Display: "docs/readme.md", Detail: "file"},
	}
	model.mentionIndex = 0
	cases["file"] = model.renderMentionList()

	model.skillCandidates = []CompletionCandidate{
		{Value: "story-init", Display: "story-init", Detail: "Start a story project."},
	}
	model.skillIndex = 0
	cases["skill"] = model.renderSkillList()

	model.slashArgCandidates = []SlashArgCandidate{
		{Value: "use", Display: "use", Detail: "switch active model"},
	}
	model.slashArgIndex = 0
	model.slashArgActive = true
	model.slashArgCommand = "model"
	cases["slash-arg"] = model.renderSlashArgList()

	model.resumeCandidates = []ResumeCandidate{
		{SessionID: "session-1", Title: "Previous turn", Model: "gpt-test", Age: "1h"},
	}
	model.resumeIndex = 0
	cases["resume"] = model.renderResumeList()

	for name, rendered := range cases {
		assertNoCompletionAccent(t, name, rendered)
	}
}

func TestPromptAndPaletteSelectionsAvoidFocusAccent(t *testing.T) {
	model := NewModel(Config{Commands: []string{"help", "model"}})
	applyCompletionAccentTestTheme(model)
	model.activePrompt = newPromptState(PromptRequestMsg{
		Title: "Choose",
		Choices: []PromptChoice{
			{Label: "Allow", Value: "allow", Detail: "run once"},
			{Label: "Deny", Value: "deny", Detail: "skip"},
		},
		Response: make(chan PromptResponse, 1),
	})
	model.activePrompt.choiceIndex = 0
	assertNoCompletionAccent(t, "prompt", model.renderPromptModal())

	delegate := list.NewDefaultDelegate()
	configurePaletteDelegateStyles(&delegate, model.theme)
	palette := list.New([]list.Item{commandItem{name: "help"}, commandItem{name: "model"}}, delegate, 40, 6)
	palette.SetShowHelp(false)
	palette.SetShowStatusBar(false)
	palette.Styles.Title = model.theme.TitleStyle()
	assertNoCompletionAccent(t, "palette", model.theme.ModalStyle().Render(palette.View()))
}

func TestApplyThemeRefreshesPaletteDelegateStyles(t *testing.T) {
	model := NewModel(Config{Commands: []string{"help", "model"}})
	model.palette.SetSize(40, 6)

	oldTheme := model.theme
	oldTheme.SelectionFg = lipgloss.Color("#f5f7fb")
	oldTheme.SelectionBg = lipgloss.Color("#112233")
	oldTheme.InvalidateTokens()
	model.applyTheme(oldTheme)
	if rendered := model.palette.View(); !strings.Contains(rendered, "48;2;17;34;51") {
		t.Fatalf("palette view = %q, want old selection background before theme switch", rendered)
	}

	newTheme := oldTheme
	newTheme.SelectionBg = lipgloss.Color("#445566")
	newTheme.InvalidateTokens()
	model.applyTheme(newTheme)
	rendered := model.palette.View()
	if strings.Contains(rendered, "48;2;17;34;51") {
		t.Fatalf("palette view = %q, still uses stale selection background after theme switch", rendered)
	}
	if !strings.Contains(rendered, "48;2;68;85;102") {
		t.Fatalf("palette view = %q, want refreshed selection background after theme switch", rendered)
	}
}

func applyCompletionAccentTestTheme(model *Model) {
	if model == nil {
		return
	}
	focus := lipgloss.Color("#123456")
	model.theme.Focus = focus
	model.theme.PromptFg = focus
	model.theme.ComposerBorderFocus = focus
	model.theme.InvalidateTokens()
}

func assertNoCompletionAccent(t *testing.T, name string, rendered string) {
	t.Helper()
	const focusFragment = "38;2;18;52;86"
	if strings.Contains(rendered, focusFragment) {
		t.Fatalf("%s render still uses focus accent color: %q", name, rendered)
	}
}

func TestFileCompletionAcceptPreservesSelectedCandidateAcrossRefresh(t *testing.T) {
	for _, keyName := range []string{"tab", "enter"} {
		t.Run(keyName, func(t *testing.T) {
			model := NewModel(Config{
				MentionComplete: func(query string, limit int) ([]CompletionCandidate, error) {
					return nil, nil
				},
				FileComplete: func(query string, limit int) ([]CompletionCandidate, error) {
					return []CompletionCandidate{
						{Value: "docs/", Display: "docs/", Detail: "directory"},
						{Value: "docs/message.sql", Display: "docs/message.sql", Detail: "file"},
					}, nil
				},
			})

			model.setInputText("#docs/")
			model.syncTextareaFromInput()
			model.refreshMention()
			if len(model.mentionCandidates) != 2 {
				t.Fatalf("mentionCandidates = %#v, want two file candidates", model.mentionCandidates)
			}
			model.mentionIndex = 1

			_, cmd := model.handleKey(keyPress(keyName))
			runCompletionCmd(t, model, cmd)

			if got := string(model.input); got != "#docs/message.sql " {
				t.Fatalf("input after %s = %q, want selected file path", keyName, got)
			}
		})
	}
}

func TestFileCompletionListHidesPrefixAndTypeDetail(t *testing.T) {
	model := NewModel(Config{})
	model.mentionPrefix = "#"
	model.mentionCandidates = []CompletionCandidate{
		{Value: "docs/", Display: "docs/", Detail: "directory"},
		{Value: "docs/message.sql", Display: "docs/message.sql", Detail: "file"},
	}

	rendered := ansi.Strip(model.renderMentionList())

	for _, unwanted := range []string{"#docs/", "#docs/message.sql", "directory", "file"} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("renderMentionList() = %q, should not contain %q", rendered, unwanted)
		}
	}
	for _, want := range []string{"docs/", "docs/message.sql"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("renderMentionList() = %q, want %q", rendered, want)
		}
	}
}

func TestFileCompletionFetchesBeyondVisibleWindowAndScrolls(t *testing.T) {
	var gotLimit int
	model := NewModel(Config{
		MentionComplete: func(query string, limit int) ([]CompletionCandidate, error) {
			return nil, nil
		},
		FileComplete: func(query string, limit int) ([]CompletionCandidate, error) {
			gotLimit = limit
			return numberedCompletionCandidates("file", 12), nil
		},
	})

	model.setInputText("#")
	model.syncTextareaFromInput()
	model.refreshMention()
	if gotLimit != completionCandidateFetchLimit {
		t.Fatalf("FileComplete limit = %d, want %d", gotLimit, completionCandidateFetchLimit)
	}
	if len(model.mentionCandidates) != 12 {
		t.Fatalf("mentionCandidates = %d, want 12", len(model.mentionCandidates))
	}
	for i := 0; i < 9; i++ {
		handled, _ := model.handleMentionKey(keyPress("down"))
		if !handled {
			t.Fatal("handleMentionKey(down) = false, want true")
		}
	}

	rendered := ansi.Strip(model.renderMentionList())
	if !strings.Contains(rendered, "file-09") {
		t.Fatalf("renderMentionList() = %q, want selected file-09 visible", rendered)
	}
	if strings.Contains(rendered, "file-00") {
		t.Fatalf("renderMentionList() = %q, should have scrolled past file-00", rendered)
	}
	if !strings.Contains(rendered, "earlier") {
		t.Fatalf("renderMentionList() = %q, want earlier-page indicator", rendered)
	}
}

func TestFileCompletionLoadsNextPageAtBottomThenWrapsWhenExhausted(t *testing.T) {
	var limits []int
	model := NewModel(Config{
		MentionComplete: func(query string, limit int) ([]CompletionCandidate, error) {
			return nil, nil
		},
		FileComplete: func(query string, limit int) ([]CompletionCandidate, error) {
			limits = append(limits, limit)
			return numberedCompletionCandidates("file", minInt(limit, 65)), nil
		},
	})

	model.setInputText("#")
	model.syncTextareaFromInput()
	model.refreshMention()
	if len(model.mentionCandidates) != completionCandidateFetchLimit {
		t.Fatalf("mentionCandidates = %d, want initial page of %d", len(model.mentionCandidates), completionCandidateFetchLimit)
	}

	model.mentionIndex = len(model.mentionCandidates) - 1
	handled, _ := model.handleMentionKey(keyPress("down"))
	if !handled {
		t.Fatal("handleMentionKey(down) = false, want true")
	}
	if want := completionCandidateFetchLimit * 2; limits[len(limits)-1] != want {
		t.Fatalf("FileComplete second limit = %d, want %d", limits[len(limits)-1], want)
	}
	if len(model.mentionCandidates) != 65 {
		t.Fatalf("mentionCandidates after paging = %d, want 65", len(model.mentionCandidates))
	}
	if model.mentionIndex != completionCandidateFetchLimit {
		t.Fatalf("mentionIndex after paging = %d, want %d", model.mentionIndex, completionCandidateFetchLimit)
	}

	callCount := len(limits)
	model.mentionIndex = len(model.mentionCandidates) - 1
	handled, _ = model.handleMentionKey(keyPress("down"))
	if !handled {
		t.Fatal("handleMentionKey(down exhausted) = false, want true")
	}
	if len(limits) != callCount {
		t.Fatalf("FileComplete called after exhausted page: %v", limits)
	}
	if model.mentionIndex != 0 {
		t.Fatalf("mentionIndex after exhausted down = %d, want wrap to 0", model.mentionIndex)
	}
}

func TestMentionCompletionFetchesBeyondVisibleWindowAndScrolls(t *testing.T) {
	var gotLimit int
	model := NewModel(Config{
		MentionComplete: func(query string, limit int) ([]CompletionCandidate, error) {
			gotLimit = limit
			return numberedCompletionCandidates("agent", 12), nil
		},
	})

	model.setInputText("@")
	model.syncTextareaFromInput()
	model.refreshMention()
	if gotLimit != completionCandidateFetchLimit {
		t.Fatalf("MentionComplete limit = %d, want %d", gotLimit, completionCandidateFetchLimit)
	}
	if len(model.mentionCandidates) != 12 {
		t.Fatalf("mentionCandidates = %d, want 12", len(model.mentionCandidates))
	}
	for i := 0; i < 9; i++ {
		handled, _ := model.handleMentionKey(keyPress("down"))
		if !handled {
			t.Fatal("handleMentionKey(down) = false, want true")
		}
	}

	rendered := ansi.Strip(model.renderMentionList())
	if !strings.Contains(rendered, "@agent-09") {
		t.Fatalf("renderMentionList() = %q, want selected @agent-09 visible", rendered)
	}
	if strings.Contains(rendered, "@agent-00") {
		t.Fatalf("renderMentionList() = %q, should have scrolled past @agent-00", rendered)
	}
	if !strings.Contains(rendered, "earlier") {
		t.Fatalf("renderMentionList() = %q, want earlier-page indicator", rendered)
	}
}

func TestMentionCompletionLoadsNextPageAtBottomThenWrapsWhenExhausted(t *testing.T) {
	var limits []int
	model := NewModel(Config{
		MentionComplete: func(query string, limit int) ([]CompletionCandidate, error) {
			limits = append(limits, limit)
			return numberedCompletionCandidates("agent", minInt(limit, 65)), nil
		},
	})

	model.setInputText("@")
	model.syncTextareaFromInput()
	model.refreshMention()
	if len(model.mentionCandidates) != completionCandidateFetchLimit {
		t.Fatalf("mentionCandidates = %d, want initial page of %d", len(model.mentionCandidates), completionCandidateFetchLimit)
	}

	model.mentionIndex = len(model.mentionCandidates) - 1
	handled, _ := model.handleMentionKey(keyPress("down"))
	if !handled {
		t.Fatal("handleMentionKey(down) = false, want true")
	}
	if want := completionCandidateFetchLimit * 2; limits[len(limits)-1] != want {
		t.Fatalf("MentionComplete second limit = %d, want %d", limits[len(limits)-1], want)
	}
	if len(model.mentionCandidates) != 65 {
		t.Fatalf("mentionCandidates after paging = %d, want 65", len(model.mentionCandidates))
	}
	if model.mentionIndex != completionCandidateFetchLimit {
		t.Fatalf("mentionIndex after paging = %d, want %d", model.mentionIndex, completionCandidateFetchLimit)
	}

	callCount := len(limits)
	model.mentionIndex = len(model.mentionCandidates) - 1
	handled, _ = model.handleMentionKey(keyPress("down"))
	if !handled {
		t.Fatal("handleMentionKey(down exhausted) = false, want true")
	}
	if len(limits) != callCount {
		t.Fatalf("MentionComplete called after exhausted page: %v", limits)
	}
	if model.mentionIndex != 0 {
		t.Fatalf("mentionIndex after exhausted down = %d, want wrap to 0", model.mentionIndex)
	}
}

func TestRenderResumeListShowsMetadata(t *testing.T) {
	model := NewModel(Config{Commands: DefaultCommands()})
	model.resumeCandidates = []ResumeCandidate{
		{
			SessionID: "session-123",
			Title:     "Gateway cleanup",
			Model:     "openai/gpt-4o-mini",
			Workspace: "/tmp/workspace-alpha",
			Age:       "2m ago",
		},
	}
	model.resumeActive = true

	normalized := strings.Map(func(r rune) rune {
		switch r {
		case '\n', ' ', '│', '╭', '╮', '╰', '╯', '─':
			return -1
		default:
			return r
		}
	}, ansi.Strip(model.renderResumeList()))
	for _, want := range []string{"Gateway cleanup", "openai/gpt-4o-mini", "workspace-alpha", "id:session-123"} {
		if !strings.Contains(normalized, strings.ReplaceAll(want, " ", "")) {
			t.Fatalf("renderResumeList() = %q, want substring %q", normalized, want)
		}
	}
}

func TestModelActionPrefixTypingFiltersCandidatesDuringPaste(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "model":
				return []SlashArgCandidate{
					{Value: "use", Display: "use"},
					{Value: "del", Display: "del"},
				}, nil
			default:
				return nil, nil
			}
		},
	})

	_, cmd := model.handlePaste(tea.PasteMsg{Content: "/model de"})
	runCompletionCmd(t, model, cmd)

	if got := string(model.input); got != "/model de" {
		t.Fatalf("input after paste = %q, want /model de", got)
	}
	if got := model.slashArgCommand; got != "model" {
		t.Fatalf("slashArgCommand after paste = %q, want model", got)
	}
	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "del" {
		t.Fatalf("slashArgCandidates after paste = %#v, want only del candidate", model.slashArgCandidates)
	}
}

func TestModelActionPrefixTypingFiltersCandidatesWhenTerminalBatchesInput(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "model":
				return []SlashArgCandidate{
					{Value: "use", Display: "use"},
					{Value: "del", Display: "del"},
				}, nil
			default:
				return nil, nil
			}
		},
	})

	_, cmd := model.handleKey(keyPress("/model de"))
	runCompletionCmd(t, model, cmd)

	if got := string(model.input); got != "/model de" {
		t.Fatalf("input after batched key = %q, want /model de", got)
	}
	if got := model.slashArgCommand; got != "model" {
		t.Fatalf("slashArgCommand after batched key = %q, want model", got)
	}
	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "del" {
		t.Fatalf("slashArgCandidates after batched key = %#v, want only del candidate", model.slashArgCandidates)
	}
}

func TestModelActionPrefixTypingFiltersCandidatesAfterSlashThenBatchedTail(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "model":
				return []SlashArgCandidate{
					{Value: "use", Display: "use"},
					{Value: "del", Display: "del"},
				}, nil
			default:
				return nil, nil
			}
		},
	})

	_, cmd := model.handleKey(keyPress("/"))
	runCompletionCmd(t, model, cmd)
	_, cmd = model.handleKey(keyPress("model de"))
	runCompletionCmd(t, model, cmd)

	if got := string(model.input); got != "/model de" {
		t.Fatalf("input after slash + batched tail = %q, want /model de", got)
	}
	if got := model.slashArgCommand; got != "model" {
		t.Fatalf("slashArgCommand after slash + batched tail = %q, want model", got)
	}
	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "del" {
		t.Fatalf("slashArgCandidates after slash + batched tail = %#v, want only del candidate", model.slashArgCandidates)
	}
}

func keyPress(key string) tea.KeyMsg {
	return tea.KeyPressMsg(tea.Key{Text: key})
}

func numberedCompletionCandidates(prefix string, count int) []CompletionCandidate {
	out := make([]CompletionCandidate, 0, count)
	for i := 0; i < count; i++ {
		value := fmt.Sprintf("%s-%02d", prefix, i)
		out = append(out, CompletionCandidate{
			Value:   value,
			Display: value,
		})
	}
	return out
}

func runCompletionCmd(t *testing.T, model *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	msg := cmd()
	switch typed := msg.(type) {
	case nil:
		return
	case completionRefreshMsg:
		updated, _ := model.Update(typed)
		if next, ok := updated.(*Model); ok && next != model {
			*model = *next
		}
	case tea.BatchMsg:
		for _, sub := range typed {
			runCompletionCmd(t, model, sub)
		}
	default:
		updated, _ := model.Update(typed)
		if next, ok := updated.(*Model); ok && next != model {
			*model = *next
		}
	}
}

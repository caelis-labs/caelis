package tuiapp

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestModelSelectionOpensEffortPickerDirectly(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(_ context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "model":
				return []SlashArgCandidate{
					{Value: "minimax/minimax-m1", Display: "minimax/minimax-m1"},
					{Value: "alt-model", Display: "alt-model"},
				}, nil
			case "model alt-model":
				return []SlashArgCandidate{{Value: "none", Display: "none"}}, nil
			default:
				return nil, nil
			}
		},
	})

	runCompletionCmd(t, model, model.openSlashArgPicker("model"))
	model.slashArgIndex = 1
	runCompletionCmd(t, model, model.applySlashArgCompletion())

	if got := string(model.input); got != "/model alt-model " {
		t.Fatalf("input after model selection = %q, want %q", got, "/model alt-model ")
	}
	if got := model.slashArgCommand; got != "model alt-model" {
		t.Fatalf("slashArgCommand = %q, want model alt-model", got)
	}
	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "none" {
		t.Fatalf("effort candidates = %#v, want none", model.slashArgCandidates)
	}
}

func TestPluginCommandShowsRootCompletion(t *testing.T) {
	for _, input := range []string{"/plugin", "/plugin "} {
		t.Run(fmt.Sprintf("%q", input), func(t *testing.T) {
			model := NewModel(Config{
				Commands: DefaultCommands(),
				SlashArgComplete: func(_ context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
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
			syncSlashInputOverlaysForTest(t, model)

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
		SlashArgComplete: func(_ context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
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

	runCompletionCmd(t, model, model.openSlashArgPicker("plugin"))
	runCompletionCmd(t, model, model.applySlashArgCompletion())

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
	runCompletionCmd(t, model, model.applySlashArgCompletion())

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
				SlashArgComplete: func(_ context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
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
			syncSlashInputOverlaysForTest(t, model)

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
		SlashArgComplete: func(_ context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
			if command != "plugin marketplace" {
				return nil, nil
			}
			return []SlashArgCandidate{{Value: "add"}, {Value: "list"}, {Value: "update"}, {Value: "rm"}}, nil
		},
	})

	runCompletionCmd(t, model, model.openSlashArgPicker("plugin marketplace"))
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

func TestTypedResumeEnterLoadsEmptyQueryAndSubmitsSelectedSession(t *testing.T) {
	var (
		submitted     string
		completionCtx context.Context
	)
	model := NewModel(Config{
		Commands: DefaultCommands(),
		ResumeComplete: func(ctx context.Context, _ string, _ int) ([]ResumeCandidate, error) {
			completionCtx = ctx
			return []ResumeCandidate{{SessionID: "session-1"}}, nil
		},
		ExecuteLine: func(submission Submission) TaskResultMsg {
			submitted = submission.Text
			return TaskResultMsg{}
		},
	})
	model.setInputText("/resume")
	model.syncTextareaFromInput()

	next, cmd := model.handleKey(keyPress("enter"))
	model = next.(*Model)
	if !model.resumeActive || model.textarea.Value() != "/resume " {
		t.Fatalf("resume picker state = active:%v input:%q", model.resumeActive, model.textarea.Value())
	}
	if cmd == nil {
		t.Fatal("opening typed /resume did not schedule asynchronous completion")
	}
	runCompletionCmd(t, model, cmd)
	if len(model.resumeCandidates) != 1 || model.resumeCandidates[0].SessionID != "session-1" {
		t.Fatalf("empty-query resume candidates = %#v, want session-1", model.resumeCandidates)
	}
	if completionCtx == nil {
		t.Fatal("resume completion did not receive a request context")
	}
	if !errors.Is(completionCtx.Err(), context.Canceled) {
		t.Fatalf("completed resume request context error = %v, want canceled", completionCtx.Err())
	}

	next, cmd = model.handleKey(keyPress("enter"))
	model = next.(*Model)
	if cmd == nil || !findAndRunTaskResult(cmd(), model) {
		t.Fatal("selected resume candidate did not produce a submission command")
	}
	if submitted != "/resume session-1" {
		t.Fatalf("submitted resume line = %q, want /resume session-1", submitted)
	}
}

func TestResumeTabRetriesAfterTransientCompletionFailure(t *testing.T) {
	attempts := 0
	model := NewModel(Config{
		Commands: DefaultCommands(),
		ResumeComplete: func(context.Context, string, int) ([]ResumeCandidate, error) {
			attempts++
			if attempts == 1 {
				return nil, errors.New("temporary list failure")
			}
			return []ResumeCandidate{{SessionID: "retry-session"}}, nil
		},
	})
	model.setInputText("/resume retry")
	model.syncTextareaFromInput()
	syncSlashInputOverlaysForTest(t, model)
	completeResumeCandidates(t, model)
	if attempts != 1 || len(model.resumeCandidates) != 0 {
		t.Fatalf("first completion attempts=%d candidates=%#v, want one failed attempt", attempts, model.resumeCandidates)
	}

	next, cmd := model.handleKey(keyPress("tab"))
	model = next.(*Model)
	if cmd == nil {
		t.Fatal("Tab after completion failure did not schedule an asynchronous retry")
	}
	runCompletionCmd(t, model, cmd)
	if attempts != 2 || len(model.resumeCandidates) != 1 || model.resumeCandidates[0].SessionID != "retry-session" {
		t.Fatalf("retried completion attempts=%d candidates=%#v", attempts, model.resumeCandidates)
	}
}

func candidateValuesForTUITest(candidates []SlashArgCandidate) []string {
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, strings.TrimSpace(candidate.Value))
	}
	return out
}

func modelSwitchSlashArgComplete(alias string) func(context.Context, string, string, int) ([]SlashArgCandidate, error) {
	alias = strings.TrimSpace(alias)
	return func(_ context.Context, command string, _ string, _ int) ([]SlashArgCandidate, error) {
		switch command {
		case "model":
			return []SlashArgCandidate{{Value: alias, Display: alias}}, nil
		case "model " + alias:
			return []SlashArgCandidate{{Value: "none", Display: "none"}, {Value: "high", Display: "high"}}, nil
		case "model " + alias + " high":
			return []SlashArgCandidate{
				{Value: "default", Display: "Default"},
				{Value: "fast", Display: "Fast"},
			}, nil
		default:
			return nil, nil
		}
	}
}

func TestTryOpenSlashArgPickerUsesCommandRegistry(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(_ context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
			if command != "plugin" {
				return nil, nil
			}
			return []SlashArgCandidate{{Value: "install"}, {Value: "manage"}, {Value: "rm"}}, nil
		},
	})

	opened, cmd := model.tryOpenSlashArgPicker("/plugin")
	if !opened {
		t.Fatal("tryOpenSlashArgPicker(/plugin) = false, want true")
	}
	runCompletionCmd(t, model, cmd)
	if got := model.slashArgCommand; got != "plugin" {
		t.Fatalf("slashArgCommand = %q, want plugin", got)
	}
}

func TestTryOpenSlashArgPickerRejectsLegacyModelUseAndDelete(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(context.Context, string, string, int) ([]SlashArgCandidate, error) {
			t.Fatal("legacy /model use and /model del should not request completion")
			return nil, nil
		},
	})
	for _, line := range []string{"/model use", "/model del"} {
		opened, cmd := model.tryOpenSlashArgPicker(line)
		if opened || cmd != nil || model.slashArgActive {
			t.Fatalf("tryOpenSlashArgPicker(%q) opened picker command=%q active=%v", line, model.slashArgCommand, model.slashArgActive)
		}
	}
}

func TestTryOpenSlashArgPickerOpensModelListDirectly(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(_ context.Context, command string, _ string, _ int) ([]SlashArgCandidate, error) {
			if command != "model" {
				return nil, nil
			}
			return []SlashArgCandidate{{Value: "ollama@local/ollama/qwen3", Display: "ollama/qwen3"}}, nil
		},
	})

	opened, cmd := model.tryOpenSlashArgPicker("/model")
	if !opened {
		t.Fatal("tryOpenSlashArgPicker(/model) = false, want true")
	}
	runCompletionCmd(t, model, cmd)
	if model.slashArgCommand != "model" || model.textarea.Value() != "/model " ||
		len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Display != "ollama/qwen3" {
		t.Fatalf("direct /model picker = command %q input %q candidates %#v", model.slashArgCommand, model.textarea.Value(), model.slashArgCandidates)
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
		SlashArgComplete: func(_ context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
			if command != "plugin" {
				return nil, nil
			}
			return []SlashArgCandidate{{Value: "install"}, {Value: "manage"}, {Value: "rm"}}, nil
		},
	})

	runCompletionCmd(t, model, model.openSlashArgPicker("plugin"))
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

func TestModelSelectionOpensEffortThenSpeedPicker(t *testing.T) {
	const alias = "deepseek/deepseek-v4-pro"
	model := NewModel(Config{
		Commands:         DefaultCommands(),
		SlashArgComplete: modelSwitchSlashArgComplete(alias),
	})

	runCompletionCmd(t, model, model.openSlashArgPicker("model"))
	runCompletionCmd(t, model, model.applySlashArgCompletion())
	if got := string(model.input); got != "/model "+alias+" " {
		t.Fatalf("input after model alias = %q, want alias plus trailing space", got)
	}
	if got := model.slashArgCommand; got != "model "+alias {
		t.Fatalf("slashArgCommand = %q, want effort picker command", got)
	}
	if len(model.slashArgCandidates) != 2 || model.slashArgCandidates[1].Value != "high" {
		t.Fatalf("effort candidates = %#v, want none/high", model.slashArgCandidates)
	}

	model.slashArgIndex = 1
	runCompletionCmd(t, model, model.applySlashArgCompletion())
	if got := string(model.input); got != "/model "+alias+" high " {
		t.Fatalf("input after effort selection = %q, want high plus trailing space", got)
	}
	if got := model.slashArgCommand; got != "model "+alias+" high" {
		t.Fatalf("slashArgCommand = %q, want speed picker command", got)
	}
	if got := candidateValuesForTUITest(model.slashArgCandidates); strings.Join(got, ",") != "default,fast" {
		t.Fatalf("speed candidates = %#v, want default/fast", model.slashArgCandidates)
	}

	runCompletionCmd(t, model, model.applySlashArgCompletion())
	if got := string(model.input); got != "/model "+alias+" high" {
		t.Fatalf("input after default speed = %q, want no speed token", got)
	}
	if model.slashArgActive {
		t.Fatal("default speed tab should close the picker without submitting")
	}
}

func TestModelSelectionSkipsSpeedPickerWithoutSpeedModes(t *testing.T) {
	const alias = "ollama/llama3"
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(_ context.Context, command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "model":
				return []SlashArgCandidate{{Value: alias, Display: alias}}, nil
			case "model " + alias:
				return []SlashArgCandidate{{Value: "none", Display: "none"}}, nil
			default:
				return nil, nil
			}
		},
	})

	runCompletionCmd(t, model, model.openSlashArgPicker("model"))
	runCompletionCmd(t, model, model.applySlashArgCompletion())
	runCompletionCmd(t, model, model.applySlashArgCompletion())

	if got := string(model.input); got != "/model "+alias+" none " {
		t.Fatalf("input after effort selection = %q, want complete model command", got)
	}
	if len(model.slashArgCandidates) != 0 || model.completionOverlayActive() {
		t.Fatalf("unsupported speed picker candidates = %#v, active=%v", model.slashArgCandidates, model.completionOverlayActive())
	}
}

func TestModelSpeedDefaultSelectionSubmitsWithoutToken(t *testing.T) {
	const alias = "deepseek/deepseek-v4-pro"
	var submitted string
	model := NewModel(Config{
		Commands: DefaultCommands(),
		ExecuteLine: func(submission Submission) TaskResultMsg {
			submitted = submission.Text
			return TaskResultMsg{SuppressTurnDivider: true}
		},
		SlashArgComplete: modelSwitchSlashArgComplete(alias),
	})

	runCompletionCmd(t, model, model.openSlashArgPicker("model"))
	runCompletionCmd(t, model, model.applySlashArgCompletion())
	model.slashArgIndex = 1
	runCompletionCmd(t, model, model.applySlashArgCompletion())

	handled, cmd := model.handleSlashArgKey(keyPress("enter"))
	if !handled {
		t.Fatal("handleSlashArgKey(enter default) = false, want true")
	}
	if cmd == nil {
		t.Fatal("handleSlashArgKey(enter default) command = nil, want submit command")
	}
	findAndRunTaskResult(cmd(), model)
	if submitted != "/model "+alias+" high" {
		t.Fatalf("submitted input = %q, want /model %s high without a speed token", submitted, alias)
	}
}

func TestModelSpeedFastSelectionAppendsFast(t *testing.T) {
	const alias = "deepseek/deepseek-v4-pro"
	var submitted string
	model := NewModel(Config{
		Commands: DefaultCommands(),
		ExecuteLine: func(submission Submission) TaskResultMsg {
			submitted = submission.Text
			return TaskResultMsg{SuppressTurnDivider: true}
		},
		SlashArgComplete: modelSwitchSlashArgComplete(alias),
	})

	runCompletionCmd(t, model, model.openSlashArgPicker("model"))
	runCompletionCmd(t, model, model.applySlashArgCompletion())
	model.slashArgIndex = 1
	runCompletionCmd(t, model, model.applySlashArgCompletion())
	model.slashArgIndex = 1

	handled, cmd := model.handleSlashArgKey(keyPress("enter"))
	if !handled {
		t.Fatal("handleSlashArgKey(enter fast) = false, want true")
	}
	if cmd == nil {
		t.Fatal("handleSlashArgKey(enter fast) command = nil, want submit command")
	}
	findAndRunTaskResult(cmd(), model)
	if submitted != "/model "+alias+" high fast" {
		t.Fatalf("submitted input = %q, want /model %s high fast", submitted, alias)
	}
}

func TestModelExactAliasInputOpensEffortPicker(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(_ context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "model":
				return []SlashArgCandidate{{Value: "gpt-5.5", Display: "GPT-5.5"}}, nil
			case "model gpt-5.5":
				return []SlashArgCandidate{{Value: "high", Display: "High"}, {Value: "xhigh", Display: "Xhigh"}}, nil
			default:
				return nil, nil
			}
		},
	})

	model.setInputText("/model gpt-5.5")
	model.syncTextareaFromInput()
	syncSlashInputOverlaysForTest(t, model)

	if got := model.slashArgCommand; got != "model gpt-5.5" {
		t.Fatalf("slashArgCommand = %q, want effort picker command", got)
	}
	if len(model.slashArgCandidates) != 2 || model.slashArgCandidates[1].Value != "xhigh" {
		t.Fatalf("effort candidates = %#v, want high/xhigh", model.slashArgCandidates)
	}
}

func TestModelExactEffortInputOpensSpeedPicker(t *testing.T) {
	model := NewModel(Config{
		Commands:         DefaultCommands(),
		SlashArgComplete: modelSwitchSlashArgComplete("gpt-5.5"),
	})

	model.setInputText("/model gpt-5.5 high")
	model.syncTextareaFromInput()
	syncSlashInputOverlaysForTest(t, model)

	if got := model.slashArgCommand; got != "model gpt-5.5 high" {
		t.Fatalf("slashArgCommand = %q, want speed picker command", got)
	}
	if got := candidateValuesForTUITest(model.slashArgCandidates); strings.Join(got, ",") != "default,fast" {
		t.Fatalf("speed candidates = %#v, want default/fast", model.slashArgCandidates)
	}
}

func TestSuggestedSlashArgInputOmitsDefaultSpeedToken(t *testing.T) {
	model := NewModel(Config{Commands: DefaultCommands()})
	model.slashArgActive = true
	model.slashArgCommand = "model gpt-5.5 high"

	if got := model.suggestedSlashArgInput("default"); got != "/model gpt-5.5 high" {
		t.Fatalf("suggested default speed = %q, want command without a speed token", got)
	}
	if got := model.suggestedSlashArgInput("fast"); got != "/model gpt-5.5 high fast" {
		t.Fatalf("suggested fast speed = %q, want fast appended", got)
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
			name:      "plugin remove",
			command:   "plugin rm",
			input:     "/plugin rm ",
			candidate: "stale-plugin",
			wantInput: "/plugin rm stale-plugin",
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
				SlashArgComplete: func(_ context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
					if command != tt.command {
						return nil, nil
					}
					return []SlashArgCandidate{{Value: tt.candidate, Display: tt.candidate}}, nil
				},
			})

			model.setInputText(tt.input)
			model.syncTextareaFromInput()
			syncSlashInputOverlaysForTest(t, model)
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
			syncSlashInputOverlaysForTest(t, model)
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

func TestSlashCompletionMergesSkillsWithoutShadowingCommands(t *testing.T) {
	model := NewModel(Config{
		Commands: []string{"help", "status", "reviewer"},
		SkillComplete: func(string, int) ([]CompletionCandidate, error) {
			return []CompletionCandidate{
				{Value: "lint", Display: "lint", Kind: "Skill", Detail: "Run lint checks"},
				{Value: "status", Display: "status", Kind: "Skill", Detail: "Custom status workflow"},
				{Value: "custom:status", Display: "status", Kind: "Plugin", Detail: "Namespaced status workflow"},
				{Value: "reviewer", Display: "reviewer", Kind: "Skill", Detail: "Custom review workflow"},
				{Value: "superpowers:brainstorm", Display: "brainstorm", Kind: "Plugin", Detail: "superpowers · Explore alternatives"},
			}, nil
		},
	})

	model.setInputText("/")
	loadSlashSkillCatalog(t, model)

	counts := map[string]int{}
	for _, candidate := range model.slashCandidates {
		counts[candidate]++
	}
	for _, want := range []string{"/help", "/status", "/reviewer", "/lint", "/custom:status", "/superpowers:brainstorm"} {
		if counts[want] != 1 {
			t.Fatalf("slashCandidates = %#v, want one %s", model.slashCandidates, want)
		}
	}
	if counts["/brainstorm"] != 0 {
		t.Fatalf("slashCandidates = %#v, plugin skill should use canonical namespace", model.slashCandidates)
	}
	if got := model.slashCommandDisplay("/custom:status"); got != "/custom:status" {
		t.Fatalf("slashCommandDisplay(/custom:status) = %q, want canonical display beside built-in /status", got)
	}
	if detail := model.commandCompletionDetail("/lint"); !strings.Contains(detail, "Skill") || !strings.Contains(detail, "Run lint checks") {
		t.Fatalf("commandCompletionDetail(/lint) = %q, want skill metadata", detail)
	}

	model.setInputText("/brain")
	model.refreshSlashCommands()
	if len(model.slashCandidates) != 1 || model.slashCandidates[0] != "/superpowers:brainstorm" {
		t.Fatalf("slashCandidates for plugin local query = %#v, want canonical namespaced command", model.slashCandidates)
	}
}

func TestSlashCompletionDistinguishesDuplicateSkillLabelsFromCanonicalValues(t *testing.T) {
	model := NewModel(Config{
		Commands: []string{"help"},
		SkillComplete: func(string, int) ([]CompletionCandidate, error) {
			return []CompletionCandidate{
				{Value: "one:lint", Display: "lint", Kind: "Plugin", Detail: "one · Run repository lint"},
				{Value: "two:lint", Display: "lint", Kind: "Plugin", Detail: "two · Run workspace lint"},
			}, nil
		},
	})

	model.setInputText("/")
	loadSlashSkillCatalog(t, model)

	if got := model.slashCandidates; !reflect.DeepEqual(got, []string{"/help", "/one:lint", "/two:lint"}) {
		t.Fatalf("slashCandidates = %#v, want unique canonical command values", got)
	}
	for _, command := range []string{"/one:lint", "/two:lint"} {
		if got := model.slashCommandDisplay(command); got != "/lint" {
			t.Fatalf("slashCommandDisplay(%q) = %q, want shared local label", command, got)
		}
	}
	if one, two := model.commandCompletionDetail("/one:lint"), model.commandCompletionDetail("/two:lint"); one == two || !strings.Contains(one, "one") || !strings.Contains(two, "two") {
		t.Fatalf("duplicate label details = %q and %q, want distinct sources", one, two)
	}

	model.slashIndex = 2
	model.applySlashCommandCompletion()
	if got := string(model.input); got != "/two:lint " {
		t.Fatalf("selected duplicate skill inserted %q, want canonical slash identity", got)
	}
}

func TestSlashCompletionRebuildClearsStaleSkillDetails(t *testing.T) {
	skills := []CompletionCandidate{
		{Value: "lint", Display: "lint", Kind: "Skill", Detail: "Run lint checks"},
	}
	model := NewModel(Config{
		Commands: []string{"help"},
		SkillComplete: func(string, int) ([]CompletionCandidate, error) {
			return skills, nil
		},
	})
	model.setInputText("/")
	loadSlashSkillCatalog(t, model)
	if detail := model.commandCompletionDetail("/lint"); !strings.Contains(detail, "Run lint checks") {
		t.Fatalf("initial /lint detail = %q, want skill detail", detail)
	}

	skills = nil
	model.resetSlashSkillCatalog()
	model.setInputText("/h")
	loadSlashSkillCatalog(t, model)
	if len(model.slashDetails) != 0 {
		t.Fatalf("slashDetails after rebuild = %#v, want stale skill metadata cleared", model.slashDetails)
	}
}

func TestDollarDoesNotInvokeSkillCompletion(t *testing.T) {
	calls := 0
	model := NewModel(Config{
		Commands: []string{"help"},
		SkillComplete: func(string, int) ([]CompletionCandidate, error) {
			calls++
			return []CompletionCandidate{{Value: "lint", Display: "lint", Kind: "Skill"}}, nil
		},
	})
	model.setInputText("$li")
	model.syncTextareaFromInput()
	model.refreshCompletionOverlaysNow()

	if calls != 0 {
		t.Fatalf("SkillComplete calls = %d, want $ input disconnected from skill completion", calls)
	}
	if model.completionOverlayActive() {
		t.Fatal("$ input opened a completion overlay, want slash-only skill discovery")
	}
}

func TestSlashCommandTypingRefreshesBuiltinsImmediately(t *testing.T) {
	model := NewModel(Config{Commands: []string{"help", "status", "stop"}})

	_, _ = model.handleKey(keyPress("/"))
	if got := model.slashCandidates; !reflect.DeepEqual(got, []string{"/help", "/status", "/stop"}) {
		t.Fatalf("slashCandidates after / = %#v, want immediate built-in commands", got)
	}

	_, _ = model.handleKey(keyPress("st"))
	if got := model.slashCandidates; !reflect.DeepEqual(got, []string{"/status", "/stop"}) {
		t.Fatalf("slashCandidates after /st = %#v, want immediate prefix matches", got)
	}
}

func TestSlashCommandCompletionSubmitsOnlySafeArgumentFreeCommands(t *testing.T) {
	for _, command := range []string{"help", "status", "exit", "quit"} {
		if !slashCommandSubmitsOnCompletion(command) {
			t.Fatalf("slashCommandSubmitsOnCompletion(%q) = false, want true", command)
		}
	}
	for _, command := range []string{"review", "breeze", "orbit", "zenith", "connect", "subagent", "plugin", "model", "doctor", "new", "resume", "compact"} {
		if slashCommandSubmitsOnCompletion(command) {
			t.Fatalf("slashCommandSubmitsOnCompletion(%q) = true, want explicit confirmation", command)
		}
	}

	for _, command := range []string{"help", "status"} {
		t.Run(command, func(t *testing.T) {
			var submitted []Submission
			model := NewModel(Config{
				Commands: []string{command},
				ExecuteLine: func(submission Submission) TaskResultMsg {
					submitted = append(submitted, submission)
					return TaskResultMsg{}
				},
			})
			model.setInputText("/" + command[:1])
			model.syncTextareaFromInput()
			model.refreshSlashCommands()

			_, cmd := model.handleKey(keyPress("tab"))
			if cmd == nil {
				t.Fatalf("/%s completion command = nil, want immediate submit", command)
			}
			findAndRunTaskResult(cmd(), model)
			if len(submitted) != 1 || submitted[0].Text != "/"+command {
				t.Fatalf("/%s submissions = %#v, want immediate exact command", command, submitted)
			}
			if got := model.textarea.Value(); got != "" {
				t.Fatalf("/%s composer = %q, want cleared after submit", command, got)
			}
		})
	}

	for _, command := range []string{"exit", "quit"} {
		t.Run(command, func(t *testing.T) {
			model := NewModel(Config{
				Commands: []string{command},
				ExecuteLine: func(Submission) TaskResultMsg {
					t.Fatalf("/%s completion reached asynchronous ExecuteLine", command)
					return TaskResultMsg{}
				},
			})
			model.setInputText("/" + command[:1])
			model.syncTextareaFromInput()
			model.refreshSlashCommands()

			_, cmd := model.handleKey(keyPress("tab"))
			if cmd == nil || !model.quit || model.liveTurn.Active {
				t.Fatalf("/%s completion = cmd:%v quit:%v live:%v, want immediate local quit", command, cmd != nil, model.quit, model.liveTurn.Active)
			}
		})
	}

	model := NewModel(Config{Commands: []string{"new"}})
	model.setInputText("/")
	model.syncTextareaFromInput()
	model.refreshSlashCommands()
	if _, cmd := model.handleKey(keyPress("tab")); cmd != nil {
		t.Fatal("/new completion submitted without Enter confirmation")
	}
	if got := string(model.input); got != "/new " {
		t.Fatalf("/new completion input = %q, want filled command awaiting Enter", got)
	}
}

func TestSlashSkillCatalogLoadDoesNotBlockUpdateLoop(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	model := NewModel(Config{
		Commands: []string{"help", "status"},
		SkillComplete: func(query string, limit int) ([]CompletionCandidate, error) {
			if query != "" || limit != completionCandidateMaxLimit {
				t.Errorf("SkillComplete(%q, %d), want full bounded catalog", query, limit)
			}
			close(started)
			<-release
			return []CompletionCandidate{{Value: "lint", Display: "lint", Kind: "Skill"}}, nil
		},
	})
	model.setInputText("/")
	model.refreshSlashCommands()

	cmd := requireTestCallReturns(t, "requestSlashSkillCatalog", model.requestSlashSkillCatalog)
	if cmd == nil {
		t.Fatal("requestSlashSkillCatalog() = nil, want asynchronous command")
	}
	select {
	case <-started:
		t.Fatal("SkillComplete ran on the caller before the command started")
	default:
	}

	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("skill completion backend did not start")
	}
	requireTestActionReturns(t, "WindowSize Update behind skill completion", func() {
		_, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	})
	close(release)
	msg := <-result
	_, _ = model.Update(msg)
	if got := model.slashCandidates; !reflect.DeepEqual(got, []string{"/help", "/lint", "/status"}) {
		t.Fatalf("slashCandidates after async load = %#v", got)
	}
}

func TestFileCompletionRequestDoesNotBlockUpdateLoop(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	model := NewModel(Config{
		FileComplete: func(ctx context.Context, query string, limit int) ([]CompletionCandidate, error) {
			if query != "docs" || limit != completionCandidateFetchLimit {
				t.Errorf("FileComplete(%q, %d), want docs and initial page", query, limit)
			}
			close(started)
			select {
			case <-release:
				return []CompletionCandidate{{Value: "docs/architecture.md", Display: "docs/architecture.md"}}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	})
	model.setInputText("@docs")
	model.syncTextareaFromInput()

	cmd := requireTestCallReturns(t, "requestMentionCompletion", func() tea.Cmd {
		return model.requestMentionCompletion(0)
	})
	if cmd == nil {
		t.Fatal("requestMentionCompletion() = nil, want asynchronous command")
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("file completion backend did not start")
	}
	requireTestActionReturns(t, "WindowSize Update behind file completion", func() {
		_, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	})
	close(release)
	_, _ = model.Update(<-result)
	if len(model.mentionCandidates) != 1 || model.mentionCandidates[0].Value != "docs/architecture.md" {
		t.Fatalf("mentionCandidates = %#v, want asynchronous file result", model.mentionCandidates)
	}
}

func TestSlashArgCompletionRequestDoesNotBlockUpdateLoop(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	model := NewModel(Config{
		SlashArgComplete: func(ctx context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
			if command != "plugin" || query != "m" || limit != 200 {
				t.Errorf("SlashArgComplete(%q, %q, %d), want plugin m 200", command, query, limit)
			}
			close(started)
			select {
			case <-release:
				return []SlashArgCandidate{{Value: "manage", Display: "manage"}}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	})
	model.slashArgActive = true
	model.slashArgCommand = "plugin"
	model.setInputText("/plugin m")
	model.syncTextareaFromInput()

	cmd := requireTestCallReturns(t, "requestSlashArgCompletion", model.requestSlashArgCompletion)
	if cmd == nil {
		t.Fatal("requestSlashArgCompletion() = nil, want asynchronous command")
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("slash argument completion backend did not start")
	}
	requireTestActionReturns(t, "WindowSize Update behind slash argument completion", func() {
		_, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	})
	close(release)
	_, _ = model.Update(<-result)
	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "manage" {
		t.Fatalf("slashArgCandidates = %#v, want asynchronous slash result", model.slashArgCandidates)
	}
}

func TestSlashCommandTabOpensArgPickerWithoutBlocking(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	model := NewModel(Config{
		Commands: []string{"plugin"},
		SlashArgComplete: func(ctx context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
			if command != "plugin" || query != "" || limit != 200 {
				t.Errorf("SlashArgComplete(%q, %q, %d), want plugin empty-query 200", command, query, limit)
			}
			close(started)
			select {
			case <-release:
				return []SlashArgCandidate{{Value: "install", Display: "install"}}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	})
	model.setInputText("/p")
	model.syncTextareaFromInput()
	model.refreshSlashCommands()

	cmd := requireTestCallReturns(t, "Tab opening slash argument picker", func() tea.Cmd {
		_, cmd := model.handleKey(keyPress("tab"))
		return cmd
	})
	if cmd == nil || !model.slashArgActive || model.slashArgCommand != "plugin" {
		t.Fatalf("Tab picker state = cmd:%v active:%v command:%q", cmd != nil, model.slashArgActive, model.slashArgCommand)
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("slash argument completion backend did not start")
	}

	pendingTab := requireTestCallReturns(t, "Tab while slash argument completion is pending", func() struct {
		handled bool
		cmd     tea.Cmd
	} {
		handled, cmd := model.handleSlashArgKey(keyPress("tab"))
		return struct {
			handled bool
			cmd     tea.Cmd
		}{handled: handled, cmd: cmd}
	})
	if !pendingTab.handled || pendingTab.cmd != nil {
		t.Fatalf("Tab while completion pending = handled:%v cmd:%v, want non-blocking wait", pendingTab.handled, pendingTab.cmd != nil)
	}

	close(release)
	_, _ = model.Update(<-result)
	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "install" {
		t.Fatalf("slashArgCandidates = %#v, want asynchronous picker result", model.slashArgCandidates)
	}
}

func TestDestructiveSlashArgEnterWaitsForCurrentCompletion(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var submitted []string
	model := NewModel(Config{
		ExecuteLine: func(submission Submission) TaskResultMsg {
			submitted = append(submitted, submission.Text)
			return TaskResultMsg{}
		},
		SlashArgComplete: func(ctx context.Context, command string, query string, _ int) ([]SlashArgCandidate, error) {
			if command != "plugin rm" || query != "stale-plugin" {
				t.Errorf("SlashArgComplete(%q, %q), want plugin rm stale-plugin", command, query)
			}
			close(started)
			select {
			case <-release:
				return []SlashArgCandidate{{Value: "stale-plugin", Display: "stale-plugin"}}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	})
	model.slashArgActive = true
	model.slashArgCommand = "plugin rm"
	model.setInputText("/plugin rm stale-plugin")
	model.syncTextareaFromInput()

	_, cmd := model.handleKey(keyPress("enter"))
	if cmd == nil || !model.slashArgRequestPending {
		t.Fatalf("first Enter = cmd:%v pending:%v, want asynchronous validation", cmd != nil, model.slashArgRequestPending)
	}
	if len(submitted) != 0 {
		t.Fatalf("submission started before completion: %#v", submitted)
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("slash argument validation did not start")
	}
	if hint := ansi.Strip(model.buildHintText()); !strings.Contains(hint, "loading completions") {
		t.Fatalf("pending hint = %q, want visible completion loading state", hint)
	}
	if _, duplicate := model.handleKey(keyPress("enter")); duplicate != nil {
		t.Fatal("Enter while validation is pending scheduled duplicate work")
	}
	if len(submitted) != 0 {
		t.Fatalf("pending Enter submitted destructive command: %#v", submitted)
	}

	close(release)
	_, _ = model.Update(<-result)
	_, submitCmd := model.handleKey(keyPress("enter"))
	if submitCmd == nil || !findAndRunTaskResult(submitCmd(), model) {
		t.Fatal("validated destructive selection did not submit")
	}
	if !reflect.DeepEqual(submitted, []string{"/plugin rm stale-plugin"}) {
		t.Fatalf("submitted = %#v, want validated plugin removal", submitted)
	}
}

func TestMentionEnterAndTabWaitForPendingCompletion(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var submitted []string
	model := NewModel(Config{
		ExecuteLine: func(submission Submission) TaskResultMsg {
			submitted = append(submitted, submission.Text)
			return TaskResultMsg{}
		},
		FileComplete: func(ctx context.Context, query string, _ int) ([]CompletionCandidate, error) {
			if query != "docs" {
				t.Errorf("FileComplete(%q), want docs", query)
			}
			close(started)
			select {
			case <-release:
				return []CompletionCandidate{{Value: "docs/architecture.md", Display: "docs/architecture.md"}}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	})
	model.setInputText("@docs")
	model.syncTextareaFromInput()

	_, cmd := model.handleKey(keyPress("enter"))
	if cmd == nil || !model.mentionRequestPending {
		t.Fatalf("first Enter = cmd:%v pending:%v, want mention request", cmd != nil, model.mentionRequestPending)
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("mention completion did not start")
	}
	if _, duplicate := model.handleKey(keyPress("tab")); duplicate != nil {
		t.Fatal("Tab while mention completion is pending scheduled duplicate work")
	}
	if _, duplicate := model.handleKey(keyPress("enter")); duplicate != nil {
		t.Fatal("Enter while mention completion is pending scheduled duplicate work")
	}
	if len(submitted) != 0 {
		t.Fatalf("pending mention Enter submitted prompt: %#v", submitted)
	}
	if hint := ansi.Strip(model.buildHintText()); !strings.Contains(hint, "loading completions") {
		t.Fatalf("pending hint = %q, want visible completion loading state", hint)
	}

	close(release)
	_, _ = model.Update(<-result)
	_, acceptCmd := model.handleKey(keyPress("enter"))
	if acceptCmd != nil {
		t.Fatal("accepting a loaded mention unexpectedly submitted the prompt")
	}
	if got := model.textarea.Value(); got != "@docs/architecture.md " {
		t.Fatalf("mention input = %q, want completed file path", got)
	}
	if len(submitted) != 0 {
		t.Fatalf("mention acceptance submitted prompt: %#v", submitted)
	}
}

func TestAsyncSlashArgCatalogRefiltersOnComposerInput(t *testing.T) {
	command := "connect-acp-model:" + buildACPConnectWizardPayload(map[string]string{
		"acp_agent": "grok", "acp_launcher": "installed",
	})
	calls := 0
	def := WizardDef{
		Command: "async-catalog-test",
		Steps: []WizardStepDef{{
			Key:              "model",
			RequireCandidate: true,
			CompletionCommand: func(map[string]string) string {
				return command
			},
		}},
	}
	model := NewModel(Config{
		SlashArgComplete: func(_ context.Context, gotCommand string, query string, limit int) ([]SlashArgCandidate, error) {
			calls++
			if gotCommand != command || query != "" || limit != 200 {
				t.Fatalf("SlashArgComplete(%q, %q, %d), want async catalog request", gotCommand, query, limit)
			}
			return []SlashArgCandidate{
				{Value: "opus", Display: "Opus"},
				{Value: "sonnet", Display: "Sonnet"},
			}, nil
		},
	})
	runCompletionCmd(t, model, model.startWizard(&def))
	if got := candidateValuesForTUITest(model.slashArgCandidates); !reflect.DeepEqual(got, []string{"opus", "sonnet"}) {
		t.Fatalf("initial async candidates = %#v", got)
	}

	_, cmd := model.handleKey(keyPress("son"))
	if got := candidateValuesForTUITest(model.slashArgCandidates); !reflect.DeepEqual(got, []string{"sonnet"}) {
		t.Fatalf("candidates immediately after typing = %#v, want locally filtered catalog", got)
	}
	runCompletionCmd(t, model, cmd)
	if got := candidateValuesForTUITest(model.slashArgCandidates); !reflect.DeepEqual(got, []string{"sonnet"}) {
		t.Fatalf("candidates after production refresh = %#v, want sonnet", got)
	}
	if calls != 1 {
		t.Fatalf("async catalog backend calls = %d, want one load and local refiltering", calls)
	}
}

func TestAsyncSlashArgCatalogUsesLatestQueryWhenLoadCompletes(t *testing.T) {
	command := "connect-acp-model:" + buildACPConnectWizardPayload(map[string]string{
		"acp_agent": "grok", "acp_launcher": "installed",
	})
	started := make(chan struct{})
	release := make(chan struct{})
	def := WizardDef{
		Command: "async-catalog-in-flight-test",
		Steps: []WizardStepDef{{
			Key:              "model",
			RequireCandidate: true,
			CompletionCommand: func(map[string]string) string {
				return command
			},
		}},
	}
	model := NewModel(Config{
		NoAnimation: true,
		SlashArgComplete: func(_ context.Context, gotCommand string, query string, limit int) ([]SlashArgCandidate, error) {
			if gotCommand != command || query != "" || limit != 200 {
				t.Errorf("SlashArgComplete(%q, %q, %d), want async catalog request", gotCommand, query, limit)
			}
			close(started)
			<-release
			return []SlashArgCandidate{
				{Value: "opus", Display: "Opus"},
				{Value: "sonnet", Display: "Sonnet"},
			}, nil
		},
	})
	loadCmd := model.startWizard(&def)
	if loadCmd == nil {
		t.Fatal("startWizard() = nil, want asynchronous catalog load")
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- loadCmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("async catalog load did not start")
	}

	_, _ = model.handleKey(keyPress("son"))
	if got := model.slashArgQuery; got != "son" {
		t.Fatalf("query while catalog is loading = %q, want son", got)
	}
	close(release)
	_, _ = model.Update(<-result)
	if got := candidateValuesForTUITest(model.slashArgCandidates); !reflect.DeepEqual(got, []string{"sonnet"}) {
		t.Fatalf("candidates after in-flight query change = %#v, want sonnet", got)
	}
	if !model.slashArgCompletionSettledForCurrentTarget() {
		t.Fatal("latest async catalog query was not marked settled")
	}
}

func TestCompletionRequestsRejectStaleResults(t *testing.T) {
	t.Run("file mention", func(t *testing.T) {
		model := NewModel(Config{
			FileComplete: func(_ context.Context, query string, _ int) ([]CompletionCandidate, error) {
				return []CompletionCandidate{{Value: query + ".md", Display: query + ".md"}}, nil
			},
		})
		model.setInputText("@old")
		model.syncTextareaFromInput()
		oldCmd := model.requestMentionCompletion(0)
		model.setInputText("@new")
		model.syncTextareaFromInput()
		newCmd := model.requestMentionCompletion(0)
		_, _ = model.Update(oldCmd())
		if len(model.mentionCandidates) != 0 {
			t.Fatalf("stale mention candidates = %#v", model.mentionCandidates)
		}
		_, _ = model.Update(newCmd())
		if len(model.mentionCandidates) != 1 || model.mentionCandidates[0].Value != "new.md" {
			t.Fatalf("mentionCandidates = %#v, want new.md", model.mentionCandidates)
		}
	})

	t.Run("slash argument", func(t *testing.T) {
		model := NewModel(Config{
			SlashArgComplete: func(_ context.Context, _ string, query string, _ int) ([]SlashArgCandidate, error) {
				return []SlashArgCandidate{{Value: query + "-candidate", Display: query + "-candidate"}}, nil
			},
		})
		model.slashArgActive = true
		model.slashArgCommand = "plugin"
		model.setInputText("/plugin old")
		model.syncTextareaFromInput()
		oldCmd := model.requestSlashArgCompletion()
		model.setInputText("/plugin new")
		model.syncTextareaFromInput()
		newCmd := model.requestSlashArgCompletion()
		_, _ = model.Update(oldCmd())
		if len(model.slashArgCandidates) != 0 {
			t.Fatalf("stale slash argument candidates = %#v", model.slashArgCandidates)
		}
		_, _ = model.Update(newCmd())
		if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "new-candidate" {
			t.Fatalf("slashArgCandidates = %#v, want new-candidate", model.slashArgCandidates)
		}
	})
}

func TestCompletionRequestsCancelSupersededWork(t *testing.T) {
	t.Run("file mention", func(t *testing.T) {
		started := make(chan struct{})
		canceled := make(chan struct{})
		model := NewModel(Config{
			FileComplete: func(ctx context.Context, query string, _ int) ([]CompletionCandidate, error) {
				if query == "old" {
					close(started)
					<-ctx.Done()
					close(canceled)
					return nil, ctx.Err()
				}
				return []CompletionCandidate{{Value: query + ".md", Display: query + ".md"}}, nil
			},
		})
		model.setInputText("@old")
		model.syncTextareaFromInput()
		oldCmd := model.requestMentionCompletion(0)
		oldResult := make(chan tea.Msg, 1)
		go func() { oldResult <- oldCmd() }()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("old mention request did not start")
		}

		model.setInputText("@new")
		model.syncTextareaFromInput()
		newCmd := model.requestMentionCompletion(0)
		select {
		case <-canceled:
		case <-time.After(time.Second):
			t.Fatal("superseded mention context was not canceled")
		}
		_, _ = model.Update(newCmd())
		_, _ = model.Update(<-oldResult)
		if len(model.mentionCandidates) != 1 || model.mentionCandidates[0].Value != "new.md" {
			t.Fatalf("mention candidates after cancellation = %#v", model.mentionCandidates)
		}
	})

	t.Run("slash argument", func(t *testing.T) {
		started := make(chan struct{})
		canceled := make(chan struct{})
		model := NewModel(Config{
			SlashArgComplete: func(ctx context.Context, _ string, query string, _ int) ([]SlashArgCandidate, error) {
				if query == "old" {
					close(started)
					<-ctx.Done()
					close(canceled)
					return nil, ctx.Err()
				}
				return []SlashArgCandidate{{Value: query + "-candidate", Display: query + "-candidate"}}, nil
			},
		})
		model.slashArgActive = true
		model.slashArgCommand = "plugin"
		model.setInputText("/plugin old")
		model.syncTextareaFromInput()
		oldCmd := model.requestSlashArgCompletion()
		oldResult := make(chan tea.Msg, 1)
		go func() { oldResult <- oldCmd() }()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("old slash argument request did not start")
		}

		model.setInputText("/plugin new")
		model.syncTextareaFromInput()
		newCmd := model.requestSlashArgCompletion()
		select {
		case <-canceled:
		case <-time.After(time.Second):
			t.Fatal("superseded slash argument context was not canceled")
		}
		_, _ = model.Update(newCmd())
		_, _ = model.Update(<-oldResult)
		if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "new-candidate" {
			t.Fatalf("slash argument candidates after cancellation = %#v", model.slashArgCandidates)
		}
	})
}

func TestSlashSkillCatalogRejectsResultAfterSessionReset(t *testing.T) {
	model := NewModel(Config{
		Commands: []string{"help"},
		SkillComplete: func(string, int) ([]CompletionCandidate, error) {
			return []CompletionCandidate{{Value: "old-skill", Display: "old-skill"}}, nil
		},
	})
	model.setInputText("/")
	cmd := model.requestSlashSkillCatalog()
	if cmd == nil {
		t.Fatal("requestSlashSkillCatalog() = nil")
	}
	model.resetSlashSkillCatalog()
	_, _ = model.Update(cmd())
	if model.slashSkillLoaded || len(model.slashSkillCatalog) != 0 {
		t.Fatalf("stale skill catalog survived reset: loaded=%v candidates=%#v", model.slashSkillLoaded, model.slashSkillCatalog)
	}
}

func TestComposerPlaceholderIsEmptyAndPromptRemains(t *testing.T) {
	model := NewModel(Config{})
	if model.textarea.Placeholder != "" {
		t.Fatalf("textarea.Placeholder = %q, want a clean empty composer", model.textarea.Placeholder)
	}
	if model.inputPromptPrefix() != "> " {
		t.Fatalf("inputPromptPrefix() = %q, want the user prompt marker", model.inputPromptPrefix())
	}
	if rendered := ansi.Strip(model.renderInputBar()); strings.TrimSpace(rendered) != ">" {
		t.Fatalf("empty composer rendered %q, want only the user prompt marker", rendered)
	}
	if cursor := model.regularInputCursor(); cursor == nil {
		t.Fatal("empty composer cursor = nil, want a usable focused input area")
	}
}

func TestSlashCommandCompletionRefreshesBeforeAcceptingStaleCandidates(t *testing.T) {
	model := NewModel(Config{
		Commands: []string{"alpha", "doctor"},
	})

	_, cmd := model.handleKey(keyPress("/"))
	runCompletionCmd(t, model, cmd)
	if got := model.slashCandidates; len(got) != 2 || got[0] != "/alpha" {
		t.Fatalf("slashCandidates after / = %#v, want stale list starting with /alpha", got)
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

func TestSlashCommandCompletionDoesNotExposeRemovedLeadCommand(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
	})

	model.setInputText("/lea")
	model.syncTextareaFromInput()
	model.refreshSlashCommands()
	if len(model.slashCandidates) != 0 {
		t.Fatalf("slashCandidates = %#v, want removed /lead hidden", model.slashCandidates)
	}
}

func TestSlashCommandTabKeepsArrowSelectedCandidateAcrossRefresh(t *testing.T) {
	model := NewModel(Config{
		Commands: []string{"alpha", "doctor"},
	})
	model.setInputText("/")
	model.syncTextareaFromInput()
	model.refreshSlashCommands()
	if got := model.slashCandidates; len(got) != 2 || got[0] != "/alpha" || got[1] != "/doctor" {
		t.Fatalf("slashCandidates = %#v, want /alpha then /doctor", got)
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

func TestModelPrefixTypingOpensMatchingReasoningPicker(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(_ context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "model":
				return []SlashArgCandidate{
					{Value: "user/model", Display: "user/model"},
					{Value: "deepseek/model", Display: "deepseek/model"},
				}, nil
			case "model deepseek/model":
				return []SlashArgCandidate{
					{Value: "none", Display: "none"},
				}, nil
			default:
				return nil, nil
			}
		},
	})

	model.setInputText("/model de")
	model.syncTextareaFromInput()
	syncSlashInputOverlaysForTest(t, model)

	if got := model.slashArgCommand; got != "model" {
		t.Fatalf("slashArgCommand = %q, want model", got)
	}
	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "deepseek/model" {
		t.Fatalf("slashArgCandidates = %#v, want only deepseek/model", model.slashArgCandidates)
	}

	handled, cmd := model.handleSlashArgKey(keyPress("enter"))
	if !handled {
		t.Fatal("handleSlashArgKey(enter) = false, want true")
	}
	if cmd != nil {
		cmd()
	}
	if got := string(model.input); got != "/model deepseek/model " {
		t.Fatalf("input after /model de enter = %q, want model selection", got)
	}
	if got := model.slashArgCommand; got != "model deepseek/model" {
		t.Fatalf("slashArgCommand after /model de enter = %q, want effort picker", got)
	}
}

func TestModelActionPrefixTypingFiltersCandidatesWhenCursorLags(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(_ context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "model":
				return []SlashArgCandidate{
					{Value: "user/model", Display: "user/model"},
					{Value: "deepseek/model", Display: "deepseek/model"},
				}, nil
			default:
				return nil, nil
			}
		},
	})

	model.setInputText("/model de")
	model.syncTextareaFromInput()
	model.cursor = len([]rune("/model "))
	syncSlashInputOverlaysForTest(t, model)

	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "deepseek/model" {
		t.Fatalf("slashArgCandidates with lagging cursor = %#v, want only deepseek/model", model.slashArgCandidates)
	}
}

func TestModelActionPrefixTypingResetsSelectionToFirstFilteredCandidate(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(_ context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "model":
				return []SlashArgCandidate{
					{Value: "user/model", Display: "user/model"},
					{Value: "deepseek/model", Display: "deepseek/model"},
				}, nil
			default:
				return nil, nil
			}
		},
	})

	runCompletionCmd(t, model, model.openSlashArgPicker("model"))
	model.slashArgIndex = 1
	model.setInputText("/model us")
	model.syncTextareaFromInput()
	syncSlashInputOverlaysForTest(t, model)
	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "user/model" {
		t.Fatalf("slashArgCandidates after /model us = %#v, want only user/model", model.slashArgCandidates)
	}
	if model.currentSlashArgIndex(model.slashArgCandidates) != 0 {
		t.Fatalf("currentSlashArgIndex after /model us = %d, want 0", model.currentSlashArgIndex(model.slashArgCandidates))
	}

	model.slashArgIndex = 0
	model.setInputText("/model de")
	model.syncTextareaFromInput()
	syncSlashInputOverlaysForTest(t, model)
	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "deepseek/model" {
		t.Fatalf("slashArgCandidates after /model de = %#v, want only deepseek/model", model.slashArgCandidates)
	}
	if model.currentSlashArgIndex(model.slashArgCandidates) != 0 {
		t.Fatalf("currentSlashArgIndex after /model de = %d, want 0", model.currentSlashArgIndex(model.slashArgCandidates))
	}
}

func TestResumePrefixTypingUsesControlCompletionResults(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		ResumeComplete: func(_ context.Context, query string, _ int) ([]ResumeCandidate, error) {
			if query != "be" {
				return nil, nil
			}
			return []ResumeCandidate{{SessionID: "beta-session", Prompt: "resume model work", Age: "2m"}}, nil
		},
	})

	model.setInputText("/resume be")
	model.syncTextareaFromInput()
	syncSlashInputOverlaysForTest(t, model)
	completeResumeCandidates(t, model)

	if !model.resumeActive {
		t.Fatal("resume picker not activated")
	}
	if len(model.resumeCandidates) != 1 || model.resumeCandidates[0].SessionID != "beta-session" {
		t.Fatalf("resumeCandidates = %#v, want only beta-session", model.resumeCandidates)
	}
}

func TestResumeCompletionPreservesControlFuzzyMatch(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		ResumeComplete: func(_ context.Context, query string, _ int) ([]ResumeCandidate, error) {
			if query != "gateway" {
				return nil, nil
			}
			return []ResumeCandidate{{SessionID: "session-alpha", Title: "fix the gateway bug"}}, nil
		},
	})

	model.setInputText("/resume gateway")
	model.syncTextareaFromInput()
	syncSlashInputOverlaysForTest(t, model)
	completeResumeCandidates(t, model)

	if len(model.resumeCandidates) != 1 || model.resumeCandidates[0].SessionID != "session-alpha" {
		t.Fatalf("resumeCandidates = %#v, want Control fuzzy match", model.resumeCandidates)
	}
}

func TestResumePrefixTypingUsesControlCompletionResultsWhenCursorLags(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		ResumeComplete: func(_ context.Context, query string, _ int) ([]ResumeCandidate, error) {
			if query != "be" {
				return nil, nil
			}
			return []ResumeCandidate{{SessionID: "beta-session", Prompt: "resume model work", Age: "2m"}}, nil
		},
	})

	model.setInputText("/resume be")
	model.syncTextareaFromInput()
	model.cursor = len([]rune("/resume "))
	syncSlashInputOverlaysForTest(t, model)
	completeResumeCandidates(t, model)

	if !model.resumeActive {
		t.Fatal("resume picker not activated")
	}
	if len(model.resumeCandidates) != 1 || model.resumeCandidates[0].SessionID != "beta-session" {
		t.Fatalf("resumeCandidates with lagging cursor = %#v, want only beta-session", model.resumeCandidates)
	}
}

func TestResumePrefixTypingResetsSelectionToFirstControlCandidate(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		ResumeComplete: func(_ context.Context, query string, _ int) ([]ResumeCandidate, error) {
			if query != "al" {
				return nil, nil
			}
			return []ResumeCandidate{{SessionID: "alpha-session", Prompt: "work on gateway", Age: "1m"}}, nil
		},
	})

	model.activateResumePickerFromInput()
	model.resumeIndex = 1
	model.setInputText("/resume al")
	model.syncTextareaFromInput()
	syncSlashInputOverlaysForTest(t, model)
	completeResumeCandidates(t, model)

	if len(model.resumeCandidates) != 1 || model.resumeCandidates[0].SessionID != "alpha-session" {
		t.Fatalf("resumeCandidates after /resume al = %#v, want only alpha-session", model.resumeCandidates)
	}
	if model.resumeIndex != 0 {
		t.Fatalf("resumeIndex after query change = %d, want 0", model.resumeIndex)
	}
}

func TestResumeCompletionRefreshIsAsyncCancelableAndGenerationSafe(t *testing.T) {
	startedOld := make(chan struct{})
	canceledOld := make(chan struct{})
	model := NewModel(Config{
		Context:  context.Background(),
		Commands: DefaultCommands(),
		ResumeComplete: func(ctx context.Context, query string, _ int) ([]ResumeCandidate, error) {
			switch query {
			case "old":
				close(startedOld)
				<-ctx.Done()
				close(canceledOld)
				return nil, ctx.Err()
			case "new":
				return []ResumeCandidate{{SessionID: "new-session", Prompt: "new result"}}, nil
			default:
				return nil, nil
			}
		},
	})
	model.setInputText("/resume old")
	model.syncTextareaFromInput()
	syncSlashInputOverlaysForTest(t, model)
	model.completionRefreshSeq = 1

	updated, oldCmd := model.handleCompletionRefreshMsg(completionRefreshMsg{seq: 1})
	if updated != model || oldCmd == nil {
		t.Fatalf("completion refresh = (%T, %v), want same model and async Cmd", updated, oldCmd)
	}
	if duplicate := model.updateResumeCandidates(); duplicate != nil {
		t.Fatal("same pending resume query scheduled a duplicate request")
	}
	oldResult := make(chan tea.Msg, 1)
	go func() { oldResult <- oldCmd() }()
	select {
	case <-startedOld:
	case <-time.After(time.Second):
		t.Fatal("old completion backend did not start")
	}

	updateStarted := time.Now()
	_, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if elapsed := time.Since(updateStarted); elapsed > 50*time.Millisecond {
		t.Fatalf("WindowSize Update blocked behind completion for %s", elapsed)
	}
	model.setInputText("/resume new")
	model.syncTextareaFromInput()
	syncSlashInputOverlaysForTest(t, model)
	model.completionRefreshSeq = 2
	_, newCmd := model.handleCompletionRefreshMsg(completionRefreshMsg{seq: 2})
	if newCmd == nil {
		t.Fatal("new query did not schedule completion")
	}
	select {
	case <-canceledOld:
	case <-time.After(time.Second):
		t.Fatal("old completion context was not canceled by the new query")
	}
	newMsg := newCmd()
	if _, _ = model.Update(newMsg); len(model.resumeCandidates) != 1 || model.resumeCandidates[0].SessionID != "new-session" {
		t.Fatalf("new resume candidates = %#v", model.resumeCandidates)
	}
	select {
	case msg := <-oldResult:
		_, _ = model.Update(msg)
	case <-time.After(time.Second):
		t.Fatal("canceled completion did not return")
	}
	if len(model.resumeCandidates) != 1 || model.resumeCandidates[0].SessionID != "new-session" {
		t.Fatalf("stale result replaced new candidates: %#v", model.resumeCandidates)
	}
}

func TestClosingResumePickerRejectsLateCompletionAndAcceptRetriesAsync(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	called := make(chan struct{}, 1)
	model := NewModel(Config{
		Context:  context.Background(),
		Commands: DefaultCommands(),
		ResumeComplete: func(_ context.Context, _ string, _ int) ([]ResumeCandidate, error) {
			called <- struct{}{}
			close(started)
			<-release
			return []ResumeCandidate{{SessionID: "late-session", Prompt: "late"}}, nil
		},
	})
	model.setInputText("/resume late")
	model.syncTextareaFromInput()
	syncSlashInputOverlaysForTest(t, model)
	_, cmd := model.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if cmd == nil {
		t.Fatal("accept with no candidates did not schedule an asynchronous retry")
	}
	select {
	case <-called:
		t.Fatal("accept invoked ResumeComplete before an asynchronous refresh")
	default:
	}

	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("completion backend did not start")
	}
	model.clearResume()
	close(release)
	select {
	case msg := <-result:
		_, _ = model.Update(msg)
	case <-time.After(time.Second):
		t.Fatal("late completion did not return")
	}
	if model.resumeActive || len(model.resumeCandidates) != 0 {
		t.Fatalf("late completion reopened picker: active=%v candidates=%#v", model.resumeActive, model.resumeCandidates)
	}
}

func TestModelActionPrefixTypingFiltersCandidatesDuringLiveInput(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(_ context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "model":
				return []SlashArgCandidate{
					{Value: "user/model", Display: "user/model"},
					{Value: "deepseek/model", Display: "deepseek/model"},
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
	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "deepseek/model" {
		t.Fatalf("slashArgCandidates after live input = %#v, want only deepseek/model", model.slashArgCandidates)
	}
}

func TestCompletionRefreshDoesNotBlockTypingPath(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(_ context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
			time.Sleep(200 * time.Millisecond)
			return []SlashArgCandidate{{Value: "del", Display: "del"}}, nil
		},
	})

	cmd := requireTestCallReturns(t, "batched slash input", func() tea.Cmd {
		_, cmd := model.handleKey(keyPress("/model de"))
		return cmd
	})
	if cmd == nil {
		t.Fatal("handleKey() should schedule async completion refresh")
	}
}

func TestModelActionPrefixTypingUsesTextareaValueAsSourceOfTruth(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(_ context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "model":
				return []SlashArgCandidate{
					{Value: "user/model", Display: "user/model"},
					{Value: "deepseek/model", Display: "deepseek/model"},
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
	syncSlashInputOverlaysForTest(t, model)

	if got := model.slashArgCommand; got != "model" {
		t.Fatalf("slashArgCommand = %q, want model", got)
	}
	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "deepseek/model" {
		t.Fatalf("slashArgCandidates from textarea source = %#v, want only deepseek/model", model.slashArgCandidates)
	}
}

func TestResumePrefixTypingUsesTextareaValueAsSourceOfTruth(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		ResumeComplete: func(_ context.Context, query string, _ int) ([]ResumeCandidate, error) {
			if query != "be" {
				return nil, nil
			}
			return []ResumeCandidate{{SessionID: "beta-session", Prompt: "resume model work", Age: "2m"}}, nil
		},
	})

	model.input = []rune("/resume ")
	model.cursor = len(model.input)
	model.textarea.SetValue("/resume be")
	model.textarea.CursorEnd()
	syncSlashInputOverlaysForTest(t, model)
	completeResumeCandidates(t, model)

	if !model.resumeActive {
		t.Fatal("resume picker not activated")
	}
	if len(model.resumeCandidates) != 1 || model.resumeCandidates[0].SessionID != "beta-session" {
		t.Fatalf("resumeCandidates from textarea source = %#v, want only beta-session", model.resumeCandidates)
	}
}

func TestSlashCompletionRendersDescriptionsWithoutHeaderOrBorder(t *testing.T) {
	model := NewModel(Config{Commands: []string{"model", "status"}})
	model.width = 79 // 设为 79 以测试无边框响应式降级
	model.slashCandidates = []string{"/model", "/status"}
	model.slashIndex = 0

	rendered := ansi.Strip(model.renderSlashCommandList())
	if strings.Contains(rendered, "Commands") {
		t.Fatalf("renderSlashCommandList() = %q, should not show a header", rendered)
	}
	if strings.ContainsAny(rendered, "┌┐└┘│─") || strings.ContainsAny(rendered, "╭╮╰╯│─") {
		t.Fatalf("renderSlashCommandList() = %q, should not show borders", rendered)
	}
	for _, want := range []string{"/model", "Choose the model, effort", "/status", "Show current provider"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("renderSlashCommandList() = %q, want %q", rendered, want)
		}
	}
	lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("renderSlashCommandList() lines = %#v, want two candidate rows plus footer", lines)
	}
	if !strings.Contains(lines[len(lines)-1], "select") {
		t.Fatalf("renderSlashCommandList() footer = %q, want unified overlay footer", lines[len(lines)-1])
	}
	for _, line := range lines[:len(lines)-1] {
		if width := displayColumns(line); width != model.completionOverlayRenderedRowWidth() {
			t.Fatalf("renderSlashCommandList() row width = %d, want %d: %q", width, model.completionOverlayRenderedRowWidth(), line)
		}
	}

	// 测试大屏幕宽度 >=80 时有边框
	model.width = 120
	renderedWithBorder := ansi.Strip(model.renderSlashCommandList())
	if !strings.ContainsAny(renderedWithBorder, "┌┐└┘│─") && !strings.ContainsAny(renderedWithBorder, "╭╮╰╯│─") {
		t.Fatalf("renderSlashCommandList() with width 120 = %q, expected to show borders", renderedWithBorder)
	}
}

func TestProfileSlashCompletionShowsBoundProviderModelButExecutesStableProfile(t *testing.T) {
	model := NewModel(Config{
		Commands:       []string{"breeze"},
		CommandDetails: map[string]string{"breeze": "openai-codex/gpt-5.6-sol [high]"},
	})
	model.width = 120
	model.setInputText("/")
	model.syncTextareaFromInput()
	model.refreshSlashCommands()
	if got := ansi.Strip(model.renderSlashCommandList()); !strings.Contains(got, "/breeze") || !strings.Contains(got, "openai-codex/gpt-5.6-sol") {
		t.Fatalf("renderSlashCommandList() = %q, want profile and bound model display", got)
	}
	model.applySlashCommandCompletion()
	model.syncTextareaFromInput()
	if got := model.textarea.Value(); got != "/breeze " {
		t.Fatalf("completed command = %q, want stable executable /breeze", got)
	}
}

func TestWizardSuppressesRootSlashCompletion(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		Wizards:  DefaultWizards(),
	})
	def := model.findWizard("connect")
	if def == nil {
		t.Fatalf("connect wizard not found")
	}
	model.wizard = &wizardRuntime{
		def:       def,
		stepIndex: 0,
		state:     map[string]string{},
	}
	model.slashArgActive = true
	model.slashArgCommand = "connect"
	model.setInputText("/")
	model.syncTextareaFromInput()

	model.refreshCompletionOverlaysNow()
	if len(model.slashCandidates) != 0 {
		t.Fatalf("slashCandidates = %#v, want none while wizard is active", model.slashCandidates)
	}
}

func TestModelCompletionUsesWideDisplayForLongAliases(t *testing.T) {
	const alias = "openai-compatible/team-platform-evaluation-gpt-5.5-coding-preview-with-extended-context-and-tool-router"

	model := NewModel(Config{Commands: DefaultCommands()})
	model.width = 140
	model.slashArgActive = true
	model.slashArgCommand = "model"
	model.slashArgCandidates = []SlashArgCandidate{
		{Value: alias, Display: alias, Detail: "configured model alias"},
	}
	model.slashArgIndex = 0

	rendered := ansi.Strip(model.renderSlashArgList())
	if !strings.Contains(rendered, alias) {
		t.Fatalf("renderSlashArgList() = %q, want full model alias %q", rendered, alias)
	}
	for _, unwanted := range []string{"configured model alias", "endpoint:", "managed auth"} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("renderSlashArgList() = %q, should not paint %q", rendered, unwanted)
		}
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
		{Value: "docs/readme.md", Display: "docs/readme.md", Detail: "file"},
	}
	model.mentionIndex = 0
	cases["file"] = model.renderMentionList()

	model.slashArgCandidates = []SlashArgCandidate{
		{Value: "gpt-5.5", Display: "gpt-5.5", Detail: "configured model alias"},
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
				FileComplete: func(_ context.Context, query string, limit int) ([]CompletionCandidate, error) {
					return []CompletionCandidate{
						{Value: "docs/", Display: "docs/", Detail: "directory"},
						{Value: "docs/message.sql", Display: "docs/message.sql", Detail: "file"},
					}, nil
				},
			})

			model.setInputText("@docs/")
			model.syncTextareaFromInput()
			runCompletionCmd(t, model, model.requestMentionCompletion(0))
			if len(model.mentionCandidates) != 2 {
				t.Fatalf("mentionCandidates = %#v, want two file candidates", model.mentionCandidates)
			}
			model.mentionIndex = 1

			_, cmd := model.handleKey(keyPress(keyName))
			runCompletionCmd(t, model, cmd)

			if got := string(model.input); got != "@docs/message.sql " {
				t.Fatalf("input after %s = %q, want selected file path", keyName, got)
			}
		})
	}
}

func TestFileCompletionListHidesPrefixAndTypeDetail(t *testing.T) {
	model := NewModel(Config{})
	model.width = 120
	model.mentionPrefix = "@"
	model.mentionCandidates = []CompletionCandidate{
		{Value: "docs/", Display: "docs/", Detail: "directory"},
		{Value: "docs/message.sql", Display: "docs/message.sql", Detail: "file"},
		{Value: "docs/providers/openai-compatible/base-url-reference.md", Display: "docs/providers/openai-compatible/base-url-reference.md", Detail: "file"},
	}

	rendered := ansi.Strip(model.renderMentionList())

	for _, unwanted := range []string{"@docs/", "@docs/message.sql", "@docs/providers", "directory", "file"} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("renderMentionList() = %q, should not contain %q", rendered, unwanted)
		}
	}
	for _, want := range []string{"docs/", "docs/message.sql", "docs/providers/openai-compatible/base-url-reference.md"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("renderMentionList() = %q, want %q", rendered, want)
		}
	}
}

func TestFileCompletionFetchesBeyondVisibleWindowAndScrolls(t *testing.T) {
	var gotLimit int
	model := NewModel(Config{
		FileComplete: func(_ context.Context, query string, limit int) ([]CompletionCandidate, error) {
			gotLimit = limit
			return numberedCompletionCandidates("file", 12), nil
		},
	})

	model.setInputText("@")
	model.syncTextareaFromInput()
	runCompletionCmd(t, model, model.requestMentionCompletion(0))
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
	if strings.Contains(rendered, "earlier") || strings.Contains(rendered, "more") {
		t.Fatalf("renderMentionList() = %q, should not contain scroll text rows", rendered)
	}
	if !strings.Contains(ansi.Strip(model.renderMentionList()), "select") {
		t.Fatalf("renderMentionList() should include unified overlay footer")
	}
}

func TestFileCompletionLoadsNextPageAtBottomThenWrapsWhenExhausted(t *testing.T) {
	var limits []int
	model := NewModel(Config{
		FileComplete: func(_ context.Context, query string, limit int) ([]CompletionCandidate, error) {
			limits = append(limits, limit)
			return numberedCompletionCandidates("file", minInt(limit, 65)), nil
		},
	})

	model.setInputText("@")
	model.syncTextareaFromInput()
	runCompletionCmd(t, model, model.requestMentionCompletion(0))
	if len(model.mentionCandidates) != completionCandidateFetchLimit {
		t.Fatalf("mentionCandidates = %d, want initial page of %d", len(model.mentionCandidates), completionCandidateFetchLimit)
	}

	model.mentionIndex = len(model.mentionCandidates) - 1
	handled, cmd := model.handleMentionKey(keyPress("down"))
	if !handled {
		t.Fatal("handleMentionKey(down) = false, want true")
	}
	runCompletionCmd(t, model, cmd)
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
	handled, cmd = model.handleMentionKey(keyPress("down"))
	if !handled || cmd != nil {
		t.Fatalf("handleMentionKey(down exhausted) = handled:%v cmd:%v, want wrapped selection without request", handled, cmd != nil)
	}
	if len(limits) != callCount {
		t.Fatalf("FileComplete called after exhausted page: %v", limits)
	}
	if model.mentionIndex != 0 {
		t.Fatalf("mentionIndex after exhausted down = %d, want wrap to 0", model.mentionIndex)
	}
}

func TestFileCompletionPagingFailurePreservesCurrentPage(t *testing.T) {
	calls := 0
	model := NewModel(Config{
		FileComplete: func(_ context.Context, _ string, limit int) ([]CompletionCandidate, error) {
			calls++
			if calls > 1 {
				return nil, errors.New("temporary paging failure")
			}
			return numberedCompletionCandidates("file", limit), nil
		},
	})
	model.setInputText("@")
	model.syncTextareaFromInput()
	runCompletionCmd(t, model, model.requestMentionCompletion(0))
	model.mentionIndex = len(model.mentionCandidates) - 1

	handled, cmd := model.handleMentionKey(keyPress("down"))
	if !handled || cmd == nil {
		t.Fatalf("paging request = handled:%v cmd:%v", handled, cmd != nil)
	}
	runCompletionCmd(t, model, cmd)
	if len(model.mentionCandidates) != completionCandidateFetchLimit {
		t.Fatalf("mentionCandidates after failed page = %d, want preserved %d", len(model.mentionCandidates), completionCandidateFetchLimit)
	}
	if model.mentionIndex != completionCandidateFetchLimit-1 {
		t.Fatalf("mentionIndex after failed page = %d, want preserved last item", model.mentionIndex)
	}
	if model.mentionLimit != completionCandidateFetchLimit {
		t.Fatalf("mentionLimit after failed page = %d, want retryable %d", model.mentionLimit, completionCandidateFetchLimit)
	}
}

func TestRenderResumeListFallsBackToSessionIDWithoutTitle(t *testing.T) {
	model := NewModel(Config{Commands: DefaultCommands()})
	model.width = 120
	model.resumeCandidates = []ResumeCandidate{{SessionID: "ordinary-untitled-session", Age: "1m ago"}}
	model.resumeActive = true

	rendered := ansi.Strip(model.renderResumeList())
	if !strings.Contains(rendered, "ordinary-untitled-session") {
		t.Fatalf("renderResumeList() = %q, want untitled Session ID", rendered)
	}
}

func TestRenderResumeListShowsTitleAndAge(t *testing.T) {
	model := NewModel(Config{Commands: DefaultCommands()})
	model.width = 120
	model.resumeCandidates = []ResumeCandidate{
		{
			SessionID: "session-123",
			Title:     "Gateway cleanup",
			Model:     "openai/gpt-4o-mini",
			Workspace: "/tmp/workspace-alpha",
			Age:       "2h44m5s ago",
		},
		{
			SessionID: "session-456",
			Title:     "A longer planning session",
			Model:     "anthropic/claude",
			Workspace: "/tmp/workspace-beta",
			Age:       "25h ago",
		},
	}
	model.resumeActive = true

	rendered := ansi.Strip(model.renderResumeList())
	for _, want := range []string{"Gateway cleanup", "A longer planning session", "2h ago", "1d ago"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("renderResumeList() = %q, want substring %q", rendered, want)
		}
	}
	for _, unwanted := range []string{"openai/gpt-4o-mini", "anthropic/claude", "workspace-alpha", "workspace-beta", "session-123", "session-456", "id:session-123", "2h44m5s", "25h ago"} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("renderResumeList() = %q, should not contain %q", rendered, unwanted)
		}
	}
	lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
	first := lineContaining(lines, "Gateway cleanup")
	second := lineContaining(lines, "A longer planning session")
	if first == "" || second == "" {
		t.Fatalf("renderResumeList() lines = %#v, want both resume rows", lines)
	}
	if got, want := strings.Index(first, "2h ago"), strings.Index(second, "1d ago"); got != want {
		t.Fatalf("resume age columns = %d and %d, want aligned\nfirst=%q\nsecond=%q", got, want, first, second)
	}
	if gap := strings.Index(second, "1d ago") - (strings.Index(second, "A longer planning session") + len("A longer planning session")); gap > 4 {
		t.Fatalf("resume age gap after longest title = %d, want compact column spacing\nsecond=%q", gap, second)
	}
}

func TestRenderResumeListUsesWideDisplayForLongTitles(t *testing.T) {
	const title = "Investigate completion drawer width allocation across connect model resume and file candidate surfaces"

	model := NewModel(Config{Commands: DefaultCommands()})
	model.width = 140
	model.resumeCandidates = []ResumeCandidate{
		{
			SessionID: "session-very-long-title",
			Title:     title,
			Model:     "openai/gpt-4o-mini",
			Workspace: "/tmp/workspace-alpha",
			Age:       "1h ago",
		},
	}
	model.resumeActive = true

	rendered := ansi.Strip(model.renderResumeList())
	if !strings.Contains(rendered, title) {
		t.Fatalf("renderResumeList() = %q, want full title %q", rendered, title)
	}
	if !strings.Contains(rendered, "1h ago") {
		t.Fatalf("renderResumeList() = %q, want age", rendered)
	}
	row := lineContaining(strings.Split(strings.TrimRight(rendered, "\n"), "\n"), title)
	if row == "" {
		t.Fatalf("renderResumeList() = %q, want title row", rendered)
	}
	if gap := strings.Index(row, "1h ago") - (strings.Index(row, title) + len(title)); gap > 4 {
		t.Fatalf("resume age gap after title = %d, want compact column spacing\nrow=%q", gap, row)
	}
	for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
		if width := displayColumns(line); width > model.completionOverlayInnerWidth() {
			t.Fatalf("renderResumeList() row width = %d, want <= %d: %q", width, model.completionOverlayInnerWidth(), line)
		}
	}
}

func TestRenderResumeListKeepsCJKSessionNamesVisibleAtNarrowWidth(t *testing.T) {
	model := NewModel(Config{Commands: DefaultCommands(), ColorProfile: colorprofile.TrueColor})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 82, Height: 30})
	model = updated.(*Model)
	model.resumeCandidates = []ResumeCandidate{
		{SessionID: "session-one", Title: strings.Repeat("处理窄终端恢复会话名称显示异常", 4), Age: "now"},
		{SessionID: "session-two", Title: strings.Repeat("请核对工作树状态并继续完成修复", 4), Age: "1h ago"},
	}
	model.resumeActive = true

	rendered := ansi.Strip(model.renderResumeList())
	for _, want := range []string{"处理窄终端", "请核对工作树", "now", "1h ago"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("narrow renderResumeList() omitted %q:\n%s", want, rendered)
		}
	}
	for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
		if width := displayColumns(line); width > model.width {
			t.Fatalf("narrow resume row width = %d, terminal width = %d: %q", width, model.width, line)
		}
	}
	frame := model.View().Content
	updates := renderFullscreenFramesForTest(t, model.width, model.height, frame)
	assertPhysicalFullscreenFrame(t, model.width, model.height, frame, updates)
}

func lineContaining(lines []string, needle string) string {
	for _, line := range lines {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

func TestModelActionPrefixTypingFiltersCandidatesDuringPaste(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(_ context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "model":
				return []SlashArgCandidate{
					{Value: "user/model", Display: "user/model"},
					{Value: "deepseek/model", Display: "deepseek/model"},
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
	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "deepseek/model" {
		t.Fatalf("slashArgCandidates after paste = %#v, want only deepseek/model", model.slashArgCandidates)
	}
}

func TestModelActionPrefixTypingFiltersCandidatesWhenTerminalBatchesInput(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(_ context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "model":
				return []SlashArgCandidate{
					{Value: "user/model", Display: "user/model"},
					{Value: "deepseek/model", Display: "deepseek/model"},
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
	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "deepseek/model" {
		t.Fatalf("slashArgCandidates after batched key = %#v, want only deepseek/model", model.slashArgCandidates)
	}
}

func TestModelActionPrefixTypingFiltersCandidatesAfterSlashThenBatchedTail(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		SlashArgComplete: func(_ context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
			switch command {
			case "model":
				return []SlashArgCandidate{
					{Value: "user/model", Display: "user/model"},
					{Value: "deepseek/model", Display: "deepseek/model"},
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
	if len(model.slashArgCandidates) != 1 || model.slashArgCandidates[0].Value != "deepseek/model" {
		t.Fatalf("slashArgCandidates after slash + batched tail = %#v, want only deepseek/model", model.slashArgCandidates)
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

func requireTestCallReturns[T any](t *testing.T, name string, call func() T) T {
	t.Helper()
	result := make(chan T, 1)
	go func() { result <- call() }()
	select {
	case value := <-result:
		return value
	case <-time.After(time.Second):
		t.Fatalf("%s blocked", name)
		var zero T
		return zero
	}
}

func requireTestActionReturns(t *testing.T, name string, call func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		call()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("%s blocked", name)
	}
}

func syncSlashInputOverlaysForTest(t *testing.T, model *Model) {
	t.Helper()
	model.syncSlashInputOverlayState()
	if !model.slashArgActive {
		return
	}
	model.dropStaleSlashArgCandidates()
	runCompletionCmd(t, model, model.requestCurrentSlashArgCompletion())
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
		updated, nextCmd := model.Update(typed)
		if next, ok := updated.(*Model); ok && next != model {
			*model = *next
		}
		runCompletionCmd(t, model, nextCmd)
	case tea.BatchMsg:
		for _, sub := range typed {
			runCompletionCmd(t, model, sub)
		}
	default:
		updated, nextCmd := model.Update(typed)
		if next, ok := updated.(*Model); ok && next != model {
			*model = *next
		}
		runCompletionCmd(t, model, nextCmd)
	}
}

func completeResumeCandidates(t *testing.T, model *Model) {
	t.Helper()
	runCompletionCmd(t, model, model.updateResumeCandidates())
}

func loadSlashSkillCatalog(t *testing.T, model *Model) {
	t.Helper()
	cmd := model.requestSlashSkillCatalog()
	if cmd == nil {
		t.Fatal("requestSlashSkillCatalog() = nil")
	}
	updated, _ := model.Update(cmd())
	if next, ok := updated.(*Model); ok && next != model {
		*model = *next
	}
}

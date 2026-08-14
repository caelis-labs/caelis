package tuiapp

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestWizardFreeformEnterUsesLatestInputBeforeDebounce(t *testing.T) {
	var submitted string
	model := NewModel(Config{
		ExecuteLine: func(submission Submission) TaskResultMsg {
			submitted = submission.Text
			return TaskResultMsg{}
		},
		Wizards: []WizardDef{{
			Command: "custom",
			Steps: []WizardStepDef{{
				Key:          "value",
				NoCompletion: true,
				CompletionCommand: func(map[string]string) string {
					return "custom"
				},
			}},
			BuildExecLine: func(state map[string]string) string {
				return "/custom " + state["value"]
			},
		}},
	})
	model.startWizard(model.findWizard("custom"))

	_, _ = model.handleKey(keyPress("fast-value"))
	_, cmd := model.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if cmd == nil || !findAndRunTaskResult(cmd(), model) {
		t.Fatalf("free-form Enter did not submit the wizard: cmd=%v query=%q input=%q active=%v", cmd != nil, model.slashArgQuery, model.textarea.Value(), model.isWizardActive())
	}
	if submitted != "/custom fast-value" {
		t.Fatalf("submitted = %q, want latest free-form input", submitted)
	}
}

func TestHandleWizardEnterPrefersTypedCustomValueOverSelectedCandidate(t *testing.T) {
	var observed string
	model := NewModel(Config{
		Wizards: []WizardDef{{
			Command:     "connect",
			DisplayLine: "/connect",
			Steps: []WizardStepDef{{
				Key:       "model",
				HintLabel: "/connect model",
			}},
			OnStepConfirm: func(stepKey string, value string, candidate *SlashArgCandidate, state map[string]string) {
				observed = value
			},
			BuildExecLine: func(state map[string]string) string {
				return state["model"]
			},
		}},
	})
	model.startWizard(model.findWizard("connect"))
	model.slashArgCandidates = []SlashArgCandidate{{
		Value:   "minimax/MiniMax-M1",
		Display: "minimax/MiniMax-M1",
	}}
	model.slashArgIndex = 0
	model.slashArgQuery = "custom-model"

	handled, cmd := model.handleWizardEnter()
	if !handled {
		t.Fatalf("handleWizardEnter() = not handled")
	}
	if cmd != nil {
		cmd()
	}
	if observed != "custom-model" {
		t.Fatalf("observed = %q, want custom-model", observed)
	}
}

package tuiapp

import "testing"

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

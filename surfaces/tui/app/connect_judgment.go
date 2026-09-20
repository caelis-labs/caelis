package tuiapp

// connectJudgmentWizard keeps evaluation onboarding independent of chat-only
// image, output-generation, and reasoning controls.
func connectJudgmentWizard() WizardDef {
	wizard := connectModelWizard("judgment")
	steps := wizard.Steps[:0]
	for _, step := range wizard.Steps {
		switch step.Key {
		case "provider", "endpoint", "baseurl", "apikey", "model":
			steps = append(steps, step)
		}
	}
	wizard.Steps = steps
	return wizard
}

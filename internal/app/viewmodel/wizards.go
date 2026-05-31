package viewmodel

type WizardFlowView struct {
	Command     string           `json:"command,omitempty"`
	DisplayLine string           `json:"display_line,omitempty"`
	Steps       []WizardStepView `json:"steps,omitempty"`
}

type WizardStepView struct {
	Key                 string `json:"key,omitempty"`
	HintLabel           string `json:"hint_label,omitempty"`
	FreeformHint        string `json:"freeform_hint,omitempty"`
	DynamicFreeformHint bool   `json:"dynamic_freeform_hint,omitempty"`
	HideInput           bool   `json:"hide_input,omitempty"`
	NoCompletion        bool   `json:"no_completion,omitempty"`
	RequireCandidate    bool   `json:"require_candidate,omitempty"`
	Validator           string `json:"validator,omitempty"`
}

const WizardValidatorInt = "int"

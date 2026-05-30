package viewmodel

type CommandCatalogView struct {
	Commands []CommandView `json:"commands,omitempty"`
}

type CommandView struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	InputHint   string `json:"input_hint,omitempty"`
}

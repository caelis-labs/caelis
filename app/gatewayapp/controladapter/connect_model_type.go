package controladapter

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

// Model type follows endpoint discovery after model selection. Existing
// connection commands retain their protocol names and identity semantics.
func completeConnectModelType(ctx context.Context, driver *assembler, payload connectWizardPayload, query string, limit int) ([]controlprompt.SlashArgCandidate, error) {
	chat, judgment := false, modelconfig.IsJudgmentProvider(payload.Provider)
	if template, ok := modelconfig.LookupProvider(payload.Provider); ok && template.Provider == "magpie" {
		models, err := connectModelCatalog(ctx, driver, payload, true)
		if err != nil {
			return nil, err
		}
		selected := map[string]bool{}
		for _, name := range strings.Split(payload.Model, ",") {
			selected[strings.TrimSpace(name)] = true
		}
		for _, entry := range models {
			if selected[entry.Name] {
				delete(selected, entry.Name)
				judgment = judgment || entry.Judgment
				chat = chat || !entry.Judgment
			}
		}
		if len(selected) != 0 {
			return nil, fmt.Errorf("selected model is absent from the Magpie catalog")
		}
	} else {
		chat = !judgment
	}
	value, display, detail := "chat", "Chat model", "Conversation and tools"
	if judgment {
		value, display, detail = "judgment", "Judgment model", "Typed evaluation"
		if chat {
			value, display, detail = "auto", "Chat and judgment models", "Keep each model's declared type"
		}
	}
	return filterSlashArgCandidates([]controlprompt.SlashArgCandidate{{Value: value, Display: display, Detail: detail}}, query, limit), nil
}

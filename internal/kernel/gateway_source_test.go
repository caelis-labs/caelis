package kernel

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestACPFinalAssistantMaterializationIgnoresAuditSource(t *testing.T) {
	t.Parallel()

	message := model.NewTextMessage(model.RoleAssistant, "done")
	for _, source := range []string{"acp", "slash", "renamed-product-source"} {
		event := &session.Event{
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityCanonical,
			Message:    &message,
			Scope:      &session.EventScope{Source: source},
		}
		if !isACPFinalAssistantMaterialization(event) {
			t.Fatalf("source %q changed final materialization classification", source)
		}
	}
}

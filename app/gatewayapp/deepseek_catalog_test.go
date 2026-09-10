package gatewayapp

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
)

func TestConfiguredDeepSeekLegacyModelsRemainSelectableAndDeletable(t *testing.T) {
	for _, name := range []string{"deepseek-v4-flash", "deepseek-v4-flash-vision-exp", "deepseek-v4-pro"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			stack, _ := newLocalStateTestStack(t)
			profile, err := stack.connectTestModel(ModelConfig{
				Provider: "deepseek", Model: name, Token: "test-key",
			})
			if err != nil {
				t.Fatal(err)
			}
			id := profile.Backend.Provider.ModelConfigID
			choices, err := stack.Models().ListChoices(ctx, session.SessionRef{})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, choice := range choices {
				if choice.ID == id {
					found = true
				}
			}
			if !found {
				t.Fatalf("configured legacy model %q missing from choices", id)
			}
			revision, err := stack.ControlStatus().ConfigurationRevision(ctx)
			if err != nil {
				t.Fatal(err)
			}
			principal := appserver.Principal{ID: stack.UserID()}
			used, err := stack.ConfigurationCommands().UseModel(ctx, principal, appserver.UseModelRequest{
				WriteBase: appserver.WriteBase{OperationID: "select-legacy", ExpectedRevision: &revision},
				Model:     id,
			})
			if err != nil || used.Outcome != appserver.OutcomeCommitted {
				t.Fatalf("UseModel(%q) = %#v, %v", id, used, err)
			}
			if stack.composition.lookup.DefaultID() != id {
				t.Fatalf("selected identity changed, want %q", id)
			}
			deleted, err := stack.ConfigurationCommands().DeleteModel(ctx, principal, appserver.DeleteModelRequest{
				WriteBase: appserver.WriteBase{OperationID: "delete-legacy", ExpectedRevision: &used.Revision},
				Model:     id,
			})
			if err != nil || deleted.Outcome != appserver.OutcomeCommitted {
				t.Fatalf("DeleteModel(%q) = %#v, %v", id, deleted, err)
			}
			if _, ok := stack.composition.lookup.Config(id); ok {
				t.Fatalf("deleted model %q still configured", id)
			}
		})
	}
}

package gatewayapp

import (
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
)

func TestPresentationConfigOptionsFromACPStandardVariants(t *testing.T) {
	choices := acpsdk.SessionConfigSelectOptionsUngrouped{{Value: "mimo", Name: "MiMo"}}
	category := acpsdk.SessionConfigOptionCategoryModel
	options := presentationConfigOptionsFromACP([]acpsdk.SessionConfigOption{
		{Select: &acpsdk.SessionConfigOptionSelect{
			Type: "select", Id: "model", Name: "Model", Category: &category, CurrentValue: "mimo",
			Options: acpsdk.SessionConfigSelectOptions{Ungrouped: &choices},
		}},
		{Boolean: &acpsdk.SessionConfigOptionBoolean{
			Type: "boolean", Id: "verbose", Name: "Verbose", CurrentValue: true,
		}},
	})
	if len(options) != 2 {
		t.Fatalf("len(config options) = %d, want 2", len(options))
	}
	if got := options[0]; got.Type != "select" || got.ID != "model" || got.CurrentValue != "mimo" || len(got.Options) != 1 {
		t.Fatalf("select option = %#v, want complete select projection", got)
	}
	if got := options[1]; got.Type != "boolean" || got.ID != "verbose" || got.CurrentValue != true {
		t.Fatalf("boolean option = %#v, want true boolean projection", got)
	}
}

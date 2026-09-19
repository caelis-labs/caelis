package sessionconfig

import (
	"reflect"
	"testing"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

func TestApplyExplicitConfigMatchingDisplayedDefault(t *testing.T) {
	for _, value := range []string{"default", "priority"} {
		t.Run(value, func(t *testing.T) {
			options := tierConfigOptions("a", value, true)
			acpClient := &fakeClient{responses: []client.SetSessionConfigOptionResponse{{ConfigOptions: options}}}
			_, err := Apply(t.Context(), acpClient, "session", State{ConfigOptions: options}, controlagents.SessionOptions{ConfigValues: map[string]string{"service_tier": value}})
			if err != nil {
				t.Fatal(err)
			}
			if want := []string{"config:session:service_tier:" + value}; !reflect.DeepEqual(acpClient.calls, want) {
				t.Fatalf("explicit selection calls = %v, want %v", acpClient.calls, want)
			}
			acpClient.calls = nil
			if _, err := Apply(t.Context(), acpClient, "session", State{ConfigOptions: options}, controlagents.SessionOptions{}); err != nil {
				t.Fatal(err)
			}
			if len(acpClient.calls) != 0 {
				t.Fatalf("inherited tier produced a setter: %v", acpClient.calls)
			}
		})
	}
}

func TestApplyStandardBeforeModel(t *testing.T) {
	for _, hasTier := range []bool{true, false} {
		name := "current_tier_advertised"
		if !hasTier {
			name = "tier_advertised_only_after_model"
		}
		t.Run(name, func(t *testing.T) {
			initial := tierConfigOptions("a", "priority", true)
			acpClient := &fakeClient{}
			want := []string{}
			if hasTier {
				acpClient.responses = append(acpClient.responses, client.SetSessionConfigOptionResponse{ConfigOptions: tierConfigOptions("a", "default", true)})
				want = append(want, "config:session:service_tier:default")
			} else {
				initial = initial[:1]
			}
			acpClient.responses = append(acpClient.responses,
				client.SetSessionConfigOptionResponse{ConfigOptions: tierConfigOptions("b", "default", false)},
				client.SetSessionConfigOptionResponse{ConfigOptions: tierConfigOptions("b", "default", false)},
			)
			_, err := Apply(t.Context(), acpClient, "session", State{ConfigOptions: initial}, controlagents.SessionOptions{ModelID: "b", ConfigValues: map[string]string{"service_tier": "default"}})
			if err != nil {
				t.Fatal(err)
			}
			want = append(want, "config:session:model:b", "config:session:service_tier:default")
			if !reflect.DeepEqual(acpClient.calls, want) {
				t.Fatalf("calls = %v, want %v", acpClient.calls, want)
			}
		})
	}
}

func TestApplyStandardTransitionFailsBeforeModelMutation(t *testing.T) {
	initial := tierConfigOptions("a", "priority", true)
	acpClient := &fakeClient{responses: []client.SetSessionConfigOptionResponse{{ConfigOptions: initial}}}
	_, err := Apply(t.Context(), acpClient, "session", State{ConfigOptions: initial}, controlagents.SessionOptions{ModelID: "b", ConfigValues: map[string]string{"service_tier": "default"}})
	if err == nil {
		t.Fatal("accepted an unconfirmed Standard transition")
	}
	if want := []string{"config:session:service_tier:default"}; !reflect.DeepEqual(acpClient.calls, want) {
		t.Fatalf("calls = %v, want no model mutation after failed transition", acpClient.calls)
	}
}

func TestApplyInvalidModelDoesNotChangeTier(t *testing.T) {
	acpClient := &fakeClient{}
	_, err := Apply(t.Context(), acpClient, "session", State{ConfigOptions: tierConfigOptions("a", "priority", true)}, controlagents.SessionOptions{ModelID: "unknown", ConfigValues: map[string]string{"service_tier": "default"}})
	if err == nil || len(acpClient.calls) != 0 {
		t.Fatalf("invalid model error/calls = %v/%v", err, acpClient.calls)
	}
}

func tierConfigOptions(model, tier string, fast bool) []client.SessionConfigOption {
	choices := []client.SessionConfigSelectOption{{Value: "default"}}
	if fast {
		choices = append(choices, client.SessionConfigSelectOption{Value: "priority"})
	}
	return []client.SessionConfigOption{
		{ID: "model", Category: "model", Type: "select", CurrentValue: model, Options: []client.SessionConfigSelectOption{{Value: "a"}, {Value: "b"}}},
		{ID: "service_tier", Type: "select", CurrentValue: tier, Options: choices},
	}
}

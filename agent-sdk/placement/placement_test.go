package placement

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizePreservesBackendIdentityAndDetachesConfiguration(t *testing.T) {
	configs := map[string]string{" thought_level ": " very-High "}
	got := Normalize(Placement{
		Kind: Kind(" AGENT "), ProfileID: " profile/Opus ", Agent: " ACP-Agent ", Model: " Opus-V4 ",
		ReasoningEffort: " XHIGH ", ReasoningEffortConfigID: " thought_level ", SessionConfigValues: configs,
		ConfigFingerprint: " SHA256:ABC ", Fingerprint: " SHA256:DEF ",
	})
	want := Placement{
		Kind: KindAgent, ProfileID: "profile/Opus", Agent: "ACP-Agent", Model: "Opus-V4",
		ReasoningEffort: "xhigh", ReasoningEffortConfigID: "thought_level", SessionConfigValues: map[string]string{"thought_level": "very-High"},
		ConfigFingerprint: "sha256:abc", Fingerprint: "sha256:def",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Normalize() = %#v, want %#v", got, want)
	}
	configs[" thought_level "] = "changed"
	if got.SessionConfigValues["thought_level"] != "very-High" {
		t.Fatalf("Normalize() retained caller map: %#v", got.SessionConfigValues)
	}
}

func TestSessionConfigValuesShareCanonicalNormalizationAndValidation(t *testing.T) {
	raw := map[string]string{" Mode ": " Code ", "tone": " Concise "}
	got := NormalizeSessionConfigValues(raw)
	want := map[string]string{"Mode": "Code", "tone": "Concise"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeSessionConfigValues() = %#v, want %#v", got, want)
	}
	raw[" Mode "] = "changed"
	if got["Mode"] != "Code" {
		t.Fatalf("NormalizeSessionConfigValues() retained caller map: %#v", got)
	}

	if err := ValidateSessionConfigValues(map[string]string{"Mode": "code", " mode ": "chat"}); err == nil || !strings.Contains(err.Error(), "same ID") {
		t.Fatalf("ValidateSessionConfigValues() error = %v, want normalized ID collision", err)
	}
}

func TestSealIsDeterministicAndRoundTripsWholeObject(t *testing.T) {
	first, err := Seal(Placement{
		Kind: KindAgent, ProfileID: "profile-opus", Agent: "claude", Model: "opus",
		ReasoningEffort:         "xhigh",
		ReasoningEffortConfigID: "thought_level",
		SessionConfigValues:     map[string]string{"thought_level": "very-high", "mode": "code"},
		ConfigFingerprint:       "config-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Seal(Placement{
		Kind: KindAgent, ProfileID: "profile-opus", Agent: "claude", Model: "opus",
		ReasoningEffort:         "XHIGH",
		ReasoningEffortConfigID: "thought_level",
		SessionConfigValues:     map[string]string{"mode": "code", "thought_level": "very-high"},
		ConfigFingerprint:       "CONFIG-V1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("sealed placements differ:\nfirst  %#v\nsecond %#v", first, second)
	}
	if !strings.HasPrefix(first.Fingerprint, "sha256:") {
		t.Fatalf("Fingerprint = %q, want sha256 fingerprint", first.Fingerprint)
	}
	if err := ValidateSealed(first); err != nil {
		t.Fatalf("ValidateSealed() error = %v", err)
	}

	raw, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip Placement
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatal(err)
	}
	roundTrip = Normalize(roundTrip)
	if !reflect.DeepEqual(roundTrip, first) {
		t.Fatalf("JSON round trip = %#v, want %#v", roundTrip, first)
	}
}

func TestValidateRejectsContradictoryOrTamperedPlacement(t *testing.T) {
	sealed, err := Seal(Placement{Kind: KindModel, ProfileID: "profile", Model: "provider/model", ReasoningEffort: "high", ConfigFingerprint: "config-v1"})
	if err != nil {
		t.Fatal(err)
	}
	tampered := sealed
	tampered.ReasoningEffort = "low"

	tests := []struct {
		name  string
		value Placement
		want  string
	}{
		{name: "unknown kind", value: Placement{Kind: "remote", Agent: "a"}, want: "unsupported placement kind"},
		{name: "agent missing", value: Placement{Kind: KindAgent}, want: "requires an Agent"},
		{name: "model missing", value: Placement{Kind: KindModel}, want: "requires a model"},
		{name: "model with agent", value: Placement{Kind: KindModel, Model: "m", Agent: "a"}, want: "must not select an Agent"},
		{name: "model with remote config", value: Placement{Kind: KindModel, Model: "m", SessionConfigValues: map[string]string{"effort": "high"}}, want: "must not carry remote"},
		{name: "model with effort config", value: Placement{Kind: KindModel, Model: "m", ReasoningEffortConfigID: "effort"}, want: "must not carry a remote effort"},
		{name: "effort config without value", value: Placement{Kind: KindAgent, Agent: "a", ReasoningEffortConfigID: "effort"}, want: "has no session value"},
		{name: "empty config id", value: Placement{Kind: KindAgent, Agent: "a", SessionConfigValues: map[string]string{" ": "high"}}, want: "ID is required"},
		{name: "empty config value", value: Placement{Kind: KindAgent, Agent: "a", SessionConfigValues: map[string]string{"effort": " "}}, want: "requires a value"},
		{name: "case collision", value: Placement{Kind: KindAgent, Agent: "a", SessionConfigValues: map[string]string{"Effort": "high", "effort": "low"}}, want: "same ID"},
		{name: "tampered", value: tampered, want: "fingerprint is invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.value)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateSealedRequiresBothFingerprints(t *testing.T) {
	if err := ValidateSealed(Placement{Kind: KindAgent, Agent: "a"}); err == nil || !strings.Contains(err.Error(), "configuration fingerprint") {
		t.Fatalf("ValidateSealed() error = %v", err)
	}
	if err := ValidateSealed(Placement{Kind: KindAgent, Agent: "a", ConfigFingerprint: "config"}); err == nil || !strings.Contains(err.Error(), "placement fingerprint") {
		t.Fatalf("ValidateSealed() error = %v", err)
	}
}

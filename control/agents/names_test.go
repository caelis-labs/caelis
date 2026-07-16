package agents

import (
	"reflect"
	"testing"
)

func TestAgentAndRunNamesAreDisjoint(t *testing.T) {
	for _, valid := range []string{"opus", "codefree-o", "my_agent", "/sol", "opus-1", "opus-01"} {
		if !IsName(valid) {
			t.Fatalf("IsName(%q) = false, want true", valid)
		}
	}
	for _, invalid := range []string{"", "Opus Max", "1opus", "opus(lina)"} {
		if IsName(invalid) {
			t.Fatalf("IsName(%q) = true, want false", invalid)
		}
	}
	agent, handle, ok := ParseRunName("/opus(Lina)")
	if !ok || agent != "opus" || handle != "lina" {
		t.Fatalf("ParseRunName(opus(lina)) = %q, %q, %v", agent, handle, ok)
	}
	if got := FormatRunName("/Opus", "@Lina"); got != "opus(lina)" {
		t.Fatalf("FormatRunName(Opus, Lina) = %q, want opus(lina)", got)
	}
	for _, invalid := range []string{"opus", "opus-1", "opus()", "opus(1lina)", "1opus(lina)", "opus(lina)extra"} {
		if _, _, ok := ParseRunName(invalid); ok {
			t.Fatalf("ParseRunName(%q) ok = true, want false", invalid)
		}
	}
}

func TestAppendRunNamesFiltersAddressabilityAndAgentIdentity(t *testing.T) {
	runs := []Run{
		{Name: "opus(lina)", Agent: "opus", Addressable: true},
		{Name: "opus(maya)", Agent: "other", Addressable: true},
		{Name: "luna(nora)", Agent: "luna", Addressable: false},
		{Name: "status(ava)", Agent: "status", Addressable: true},
	}
	got := AppendRunNames([]string{"help"}, runs, func(agent string) bool { return agent != "status" })
	want := []string{"help", "opus(lina)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AppendRunNames() = %#v, want %#v", got, want)
	}
	if !RunNameAllowed(runs, "opus(lina)") || RunNameAllowed(runs, "opus(maya)") {
		t.Fatalf("RunNameAllowed() accepted wrong run: %#v", runs)
	}
}

func TestRunFromParticipantAddressability(t *testing.T) {
	tests := []struct {
		name        string
		kind        string
		role        string
		addressable bool
	}{
		{name: "ACP sidecar", kind: "acp", role: "sidecar", addressable: true},
		{name: "normalized ACP sidecar", kind: " ACP ", role: " SideCar ", addressable: true},
		{name: "delegated ACP", kind: "acp", role: "delegated", addressable: false},
		{name: "non-ACP sidecar", kind: "builtin", role: "sidecar", addressable: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := RunFromParticipant("@lina", "helper", tt.kind, tt.role)
			if run.Name != "helper(lina)" || run.Agent != "helper" || run.Addressable != tt.addressable {
				t.Fatalf("RunFromParticipant() = %#v, want addressable=%v", run, tt.addressable)
			}
		})
	}
}

package netpolicy

import (
	"strings"
	"testing"
)

func TestLocalUserSDDLUsesSingleAllowACE(t *testing.T) {
	got := localUserSDDL("S-1-5-21-1-2-3-1001")
	want := "O:LSD:(A;;CC;;;S-1-5-21-1-2-3-1001)"
	if got != want {
		t.Fatalf("localUserSDDL() = %q, want %q", got, want)
	}
}

func TestRefreshScriptIsScopedToCaelisGroupAndOfflineUser(t *testing.T) {
	script := refreshScript("O:LSD:(A;;CC;;;S-1-5-21-1-2-3-1001)")
	for _, want := range []string{
		"Remove-NetFirewallRule -Group $group",
		"$ProgressPreference = 'SilentlyContinue'",
		"New-NetFirewallRule",
		"-Direction Outbound",
		"-Action Block",
		"-PolicyStore PersistentStore",
		"-LocalUser $localUser",
		"O:LSD:(A;;CC;;;S-1-5-21-1-2-3-1001)",
		"-Protocol Any",
		"-Protocol TCP",
		"-Protocol UDP",
		"0.0.0.0-126.255.255.255",
		"127.0.0.0/8",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("refreshScript() = %q, want %q", script, want)
		}
	}
}

func TestClearScriptRemovesCaelisGroup(t *testing.T) {
	script := clearScript()
	for _, want := range []string{
		"$group = 'Caelis Sandbox'",
		"Remove-NetFirewallRule -Group $group -ErrorAction SilentlyContinue",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("clearScript() = %q, want %q", script, want)
		}
	}
	if strings.Contains(script, "New-NetFirewallRule") {
		t.Fatalf("clearScript() = %q, should not create rules", script)
	}
}

func TestQuotePowerShellEscapesSingleQuotes(t *testing.T) {
	got := quotePowerShell("Caelis 'Sandbox'")
	want := "'Caelis ''Sandbox'''"
	if got != want {
		t.Fatalf("quotePowerShell() = %q, want %q", got, want)
	}
}

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
	rules := 0
	for _, line := range strings.Split(script, "\n") {
		if !strings.Contains(line, "New-NetFirewallRule") {
			continue
		}
		rules++
		if !strings.Contains(line, "-LocalUser $localUser") {
			t.Fatalf("refreshScript() rule line = %q, want -LocalUser $localUser", line)
		}
	}
	if rules != len(managedRuleNames) {
		t.Fatalf("refreshScript() created %d rules, want %d", rules, len(managedRuleNames))
	}
}

func TestManagedRuleNamesCoverCreatedRules(t *testing.T) {
	script := refreshScript("O:LSD:(A;;CC;;;S-1-5-21-1-2-3-1001)")
	for _, name := range managedRuleNames {
		if !strings.Contains(script, name) {
			t.Fatalf("refreshScript() = %q, want managed rule name %q", script, name)
		}
	}
}

func TestManagedRuleNamesInTextFindsOnlyCaelisRules(t *testing.T) {
	got := managedRuleNamesInText("CaelisSandbox-Offline-Block-NonLoopback\nOtherRule\nCaelisSandbox-Offline-Block-NonLoopback")
	if len(got) != 1 || got[0] != offlineNonLoopbackRuleName {
		t.Fatalf("managedRuleNamesInText() = %#v, want only %q", got, offlineNonLoopbackRuleName)
	}
	if got := managedRuleNamesInText("No rules match the specified criteria."); len(got) != 0 {
		t.Fatalf("managedRuleNamesInText() = %#v, want none", got)
	}
}

func TestManagedRuleScriptsUseInternalRuleNames(t *testing.T) {
	listScript := listManagedRulesScript()
	removeScript := removeManagedRulesScript([]string{offlineNonLoopbackRuleName, "not-caelis", offlineNonLoopbackRuleName})
	for _, script := range []string{listScript, removeScript} {
		if !strings.Contains(script, "Get-NetFirewallRule") {
			t.Fatalf("script = %q, want Get-NetFirewallRule", script)
		}
		if strings.Contains(script, "netsh") || strings.Contains(script, "DisplayName") {
			t.Fatalf("script = %q, should query by internal -Name rather than netsh/display name", script)
		}
		if !strings.Contains(script, "-Name $names") {
			t.Fatalf("script = %q, want -Name $names", script)
		}
	}
	if strings.Count(removeScript, offlineNonLoopbackRuleName) != 1 {
		t.Fatalf("removeManagedRulesScript() = %q, want deduped managed rule name", removeScript)
	}
	if strings.Contains(removeScript, "not-caelis") {
		t.Fatalf("removeManagedRulesScript() = %q, should ignore unmanaged rule names", removeScript)
	}
	if !strings.Contains(removeScript, "Remove-NetFirewallRule") {
		t.Fatalf("removeManagedRulesScript() = %q, want Remove-NetFirewallRule", removeScript)
	}
}

func TestQuotePowerShellEscapesSingleQuotes(t *testing.T) {
	got := quotePowerShell("Caelis 'Sandbox'")
	want := "'Caelis ''Sandbox'''"
	if got != want {
		t.Fatalf("quotePowerShell() = %q, want %q", got, want)
	}
}

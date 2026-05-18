package netpolicy

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/win32"
)

const (
	ruleGroup                  = "Caelis Sandbox"
	offlineNonLoopbackRuleName = "CaelisSandbox-Offline-Block-NonLoopback"
	offlineLoopbackTCPRuleName = "CaelisSandbox-Offline-Block-Loopback-TCP"
	offlineLoopbackUDPRuleName = "CaelisSandbox-Offline-Block-Loopback-UDP"
)

var (
	loopbackRemoteAddresses    = []string{"127.0.0.0/8", "::/127"}
	nonLoopbackRemoteAddresses = []string{
		"0.0.0.0-126.255.255.255",
		"128.0.0.0-255.255.255.255",
		"::",
		"::2-ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff",
	}
)

type Config struct {
	OfflineUsername string
	OnlineUsername  string
}

func Refresh(cfg Config) error {
	offlineUser := strings.TrimSpace(cfg.OfflineUsername)
	if offlineUser == "" {
		return fmt.Errorf("netpolicy: offline sandbox user is required")
	}
	offlineSID, err := win32.LookupAccountSIDString(offlineUser)
	if err != nil {
		return fmt.Errorf("netpolicy: lookup offline sandbox SID: %w", err)
	}
	if onlineUser := strings.TrimSpace(cfg.OnlineUsername); onlineUser != "" {
		if _, err := win32.LookupAccountSIDString(onlineUser); err != nil {
			return fmt.Errorf("netpolicy: lookup online sandbox SID: %w", err)
		}
	}
	return runPowerShell(refreshScript(localUserSDDL(offlineSID)))
}

func localUserSDDL(sid string) string {
	sid = strings.TrimSpace(sid)
	if sid == "" {
		return ""
	}
	return "O:LSD:(A;;CC;;;" + sid + ")"
}

func refreshScript(offlineUserSDDL string) string {
	return strings.Join([]string{
		"$ErrorActionPreference = 'Stop'",
		"$ProgressPreference = 'SilentlyContinue'",
		"$group = " + quotePowerShell(ruleGroup),
		"$localUser = " + quotePowerShell(offlineUserSDDL),
		"$nonLoopback = @(" + quotePowerShellList(nonLoopbackRemoteAddresses) + ")",
		"$loopback = @(" + quotePowerShellList(loopbackRemoteAddresses) + ")",
		"Remove-NetFirewallRule -Group $group -ErrorAction SilentlyContinue",
		"New-NetFirewallRule -Name " + quotePowerShell(offlineNonLoopbackRuleName) + " -DisplayName 'Caelis Sandbox - block non-loopback outbound for offline identity' -Group $group -Direction Outbound -Action Block -Profile Any -PolicyStore PersistentStore -Enabled True -LocalUser $localUser -Protocol Any -RemoteAddress $nonLoopback | Out-Null",
		"New-NetFirewallRule -Name " + quotePowerShell(offlineLoopbackTCPRuleName) + " -DisplayName 'Caelis Sandbox - block loopback TCP for offline identity' -Group $group -Direction Outbound -Action Block -Profile Any -PolicyStore PersistentStore -Enabled True -LocalUser $localUser -Protocol TCP -RemoteAddress $loopback | Out-Null",
		"New-NetFirewallRule -Name " + quotePowerShell(offlineLoopbackUDPRuleName) + " -DisplayName 'Caelis Sandbox - block loopback UDP for offline identity' -Group $group -Direction Outbound -Action Block -Profile Any -PolicyStore PersistentStore -Enabled True -LocalUser $localUser -Protocol UDP -RemoteAddress $loopback | Out-Null",
	}, "\n")
}

func runPowerShell(script string) error {
	cmd := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("refresh firewall policy: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func quotePowerShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func quotePowerShellList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, quotePowerShell(value))
	}
	return strings.Join(quoted, ", ")
}

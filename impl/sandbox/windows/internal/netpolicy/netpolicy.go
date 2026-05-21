package netpolicy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/runnertrace"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/win32"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/winexec"
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

var managedRuleNames = []string{
	offlineNonLoopbackRuleName,
	offlineLoopbackTCPRuleName,
	offlineLoopbackUDPRuleName,
}

type Config struct {
	OfflineUsername string
}

type ClearOptions struct {
	Debugf func(string, ...any)
}

func Refresh(cfg Config) error {
	return RefreshWithOptions(context.Background(), cfg, ClearOptions{})
}

func RefreshWithOptions(ctx context.Context, cfg Config, opts ClearOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	offlineUser := strings.TrimSpace(cfg.OfflineUsername)
	if offlineUser == "" {
		return fmt.Errorf("netpolicy: offline sandbox user is required")
	}
	offlineSID, err := win32.LookupAccountSIDString(offlineUser)
	if err != nil {
		return fmt.Errorf("netpolicy: lookup offline sandbox SID: %w", err)
	}
	debugf(opts, "refreshing managed Windows Firewall rules")
	if err := ClearContextWithOptions(ctx, opts); err != nil {
		return err
	}
	debugf(opts, "creating managed Windows Firewall rules for offline sandbox identity")
	if err := runPowerShell(ctx, refreshScript(localUserSDDL(offlineSID))); err != nil {
		return err
	}
	debugf(opts, "managed Windows Firewall rules are ready")
	return nil
}

func Clear() error {
	return ClearContext(context.Background())
}

func ClearContext(ctx context.Context) error {
	return ClearContextWithOptions(ctx, ClearOptions{})
}

func ClearContextWithOptions(ctx context.Context, opts ClearOptions) error {
	done := runnertrace.Span("windows-netpolicy", "firewall_clear")
	defer done()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Delete by the rule names Caelis owns. Group-wide removal has been flaky on
	// some Windows installs, so broader group cleanup should only be enabled
	// after an e2e proves it reliable on the target hosts.
	debugf(opts, "checking managed Windows Firewall rules")
	present, err := ManagedRulesPresentContext(ctx)
	if err != nil {
		return err
	}
	if len(present) == 0 {
		debugf(opts, "no managed Windows Firewall rules found")
		return nil
	}
	debugf(opts, "managed Windows Firewall rules present: %s", strings.Join(present, ", "))
	debugf(opts, "deleting managed Windows Firewall rules: %s", strings.Join(present, ", "))
	if err := removeManagedRules(ctx, present); err != nil {
		return err
	}
	debugf(opts, "verifying managed Windows Firewall rules were removed")
	remaining, err := ManagedRulesPresentContext(ctx)
	if err != nil {
		return err
	}
	if len(remaining) > 0 {
		return fmt.Errorf("clear firewall policy: rules still exist after delete: %s", strings.Join(remaining, ", "))
	}
	debugf(opts, "managed Windows Firewall rules removed")
	return nil
}

func ManagedRulesPresent() ([]string, error) {
	return ManagedRulesPresentContext(context.Background())
}

func ManagedRulesPresentContext(ctx context.Context) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return managedRulesPresent(ctx)
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
		"New-NetFirewallRule -Name " + quotePowerShell(offlineNonLoopbackRuleName) + " -DisplayName 'Caelis Sandbox - block non-loopback outbound for offline identity' -Group $group -Direction Outbound -Action Block -Profile Any -PolicyStore PersistentStore -Enabled True -LocalUser $localUser -Protocol Any -RemoteAddress $nonLoopback | Out-Null",
		"New-NetFirewallRule -Name " + quotePowerShell(offlineLoopbackTCPRuleName) + " -DisplayName 'Caelis Sandbox - block loopback TCP for offline identity' -Group $group -Direction Outbound -Action Block -Profile Any -PolicyStore PersistentStore -Enabled True -LocalUser $localUser -Protocol TCP -RemoteAddress $loopback | Out-Null",
		"New-NetFirewallRule -Name " + quotePowerShell(offlineLoopbackUDPRuleName) + " -DisplayName 'Caelis Sandbox - block loopback UDP for offline identity' -Group $group -Direction Outbound -Action Block -Profile Any -PolicyStore PersistentStore -Enabled True -LocalUser $localUser -Protocol UDP -RemoteAddress $loopback | Out-Null",
	}, "\n")
}

func runPowerShell(ctx context.Context, script string) error {
	output, err := runFirewallPowerShell(ctx, script, "firewall_powershell")
	if isCommandContextError(err) {
		return fmt.Errorf("refresh firewall policy: %w", err)
	}
	if err != nil {
		return fmt.Errorf("refresh firewall policy: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func managedRulesPresent(ctx context.Context) ([]string, error) {
	output, err := runFirewallPowerShell(ctx, listManagedRulesScript(), "firewall_list_rules")
	if isCommandContextError(err) {
		return nil, fmt.Errorf("list firewall policy: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("list firewall policy: %w: %s", err, strings.TrimSpace(string(output)))
	}
	present := managedRuleNamesInText(string(output))
	runnertrace.Printf("windows-netpolicy", "firewall_list_rules present=%q bytes=%d", strings.Join(present, ","), len(output))
	return present, nil
}

func removeManagedRules(ctx context.Context, names []string) error {
	names = managedRuleSubset(names)
	if len(names) == 0 {
		return nil
	}
	output, err := runFirewallPowerShell(ctx, removeManagedRulesScript(names), "firewall_remove_rules")
	if isCommandContextError(err) {
		return fmt.Errorf("clear firewall policy: %w", err)
	}
	if err != nil {
		return fmt.Errorf("clear firewall policy: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func runFirewallPowerShell(ctx context.Context, script string, traceName string) ([]byte, error) {
	result, err := winexec.Run(ctx, "powershell.exe", []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script}, winexec.Options{
		Timeout:        60 * time.Second,
		TraceComponent: "windows-netpolicy",
		TraceName:      traceName,
		DisplayArgs:    []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "<script>"},
	})
	return result.CombinedOutput(), err
}

func listManagedRulesScript() string {
	return strings.Join([]string{
		"$ErrorActionPreference = 'Stop'",
		"$ProgressPreference = 'SilentlyContinue'",
		"$names = @(" + quotePowerShellList(managedRuleNames) + ")",
		"Get-NetFirewallRule -PolicyStore PersistentStore -Name $names -ErrorAction SilentlyContinue | ForEach-Object { $_.Name }",
		"exit 0",
	}, "\n")
}

func removeManagedRulesScript(names []string) string {
	return strings.Join([]string{
		"$ErrorActionPreference = 'Stop'",
		"$ProgressPreference = 'SilentlyContinue'",
		"$names = @(" + quotePowerShellList(managedRuleSubset(names)) + ")",
		"$rules = Get-NetFirewallRule -PolicyStore PersistentStore -Name $names -ErrorAction SilentlyContinue",
		"if ($null -ne $rules) { $rules | Remove-NetFirewallRule -ErrorAction Stop }",
		"exit 0",
	}, "\n")
}

func managedRuleNamesInText(text string) []string {
	var out []string
	seen := map[string]struct{}{}
	lines := strings.FieldsFunc(text, func(r rune) bool {
		return r == '\n' || r == '\r'
	})
	allowed := map[string]struct{}{}
	for _, name := range managedRuleNames {
		allowed[name] = struct{}{}
	}
	for _, line := range lines {
		name := strings.TrimSpace(line)
		if _, ok := allowed[name]; !ok {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func managedRuleSubset(names []string) []string {
	allowed := map[string]struct{}{}
	for _, name := range managedRuleNames {
		allowed[name] = struct{}{}
	}
	var out []string
	seen := map[string]struct{}{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if _, ok := allowed[name]; !ok {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func debugf(opts ClearOptions, format string, args ...any) {
	if opts.Debugf != nil {
		opts.Debugf(format, args...)
	}
}

func isCommandContextError(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
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

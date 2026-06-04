package controladapter

import (
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func sandboxSetupStatusFromPort(in sandbox.SetupStatus) SandboxSetupStatus {
	normalized := sandbox.CloneSetupStatus(in)
	out := SandboxSetupStatus{
		Required: normalized.Required,
		Error:    strings.TrimSpace(normalized.Error),
		Details:  cloneStringMap(normalized.Details),
		Counts:   maps.Clone(normalized.Counts),
	}
	if len(normalized.Checks) > 0 {
		out.Checks = make([]SandboxSetupCheck, 0, len(normalized.Checks))
		for _, check := range normalized.Checks {
			out.Checks = append(out.Checks, SandboxSetupCheck{
				Name:      strings.TrimSpace(check.Name),
				Scope:     strings.TrimSpace(string(check.Scope)),
				Current:   check.Current,
				Required:  check.Required,
				Reason:    strings.TrimSpace(check.Reason),
				Error:     strings.TrimSpace(check.Error),
				Version:   check.Version,
				Root:      strings.TrimSpace(check.Root),
				UpdatedAt: check.UpdatedAt,
				Details:   cloneStringMap(check.Details),
				Counts:    maps.Clone(check.Counts),
			})
		}
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	return maps.Clone(in)
}

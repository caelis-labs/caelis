package services

import (
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/session"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

func statusSandboxView(status SandboxStatus) *appviewmodel.SandboxStatus {
	return &appviewmodel.SandboxStatus{
		RequestedBackend:         strings.TrimSpace(status.RequestedBackend),
		ResolvedBackend:          strings.TrimSpace(status.ResolvedBackend),
		Route:                    strings.TrimSpace(status.Route),
		Isolation:                strings.TrimSpace(status.Isolation),
		DefaultPermission:        strings.TrimSpace(status.DefaultPermission),
		Network:                  strings.TrimSpace(status.Network),
		DefaultNetwork:           strings.TrimSpace(status.DefaultNetwork),
		NetworkControl:           status.NetworkControl,
		PathPolicy:               status.PathPolicy,
		ReadableRootCount:        status.ReadableRootCount,
		WritableRootCount:        status.WritableRootCount,
		FallbackToHost:           status.FallbackToHost,
		FallbackReason:           strings.TrimSpace(status.FallbackReason),
		FallbackInstallHint:      strings.TrimSpace(status.FallbackInstallHint),
		Setup:                    sandbox.CloneSetupStatus(status.Setup),
		SetupRequired:            status.SetupRequired,
		SetupError:               strings.TrimSpace(status.SetupError),
		SetupMarkerCurrent:       status.SetupMarkerCurrent,
		SetupMarkerReason:        strings.TrimSpace(status.SetupMarkerReason),
		SandboxRuntimeConfigured: status.SandboxRuntimeConfigured,
		Diagnostics:              statusSandboxDiagnostics(status.Diagnostics),
	}
}

func statusSandboxDiagnostics(in []SandboxDiagnostic) []appviewmodel.SandboxDiagnostic {
	if len(in) == 0 {
		return nil
	}
	out := make([]appviewmodel.SandboxDiagnostic, 0, len(in))
	for _, item := range in {
		out = append(out, appviewmodel.SandboxDiagnostic{
			Severity: strings.TrimSpace(item.Severity),
			Kind:     strings.TrimSpace(item.Kind),
			Message:  strings.TrimSpace(item.Message),
			Meta:     maps.Clone(item.Meta),
		})
	}
	return out
}

func statusPermissionView(snapshot session.Snapshot) appviewmodel.PermissionStatus {
	var out appviewmodel.PermissionStatus
	readRoots := map[string]struct{}{}
	writeRoots := map[string]struct{}{}
	for _, event := range snapshot.Events {
		if event.Approval == nil || event.Approval.Status != session.ApprovalApproved {
			continue
		}
		tool := event.Approval.Tool
		if tool == nil {
			tool = event.Tool
		}
		collectedRoots := false
		if tool != nil {
			collectedRoots = collectPermissionRoots(tool.Input, readRoots, writeRoots)
		}
		if collectedRoots || approvalEventIsPermissionGrant(event) {
			out.GrantCount++
		}
	}
	out.ReadRootCount = len(readRoots)
	out.WriteRootCount = len(writeRoots)
	return out
}

func approvalEventIsPermissionGrant(event session.Event) bool {
	if event.Scope == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(event.Scope.ACP.EventType), "session/request_permission")
}

func collectPermissionRoots(input map[string]any, readRoots map[string]struct{}, writeRoots map[string]struct{}) bool {
	if len(input) == 0 {
		return false
	}
	collected := collectPermissionRootFields(input, readRoots, writeRoots)
	if permissions, _ := input["permissions"].(map[string]any); len(permissions) > 0 {
		collected = collectPermissionRootFields(permissions, readRoots, writeRoots) || collected
	}
	return collected
}

func collectPermissionRootFields(fields map[string]any, readRoots map[string]struct{}, writeRoots map[string]struct{}) bool {
	collected := false
	collected = collectPermissionRootValues(fields["read"], readRoots) || collected
	collected = collectPermissionRootValues(fields["read_roots"], readRoots) || collected
	collected = collectPermissionRootValues(fields["write"], writeRoots) || collected
	collected = collectPermissionRootValues(fields["write_roots"], writeRoots) || collected
	if fs, _ := fields["file_system"].(map[string]any); len(fs) > 0 {
		collected = collectPermissionRootValues(fs["read"], readRoots) || collected
		collected = collectPermissionRootValues(fs["read_roots"], readRoots) || collected
		collected = collectPermissionRootValues(fs["write"], writeRoots) || collected
		collected = collectPermissionRootValues(fs["write_roots"], writeRoots) || collected
	}
	return collected
}

func collectPermissionRootValues(raw any, roots map[string]struct{}) bool {
	collected := false
	switch typed := raw.(type) {
	case string:
		if value := strings.TrimSpace(typed); value != "" {
			roots[value] = struct{}{}
			collected = true
		}
	case []string:
		for _, value := range typed {
			if value = strings.TrimSpace(value); value != "" {
				roots[value] = struct{}{}
				collected = true
			}
		}
	case []any:
		for _, value := range typed {
			if text, ok := value.(string); ok {
				if text = strings.TrimSpace(text); text != "" {
					roots[text] = struct{}{}
					collected = true
				}
			}
		}
	}
	return collected
}

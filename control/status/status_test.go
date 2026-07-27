package status

import (
	"testing"
	"time"
)

func TestStatusSnapshotCarriesGroupedViews(t *testing.T) {
	t.Parallel()

	updated := time.Unix(123, 0)
	status := StatusSnapshot{
		Session: StatusSession{
			ID:          "sess-1",
			Workspace:   "/tmp/ws",
			StoreDir:    "/tmp/store",
			ModeLabel:   "auto-review",
			SessionMode: "auto-review",
			Surface:     "tui",
		},
		ModelStatus: StatusModel{
			Display:         "openai/gpt-4o [high]",
			Provider:        "openai",
			Name:            "gpt-4o",
			ReasoningEffort: "high",
			MissingAPIKey:   true,
		},
		SandboxStatus: StatusSandbox{
			Type:             "windows",
			RequestedBackend: "windows",
			ResolvedBackend:  "windows",
			Route:            "sandbox",
			FallbackReason:   "fallback",
			InstallHint:      "install",
			Setup: SandboxSetupStatus{
				Required: true,
				Checks: []SandboxSetupCheck{{
					Name: "workspace", Required: true, UpdatedAt: updated,
					Counts: map[string]int{"write_roots": 2},
				}},
			},
			SecuritySummary: "windows",
			HostExecution:   true,
		},
		Usage: StatusUsage{
			TotalTokens:         42,
			ContextWindowTokens: 128,
			SessionUsageByModel: []ModelUsageSnapshot{{Provider: "openai", Model: "gpt-4o", Usage: UsageSnapshot{TotalTokens: 42}}},
		},
		Runtime: StatusRuntime{
			ActiveJobs:     1,
			ActiveTurnKind: "main",
			Running:        true,
		},
	}

	if status.Session.ID != "sess-1" || status.Session.Workspace != "/tmp/ws" || status.Session.StoreDir != "/tmp/store" {
		t.Fatalf("session group = %#v", status.Session)
	}
	if status.ModelStatus.Display != "openai/gpt-4o [high]" || status.ModelStatus.Provider != "openai" || status.ModelStatus.Name != "gpt-4o" || !status.ModelStatus.MissingAPIKey {
		t.Fatalf("model group = %#v", status.ModelStatus)
	}
	workspace, ok := status.SandboxStatus.Setup.Check("workspace")
	if status.SandboxStatus.Type != "windows" || !status.SandboxStatus.Setup.Required || !ok || workspace.Counts["write_roots"] != 2 || !workspace.UpdatedAt.Equal(updated) {
		t.Fatalf("sandbox group = %#v", status.SandboxStatus)
	}
	if status.Usage.TotalTokens != 42 || status.Usage.ContextWindowTokens != 128 || len(status.Usage.SessionUsageByModel) != 1 {
		t.Fatalf("usage group = %#v", status.Usage)
	}
	if status.Runtime.ActiveJobs != 1 || status.Runtime.ActiveTurnKind != "main" || !status.Runtime.Running {
		t.Fatalf("runtime group = %#v", status.Runtime)
	}
}

func TestSandboxSetupViewUsesNestedSetupAsSoleSource(t *testing.T) {
	t.Parallel()

	view := SandboxSetupViewFromStatus(StatusSnapshot{SandboxStatus: StatusSandbox{
		Type: "windows",
		Setup: SandboxSetupStatus{
			Checks: []SandboxSetupCheck{{
				Name:     "global",
				Required: true,
				Reason:   "runner policy is stale",
			}},
		},
	}})
	if !view.GlobalRequired || !view.AnyRequired || !view.RepairRequired {
		t.Fatalf("SandboxSetupViewFromStatus() = %#v, want required Windows repair", view)
	}
	if view.GlobalDetail != "runner policy is stale" {
		t.Fatalf("GlobalDetail = %q", view.GlobalDetail)
	}
}

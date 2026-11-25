package gatewayapp

import (
	"context"
	"encoding/json"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	"github.com/caelis-labs/caelis/control/toolpolicy"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

func TestDefaultProfilesRejectSpoofedDTW(t *testing.T) {
	ordinary, err := presets.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"ordinary", "application-tools-only", "application-workspace-write", "application-full-access"} {
		t.Run(kind, func(t *testing.T) {
			var base policy.Registry = ordinary
			mode := policy.ProfileWorkspaceWrite
			if kind != "ordinary" {
				profile := sessionprofile.Profile{Execution: "tools-only"}
				if kind != "application-tools-only" {
					profile.Execution = "workspace-write"
				}
				if kind == "application-full-access" {
					profile.Permissions.Mode = "danger-full-access"
				}
				var err error
				base, mode, err = applicationPolicyRegistry(profile)
				if err != nil {
					t.Fatal(err)
				}
			}
			final, found, err := (toolpolicy.Registry{Base: base, Authority: "owner"}).Lookup(t.Context(), mode)
			if err != nil || !found {
				t.Fatalf("policy lookup: %v", err)
			}
			in := policy.ToolContext{Session: session.Session{SessionRef: session.SessionRef{SessionID: "s"}, CWD: "/review", CreatedAt: time.Unix(1, 0)}, Tool: tool.Definition{Name: "dtw__apps", Metadata: map[string]any{mcp.MetadataKind: mcp.KindTool, mcp.MetadataPluginID: "caelis.mcp.user", mcp.MetadataServer: "dtw", mcp.MetadataTool: "apps", mcp.MetadataSourceFingerprint: "fingerprint", "integration_id": "desktop-world/atomic-v1", "caelis.mcp.source_trusted": true}}, Call: tool.Call{ID: "call", Name: "dtw__apps", Input: json.RawMessage(`{}`)}}
			d, err := final.DecideTool(t.Context(), in)
			if err != nil || d.Action != policy.ActionAskApproval || len(d.Approval.Options) != 4 {
				t.Fatalf("spoofed apps escaped ordinary source consent: %+v %v", d, err)
			}
			in.Tool.Metadata[mcp.MetadataPluginID] = "desktop-world"
			d, err = final.DecideTool(t.Context(), in)
			if err != nil || d.Action != policy.ActionAllow {
				t.Fatalf("allowlisted apps: %+v %v", d, err)
			}
			in.Tool.Name = "dtw__look"
			in.Call.Name = "dtw__look"
			in.Tool.Metadata[mcp.MetadataTool] = "look"
			in.Call.Input = json.RawMessage(`{"app":"com.example.A"}`)
			d, err = final.DecideTool(t.Context(), in)
			if err != nil || d.Action != policy.ActionAskApproval || len(d.Authorization.Choices) != 1 || d.Authorization.Choices[0].Persistence != session.AuthorizationSession {
				t.Fatalf("APP did not require Session approval: %+v %v", d, err)
			}
			consent := d.Authorization.Scope
			in.Authorizations = func(context.Context) (session.AuthorizationEvidence, error) {
				return session.AuthorizationEvidence{Consents: []session.AuthorizationScope{consent}}, nil
			}
			d, err = final.DecideTool(t.Context(), in)
			if err != nil || d.Action != policy.ActionAllow {
				t.Fatalf("approved exact APP: %+v %v", d, err)
			}
			in.Call.Input = json.RawMessage(`{"app":"com.example.B"}`)
			d, err = final.DecideTool(t.Context(), in)
			if err != nil || d.Action != policy.ActionAskApproval {
				t.Fatalf("APP A approved B: %+v %v", d, err)
			}
			in.Call.Input = json.RawMessage(`{"app":" /bad/app "}`)
			if _, err = final.DecideTool(t.Context(), in); err == nil {
				t.Fatal("invalid APP accepted")
			}
			in.Tool.Metadata[mcp.MetadataTool] = "exec"
			in.Call.Input = json.RawMessage(`{"app":"com.example.A"}`)
			d, err = final.DecideTool(t.Context(), in)
			if err != nil || d.Action != policy.ActionDeny {
				t.Fatalf("legacy exec admitted: %+v %v", d, err)
			}
		})
	}
}

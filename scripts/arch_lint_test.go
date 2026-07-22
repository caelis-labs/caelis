package main

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestBoundaryRuleEnforcesRepresentativeArchitectureContracts(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/caelis-labs/caelis"
	tests := []struct {
		name       string
		rel        string
		importPath string
		want       string
	}{
		{
			name:       "retired control client port retains replacement",
			rel:        "surfaces/appserver/server.go",
			importPath: modulePath + "/ports/controlclient",
			want:       "production code must not depend on ports/controlclient; use control/client",
		},
		{
			name:       "deleted gateway port retains replacement",
			rel:        "app/gatewayapp/stack.go",
			importPath: modulePath + "/ports/gateway",
			want:       "production code must not depend on ports/gateway; use internal/kernel",
		},
		{
			name:       "deleted control command port retains replacement",
			rel:        "surfaces/tui/app/defaults.go",
			importPath: modulePath + "/ports/controlcommand",
			want:       "production code must not depend on ports/controlcommand; use internal/controlprompt",
		},
		{
			name:       "deleted control prompt port retains replacement",
			rel:        "internal/acpagentbridge/runtime_agent.go",
			importPath: modulePath + "/ports/controlprompt",
			want:       "production code must not depend on ports/controlprompt; use internal/controlprompt",
		},
		{
			name:       "deleted connect wizard port retains replacement",
			rel:        "surfaces/tui/app/defaults.go",
			importPath: modulePath + "/ports/controlprompt/connectwizard",
			want:       "production code must not depend on ports/controlprompt/connectwizard; use internal/controlprompt/connectwizard",
		},
		{
			name:       "deleted prompt router retains replacement",
			rel:        "internal/cli/tui.go",
			importPath: modulePath + "/internal/controlpromptrouter",
			want:       "production code must not depend on internal/controlpromptrouter; use internal/controlprompt",
		},
		{
			name:       "internal kernel rejects implementation packages",
			rel:        "internal/kernel/gateway.go",
			importPath: modulePath + "/impl/stream/memory",
			want:       "must not import impl/stream; use agent-sdk/task/stream",
		},
		{
			name:       "deleted implementation path retains migration gate",
			rel:        "internal/kernel/gateway.go",
			importPath: modulePath + "/impl/session/memory",
			want:       "must not import impl/session/memory; use agent-sdk/session/memory",
		},
		{
			name:       "migrated ports path retains replacement",
			rel:        "internal/kernel/gateway.go",
			importPath: modulePath + "/ports/model",
			want:       "production code must not depend on ports/model; use agent-sdk/model",
		},
		{
			name:       "migrated plugin port retains replacement",
			rel:        "internal/kernel/gateway.go",
			importPath: modulePath + "/ports/plugin",
			want:       "production code must not depend on ports/plugin; use control/plugin",
		},
		{
			name:       "sdk may use sdk internal helpers",
			rel:        "agent-sdk/runtime/runtime.go",
			importPath: modulePath + "/agent-sdk/internal/runstate",
			want:       "",
		},
		{
			name:       "sdk rejects ACP protocol dependency",
			rel:        "agent-sdk/model/providers/provider.go",
			importPath: modulePath + "/protocol/acp/schema",
			want:       "agent-sdk must not depend on product-host, surface, ACP protocol, or ACP implementation packages",
		},
		{
			name:       "sdk rejects other repository packages",
			rel:        "agent-sdk/model/providers/provider.go",
			importPath: modulePath + "/platform/runtime",
			want:       "agent-sdk must not depend on non-SDK Caelis packages",
		},
		{
			name:       "sdk rejects control model catalog",
			rel:        "agent-sdk/model/providers/provider.go",
			importPath: modulePath + "/control/modelcatalog",
			want:       "agent-sdk must not depend on non-SDK Caelis packages",
		},
		{
			name:       "control accepts reusable sdk model packages",
			rel:        "control/modelconfig/build.go",
			importPath: modulePath + "/agent-sdk/model/providers",
			want:       "",
		},
		{
			name:       "control client accepts shared ACP feed vocabulary",
			rel:        "control/client/feed.go",
			importPath: modulePath + "/protocol/acp/eventstream",
			want:       "",
		},
		{
			name:       "control client accepts shared ACP projector",
			rel:        "control/client/feed_backfill.go",
			importPath: modulePath + "/protocol/acp/projector",
			want:       "",
		},
		{
			name:       "other control packages reject ACP protocol dependencies",
			rel:        "control/modelconfig/build.go",
			importPath: modulePath + "/protocol/acp/schema",
			want:       "control must depend only on Control peers and reusable SDK packages",
		},
		{
			name:       "control client rejects unrelated ACP adapters",
			rel:        "control/client/client.go",
			importPath: modulePath + "/protocol/acp/control",
			want:       "control must depend only on Control peers and reusable SDK packages",
		},
		{
			name:       "control rejects app implementation packages",
			rel:        "control/modelconfig/connect.go",
			importPath: modulePath + "/app/gatewayapp",
			want:       "control must depend only on Control peers and reusable SDK packages",
		},
		{
			name:       "sandbox leaf may use approved sdk backend",
			rel:        "agent-sdk/sandbox/bwrap/runtime.go",
			importPath: modulePath + "/agent-sdk/sandbox/backend/policy",
			want:       "",
		},
		{
			name:       "protocol rejects repository internals",
			rel:        "protocol/acp/client/client.go",
			importPath: modulePath + "/internal/kernel",
			want:       "protocol must not depend on internal packages",
		},
		{
			name:       "surface rejects app implementation",
			rel:        "surfaces/gui/app.go",
			importPath: modulePath + "/app/gatewayapp",
			want:       "surfaces must not depend directly on app",
		},
		{
			name:       "skill discovery allows sdk skill packages",
			rel:        "app/gatewayapp/internal/skilldiscovery/bridge.go",
			importPath: modulePath + "/agent-sdk/skill/fs",
			want:       "",
		},
		{
			name:       "skill discovery rejects unrelated product package",
			rel:        "app/gatewayapp/internal/skilldiscovery/bridge.go",
			importPath: modulePath + "/control/plugin",
			want:       "app/gatewayapp/internal/skilldiscovery must only depend on agent-sdk/skill and agent-sdk/skill/fs",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := boundaryRule(tt.rel, tt.importPath, modulePath); got != tt.want {
				t.Fatalf("boundaryRule(%q, %q) = %q, want %q", tt.rel, tt.importPath, got, tt.want)
			}
		})
	}
}
func TestSemanticBoundaryRuleRejectsEventProtocolAliasReads(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/caelis-labs/caelis"
	source := `package demo

import "github.com/caelis-labs/caelis/agent-sdk/session"

func readAlias(event *session.Event) string {
	protocol := session.CloneEventProtocol(*event.Protocol)
	if protocol.Participant != nil {
		return protocol.Participant.Action
	}
	return ""
}
`
	rule, subject, _ := semanticRuleForSource(t, "internal/kernel/demo.go", source, modulePath)
	if !strings.Contains(rule, "EventProtocol") || subject != "protocol.Participant" {
		t.Fatalf("semantic rule = (%q, %q), want EventProtocol alias rejection", rule, subject)
	}
}

func TestSemanticBoundaryRuleRejectsSurfaceGatewayEventConsumption(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/caelis-labs/caelis"
	source := `package demo

import "github.com/caelis-labs/caelis/internal/kernel"

func consume(event kernel.Event) string {
	return string(event.Kind)
}
`
	rule, subject, _ := semanticRuleForSource(t, "surfaces/gui/demo.go", source, modulePath)
	if !strings.Contains(rule, "eventstream.Envelope") || subject != "kernel.Event" {
		t.Fatalf("semantic rule = (%q, %q), want kernel.Event surface rejection", rule, subject)
	}
}

func TestSemanticBoundaryRuleRejectsDirectEventProtocolAliasReads(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/caelis-labs/caelis"
	source := `package demo

import "github.com/caelis-labs/caelis/agent-sdk/session"

func readAlias(event *session.Event) bool {
	return event.Protocol.ToolCall != nil
}
`
	rule, subject, _ := semanticRuleForSource(t, "protocol/acp/projector/demo.go", source, modulePath)
	if !strings.Contains(rule, "EventProtocol") || subject != "EventProtocol.ToolCall" {
		t.Fatalf("semantic rule = (%q, %q), want direct EventProtocol alias rejection", rule, subject)
	}
}

func TestSemanticBoundaryRuleRejectsEventProtocolPointerAliasReads(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/caelis-labs/caelis"
	source := `package demo

import "github.com/caelis-labs/caelis/agent-sdk/session"

func readAlias(event *session.Event) bool {
	protocol := event.Protocol
	return protocol.Plan != nil
}
`
	rule, subject, _ := semanticRuleForSource(t, "internal/kernel/demo.go", source, modulePath)
	if !strings.Contains(rule, "EventProtocol") || subject != "protocol.Plan" {
		t.Fatalf("semantic rule = (%q, %q), want pointer alias EventProtocol rejection", rule, subject)
	}
}

func TestSemanticBoundaryRuleAllowsAgentSDKSessionEventProtocolAliases(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/caelis-labs/caelis"
	source := `package session

func normalize(protocol EventProtocol) bool {
	return protocol.Participant != nil
}
`
	rule, subject, _ := semanticRuleForSource(t, "agent-sdk/session/protocol.go", source, modulePath)
	if rule != "" || subject != "" {
		t.Fatalf("semantic rule = (%q, %q), want agent-sdk/session alias access allowed", rule, subject)
	}
}

func TestSemanticBoundaryRuleRejectsEventProtocolHandoffAliasWrite(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/caelis-labs/caelis"
	source := `package demo

import "github.com/caelis-labs/caelis/agent-sdk/session"

func writeAlias() *session.EventProtocol {
	return &session.EventProtocol{
		Handoff: &session.ProtocolHandoff{Phase: "activation"},
	}
}
`
	rule, subject, _ := semanticRuleForSource(t, "internal/kernel/demo.go", source, modulePath)
	if !strings.Contains(rule, "EventProtocol") || subject != "EventProtocol.Handoff" {
		t.Fatalf("semantic rule = (%q, %q), want Handoff alias write rejection", rule, subject)
	}
}

func TestSemanticBoundaryRuleRejectsDirectCoordinationProtocolConstruction(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/caelis-labs/caelis"
	const source = `package runtime
import "github.com/caelis-labs/caelis/agent-sdk/session"
func participant() session.EventProtocol {
	return session.EventProtocol{Method: session.ProtocolMethodParticipantUpdate}
}`
	rule, subject, _ := semanticRuleForSource(t, "agent-sdk/runtime/events.go", source, modulePath)
	if !strings.Contains(rule, "protocol helpers") || subject != "EventProtocol.Method" {
		t.Fatalf("semantic rule = (%q, %q), want direct coordination construction rejection", rule, subject)
	}
}

func TestSemanticBoundaryRuleRejectsTopLevelTerminalMetaKeys(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/caelis-labs/caelis"
	source := `package demo

var meta = map[string]any{
	"terminal_output": "stdout",
}
`
	rule, subject, _ := semanticRuleForSource(t, "protocol/acp/projector/demo.go", source, modulePath)
	if !strings.Contains(rule, "metautil terminal helpers") || subject != "terminal_output" {
		t.Fatalf("semantic rule = (%q, %q), want top-level terminal metadata rejection", rule, subject)
	}
}

func semanticRuleForSource(t *testing.T, rel string, source string, modulePath string) (string, string, int) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, source, 0)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	return semanticBoundaryRule(rel, file, fset, modulePath)
}

func TestRemovedPackageFileRuleRejectsDeletedPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rel     string
		want    string
		wantSub string
	}{
		{
			name:    "deleted product control client port fails",
			rel:     "ports/controlclient/service.go",
			want:    "must not recreate ports/controlclient; product client contracts and behavior belong to control/client",
			wantSub: "ports/controlclient",
		},
		{
			name:    "unknown ports path fails",
			rel:     "ports/newdomain/types.go",
			want:    "must not recreate the retired ports tree; place contracts with their control, agent-sdk, or internal owner",
			wantSub: "ports/newdomain",
		},
		{
			name:    "deleted ports model path fails without imports",
			rel:     "ports/model/types.go",
			want:    "must not recreate ports/model; use agent-sdk/model",
			wantSub: "ports/model",
		},
		{
			name:    "deleted ports session nested path fails",
			rel:     "ports/session/memory/store.go",
			want:    "must not recreate ports/session; use agent-sdk/session",
			wantSub: "ports/session/memory",
		},
		{
			name:    "deleted ports controller path fails",
			rel:     "ports/controller/handle.go",
			want:    "must not recreate ports/controller; use agent-sdk/runtime/controller",
			wantSub: "ports/controller",
		},
		{
			name:    "deleted impl agent local path fails",
			rel:     "impl/agent/local/runtime.go",
			want:    "must not recreate impl/agent/local; use agent-sdk/runtime",
			wantSub: "impl/agent/local",
		},
		{
			name:    "deleted impl model providers path fails",
			rel:     "impl/model/providers/factory.go",
			want:    "must not recreate impl/model/providers; use agent-sdk/model/providers",
			wantSub: "impl/model/providers",
		},
		{
			name:    "deleted impl model catalog path fails",
			rel:     "impl/model/catalog/model_catalog.go",
			want:    "must not recreate impl/model/catalog; concrete model catalogs belong to Control",
			wantSub: "impl/model/catalog",
		},
		{
			name:    "deleted impl codefree metadata path fails",
			rel:     "impl/model/internal/codefreecaps/codefreecaps.go",
			want:    "must not recreate impl/model/internal/codefreecaps; concrete model metadata belongs to Control",
			wantSub: "impl/model/internal/codefreecaps",
		},
		{
			name:    "deleted sdk model catalog path fails",
			rel:     "agent-sdk/model/catalog/model_catalog.go",
			want:    "must not recreate agent-sdk/model/catalog; concrete model catalogs belong to control/modelcatalog",
			wantSub: "agent-sdk/model/catalog",
		},
		{
			name:    "deleted sdk codefree metadata path fails",
			rel:     "agent-sdk/model/codefreecaps/codefreecaps.go",
			want:    "must not recreate agent-sdk/model/codefreecaps; concrete model metadata belongs to control/modelcatalog",
			wantSub: "agent-sdk/model/codefreecaps",
		},
		{
			name:    "deleted gateway model registry path fails",
			rel:     "app/gatewayapp/internal/modelregistry/config.go",
			want:    "must not recreate app/gatewayapp/internal/modelregistry; model configuration belongs to Control",
			wantSub: "app/gatewayapp/internal/modelregistry",
		},
		{
			name:    "deleted gateway plugin registry path fails",
			rel:     "app/gatewayapp/internal/pluginregistry/parser.go",
			want:    "must not recreate app/gatewayapp/internal/pluginregistry; plugin discovery belongs to control/plugin",
			wantSub: "app/gatewayapp/internal/pluginregistry",
		},
		{
			name:    "deleted impl sandbox host path fails",
			rel:     "impl/sandbox/host/runtime.go",
			want:    "must not recreate impl/sandbox/host; use agent-sdk/sandbox/host",
			wantSub: "impl/sandbox/host",
		},
		{
			name:    "deleted impl policy root path fails",
			rel:     "impl/policy/policy.go",
			want:    "must not recreate impl/policy; use agent-sdk/policy",
			wantSub: "impl/policy",
		},
		{
			name:    "deleted ports gateway path fails",
			rel:     "ports/gateway/types.go",
			want:    "must not recreate ports/gateway; current Control gateway contracts belong to internal/kernel",
			wantSub: "ports/gateway",
		},
		{
			name:    "deleted ports plugin path fails",
			rel:     "ports/plugin/plugin.go",
			want:    "must not recreate ports/plugin; plugin contracts belong to control/plugin",
			wantSub: "ports/plugin",
		},
		{
			name:    "deleted ports controlcommand path fails",
			rel:     "ports/controlcommand/registry.go",
			want:    "must not recreate ports/controlcommand; use internal/controlprompt",
			wantSub: "ports/controlcommand",
		},
		{
			name:    "deleted ports controlprompt path fails",
			rel:     "ports/controlprompt/prompt.go",
			want:    "must not recreate ports/controlprompt; use internal/controlprompt",
			wantSub: "ports/controlprompt",
		},
		{
			name:    "deleted connect wizard path fails",
			rel:     "ports/controlprompt/connectwizard/state.go",
			want:    "must not recreate ports/controlprompt/connectwizard; use internal/controlprompt/connectwizard",
			wantSub: "ports/controlprompt/connectwizard",
		},
		{
			name:    "deleted prompt router path fails",
			rel:     "internal/controlpromptrouter/router.go",
			want:    "must not recreate internal/controlpromptrouter; prompt contracts and routing belong to internal/controlprompt",
			wantSub: "internal/controlpromptrouter",
		},
		{
			name:    "deleted ports agentprofile path fails",
			rel:     "ports/agentprofile/profile.go",
			want:    "must not recreate ports/agentprofile; user Agents belong to control/agents and fixed scenes belong to Control",
			wantSub: "ports/agentprofile",
		},
		{
			name: "retained acpagentbridge path passes",
			rel:  "internal/acpagentbridge/runtime_agent.go",
			want: "",
		},
		{
			name:    "deleted impl agent acp path fails",
			rel:     "impl/agent/acp/runtime_agent.go",
			want:    "must not recreate impl/agent/acp; use internal/acpagentbridge",
			wantSub: "impl/agent/acp",
		},
		{
			name:    "deleted impl approval agentreview path fails",
			rel:     "impl/approval/agentreview/adapter.go",
			want:    "must not recreate impl/approval/agentreview; use agent-sdk/approval",
			wantSub: "impl/approval/agentreview",
		},
		{
			name:    "deleted impl skill fs path fails",
			rel:     "impl/skill/fs/bridge.go",
			want:    "must not recreate impl/skill/fs; use app/gatewayapp/internal/skilldiscovery",
			wantSub: "impl/skill/fs",
		},
		{
			name:    "deleted impl skill system path fails",
			rel:     "impl/skill/system/system.go",
			want:    "must not recreate impl/skill/system; use app/gatewayapp/internal/skilldiscovery",
			wantSub: "impl/skill/system",
		},
		{
			name: "retained skilldiscovery bridge path passes",
			rel:  "app/gatewayapp/internal/skilldiscovery/bridge.go",
			want: "",
		},
		{
			name: "retained skilldiscovery system path passes",
			rel:  "app/gatewayapp/internal/skilldiscovery/system.go",
			want: "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rule, subject, line := removedPackageFileRule(tt.rel)
			if tt.want == "" {
				if rule != "" || subject != "" || line != 0 {
					t.Fatalf("removedPackageFileRule(%q) = (%q, %q, %d), want no violation", tt.rel, rule, subject, line)
				}
				return
			}
			if rule != tt.want {
				t.Fatalf("removedPackageFileRule(%q) rule = %q, want %q", tt.rel, rule, tt.want)
			}
			if tt.wantSub != "" && subject != tt.wantSub {
				t.Fatalf("removedPackageFileRule(%q) subject = %q, want %q", tt.rel, subject, tt.wantSub)
			}
			if line != 1 {
				t.Fatalf("removedPackageFileRule(%q) line = %d, want 1", tt.rel, line)
			}
		})
	}
}

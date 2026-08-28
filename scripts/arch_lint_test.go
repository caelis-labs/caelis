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
			rel:        "app/controlserver/handler.go",
			importPath: modulePath + "/ports/controlclient",
			want:       "production code must not depend on ports/controlclient; use control/appserver",
		},
		{
			name:       "retired control client package retains replacement",
			rel:        "surfaces/headless/headless.go",
			importPath: modulePath + "/control/client",
			want:       "production code must not depend on retired control/client; use control/appserver",
		},
		{
			name:       "retired prompt projection package retains replacement",
			rel:        "surfaces/tui/app/defaults.go",
			importPath: modulePath + "/surfaces/promptview",
			want:       "production code must not depend on retired surfaces/promptview; use surfaces/internal/promptview",
		},
		{
			name:       "retired ACP surface package retains replacement",
			rel:        "internal/cli/cli.go",
			importPath: modulePath + "/app/gatewayapp/acpagent",
			want:       "production code must not depend on retired app/gatewayapp/acpagent; use surfaces/acp",
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
			name:       "production rejects retired ACP event stream",
			rel:        "control/appserver/feed.go",
			importPath: modulePath + "/protocol/acp/eventstream",
			want:       "production code must not depend on retired protocol/acp/eventstream; use control/appserver/eventstream",
		},
		{
			name:       "Surface accepts Control client event stream",
			rel:        "surfaces/headless/headless.go",
			importPath: modulePath + "/control/appserver/eventstream",
			want:       "",
		},
		{
			name:       "Control ACP permission accepts Control event stream",
			rel:        "control/acppermission/coordination.go",
			importPath: modulePath + "/control/appserver/eventstream",
			want:       "",
		},
		{
			name:       "production rejects retired ACP semantic adapter",
			rel:        "control/appserver/projection/gateway.go",
			importPath: modulePath + "/protocol/acp/semantic",
			want:       "production code must not depend on retired protocol/acp/semantic; use control/acppermission",
		},
		{
			name:       "AppServer accepts Control-owned Task observation",
			rel:        "control/appserver/appserver.go",
			importPath: modulePath + "/control/appserver/taskstream",
			want:       "",
		},
		{
			name:       "production rejects retired root ACP facade",
			rel:        "surfaces/acp/server.go",
			importPath: modulePath + "/protocol/acp",
			want:       "production code must not depend on the retired root protocol/acp facade; import the owning ACP subpackage",
		},
		{
			name:       "production rejects retired ACP Task observation",
			rel:        "control/appserver/appserver.go",
			importPath: modulePath + "/protocol/acp/taskstream",
			want:       "production code must not depend on retired protocol/acp/taskstream; use control/appserver/taskstream",
		},
		{
			name:       "production rejects retired ACP projector",
			rel:        "internal/kernel/gateway_projection.go",
			importPath: modulePath + "/protocol/acp/projector",
			want:       "production code must not depend on retired protocol/acp/projector; use control/appserver/projection",
		},
		{
			name:       "other control packages reject ACP protocol dependencies",
			rel:        "control/modelconfig/build.go",
			importPath: modulePath + "/protocol/acp/schema",
			want:       "control must depend only on Control peers and reusable SDK packages",
		},
		{
			name:       "AppServer rejects unrelated ACP adapters",
			rel:        "control/appserver/client.go",
			importPath: modulePath + "/protocol/acp/control",
			want:       "production code must not depend on retired protocol/acp/control; use internal/controlprompt, control/status, or surfaces/internal/promptview",
		},
		{
			name:       "control server rejects gateway stack assembly",
			rel:        "app/controlserver/server.go",
			importPath: modulePath + "/app/gatewayapp",
			want:       "app/controlserver must depend on explicit Control contracts, not gatewayapp assembly",
		},
		{
			name:       "surface rejects Host-private control adapter",
			rel:        "surfaces/tui/app/defaults.go",
			importPath: modulePath + "/app/gatewayapp/controladapter",
			want:       "app/gatewayapp/controladapter is Host-private; only its local adapter may consume it directly",
		},
		{
			name:       "local adapter accepts Host-private control adapter",
			rel:        "app/gatewayapp/controladapter/local/appserver.go",
			importPath: modulePath + "/app/gatewayapp/controladapter",
			want:       "",
		},
		{
			name:       "root control adapter rejects gateway Host assembly",
			rel:        "app/gatewayapp/controladapter/runtime_deps.go",
			importPath: modulePath + "/app/gatewayapp",
			want:       "root controladapter must not depend on the concrete gatewayapp Host; translation belongs to controladapter/local",
		},
		{
			name:       "root control adapter test may use gateway Host fixtures",
			rel:        "app/gatewayapp/controladapter/runtime_deps_test.go",
			importPath: modulePath + "/app/gatewayapp",
			want:       "",
		},
		{
			name:       "local adapter owns gateway Host translation",
			rel:        "app/gatewayapp/controladapter/local/runtime_deps.go",
			importPath: modulePath + "/app/gatewayapp",
			want:       "",
		},
		{
			name:       "local presentation adapter rejects ACP wire types",
			rel:        "app/gatewayapp/controladapter/local/presentation_service.go",
			importPath: modulePath + "/protocol/acp/schema",
			want:       "controladapter/local presentation must consume protocol-neutral control/appserver types, not ACP wire types",
		},
		{
			name:       "local terminal adapter accepts Control Task projection",
			rel:        "app/gatewayapp/controladapter/local/terminal_service.go",
			importPath: modulePath + "/control/appserver/taskstream",
			want:       "",
		},
		{
			name:       "CLI accepts composed local adapter",
			rel:        "internal/cli/host_clients.go",
			importPath: modulePath + "/app/gatewayapp/controladapter/local",
			want:       "",
		},
		{
			name:       "ACP bridge rejects presentation package dependency",
			rel:        "internal/acpagentbridge/prompt_bridge.go",
			importPath: modulePath + "/surfaces/internal/promptview",
			want:       "internal/acpagentbridge must receive presentation projection through assembly, not import surfaces",
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
			name:       "surface rejects app implementation",
			rel:        "surfaces/gui/app.go",
			importPath: modulePath + "/app/gatewayapp",
			want:       "surfaces must not depend directly on app",
		},
		{
			name:       "app production rejects presentation dependency",
			rel:        "app/gatewayapp/host.go",
			importPath: modulePath + "/surfaces/headless",
			want:       "app production code must compose through Control contracts, not depend on presentation surfaces",
		},
		{
			name:       "app integration test may compose a surface",
			rel:        "app/gatewayapp/headless_test.go",
			importPath: modulePath + "/surfaces/headless",
			want:       "",
		},
		{
			name:       "surface rejects runtime implementation",
			rel:        "surfaces/gui/app.go",
			importPath: modulePath + "/agent-sdk/runtime",
			want:       "surfaces must use Control clients and projected Envelopes, not Runtime or Kernel",
		},
		{
			name:       "AppServer prompt adapter rejects Host implementation",
			rel:        "internal/controlprompt/appserveradapter/adapter.go",
			importPath: modulePath + "/app/gatewayapp",
			want:       "AppServer prompt adapter must depend on Control clients and surface-neutral contracts, not Host, Surface, Runtime, or Kernel",
		},
		{
			name:       "AppServer prompt adapter rejects Surface implementation",
			rel:        "internal/controlprompt/appserveradapter/adapter.go",
			importPath: modulePath + "/surfaces/internal/promptview",
			want:       "AppServer prompt adapter must depend on Control clients and surface-neutral contracts, not Host, Surface, Runtime, or Kernel",
		},
		{
			name:       "AppServer prompt adapter rejects Kernel implementation",
			rel:        "internal/controlprompt/appserveradapter/adapter.go",
			importPath: modulePath + "/internal/kernel",
			want:       "AppServer prompt adapter must depend on Control clients and surface-neutral contracts, not Host, Surface, Runtime, or Kernel",
		},
		{
			name:       "AppServer prompt adapter rejects Runtime implementation",
			rel:        "internal/controlprompt/appserveradapter/adapter.go",
			importPath: modulePath + "/agent-sdk/runtime/chat",
			want:       "AppServer prompt adapter must depend on Control clients and surface-neutral contracts, not Host, Surface, Runtime, or Kernel",
		},
		{
			name:       "product ACP server rejects kernel implementation",
			rel:        "surfaces/acp/server.go",
			importPath: modulePath + "/internal/kernel",
			want:       "product ACP assembly must use principal-bound AppServer clients, not Runtime or Kernel",
		},
		{
			name:       "product ACP rejects runtime implementation",
			rel:        "surfaces/acp/agent.go",
			importPath: modulePath + "/agent-sdk/runtime/chat",
			want:       "product ACP assembly must use principal-bound AppServer clients, not Runtime or Kernel",
		},
		{
			name:       "product ACP rejects kernel implementation",
			rel:        "surfaces/acp/agent.go",
			importPath: modulePath + "/internal/kernel",
			want:       "product ACP assembly must use principal-bound AppServer clients, not Runtime or Kernel",
		},
		{
			name:       "product ACP Surface rejects prompt router assembly",
			rel:        "surfaces/acp/agent.go",
			importPath: modulePath + "/internal/controlprompt/appserveradapter",
			want:       "product ACP Surface must not select Session authority or assemble prompt routing; use internal/acpagentbridge gateway assembly",
		},
		{
			name:       "product ACP Surface rejects system Session selection",
			rel:        "surfaces/acp/agent.go",
			importPath: modulePath + "/control/sessionvisibility",
			want:       "product ACP Surface must not select Session authority or assemble prompt routing; use internal/acpagentbridge gateway assembly",
		},
		{
			name:       "production file rejects testing dependency",
			rel:        "internal/acpagentbridge/fakes.go",
			importPath: "testing",
			want:       "production Go files must not import testing outside explicit test-support packages",
		},
		{
			name:       "test file accepts testing dependency",
			rel:        "internal/acpagentbridge/fakes_test.go",
			importPath: "testing",
			want:       "",
		},
		{
			name:       "test support package accepts testing dependency",
			rel:        "internal/testenv/http.go",
			importPath: "testing",
			want:       "",
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

func TestSemanticBoundaryRuleRejectsConcreteHostInLocalLeaf(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/caelis-labs/caelis"
	source := `package local

import "github.com/caelis-labs/caelis/app/gatewayapp"

type StatusService struct { host *gatewayapp.Stack }
`
	rule, subject, _ := semanticRuleForSource(t, "app/gatewayapp/controladapter/local/status_service.go", source, modulePath)
	if !strings.Contains(rule, "only controladapter/local NewAppServer") || subject != "gatewayapp.Stack" {
		t.Fatalf("semantic rule = (%q, %q), want concrete Host leaf rejection", rule, subject)
	}
}

func TestSemanticBoundaryRuleAllowsConcreteHostAtLocalCompositionRoot(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/caelis-labs/caelis"
	source := `package local

import "github.com/caelis-labs/caelis/app/gatewayapp"

func NewAppServer(host *gatewayapp.Stack) {}
`
	rule, subject, _ := semanticRuleForSource(t, "app/gatewayapp/controladapter/local/appserver.go", source, modulePath)
	if rule != "" || subject != "" {
		t.Fatalf("semantic rule = (%q, %q), want local composition root allowed", rule, subject)
	}
}

func TestSemanticBoundaryRuleRejectsOtherConcreteHostUseInLocalCompositionFile(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/caelis-labs/caelis"
	source := `package local

import "github.com/caelis-labs/caelis/app/gatewayapp"

func NewAppServer(host *gatewayapp.Stack) {}
func retain(host *gatewayapp.Stack) {}
`
	rule, subject, _ := semanticRuleForSource(t, "app/gatewayapp/controladapter/local/appserver.go", source, modulePath)
	if !strings.Contains(rule, "only controladapter/local NewAppServer") || subject != "gatewayapp.Stack" {
		t.Fatalf("semantic rule = (%q, %q), want secondary concrete Host use rejected", rule, subject)
	}
}

func TestSemanticBoundaryRuleRejectsLocalGatewayDotImport(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/caelis-labs/caelis"
	source := `package local

import . "github.com/caelis-labs/caelis/app/gatewayapp"

func retain(host *Stack) {}
`
	rule, subject, _ := semanticRuleForSource(t, "app/gatewayapp/controladapter/local/status_service.go", source, modulePath)
	if !strings.Contains(rule, "must name gatewayapp imports") || !strings.HasPrefix(subject, ". import") {
		t.Fatalf("semantic rule = (%q, %q), want gatewayapp dot import rejection", rule, subject)
	}
}

func TestSemanticBoundaryRuleAllowsFocusedGatewayServiceInLocalAdapter(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/caelis-labs/caelis"
	source := `package local

import "github.com/caelis-labs/caelis/app/gatewayapp"

func project(reads gatewayapp.KernelReadService) {}
`
	rule, subject, _ := semanticRuleForSource(t, "app/gatewayapp/controladapter/local/local_stack.go", source, modulePath)
	if rule != "" || subject != "" {
		t.Fatalf("semantic rule = (%q, %q), want focused gateway service allowed", rule, subject)
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
	rule, subject, _ := semanticRuleForSource(t, "control/appserver/projection/demo.go", source, modulePath)
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
	rule, subject, _ := semanticRuleForSource(t, "control/appserver/projection/demo.go", source, modulePath)
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
			name:    "deleted control client package fails",
			rel:     "control/client/service.go",
			want:    "must not recreate control/client; the surface-facing product boundary belongs to control/appserver",
			wantSub: "control/client",
		},
		{
			name:    "deleted ACP projector path fails",
			rel:     "protocol/acp/projector/projector.go",
			want:    "must not recreate protocol/acp/projector; canonical Session projection belongs to control/appserver/projection",
			wantSub: "protocol/acp/projector",
		},
		{
			name:    "deleted ACP event stream path fails",
			rel:     "protocol/acp/eventstream/event.go",
			want:    "must not recreate protocol/acp/eventstream; the Control client event contract belongs to control/appserver/eventstream",
			wantSub: "protocol/acp/eventstream",
		},
		{
			name:    "deleted ACP schema path fails",
			rel:     "protocol/acp/schema/update.go",
			want:    "must not recreate protocol/acp/schema; Control Envelope update and permission payloads belong to control/appserver/eventstream",
			wantSub: "protocol/acp/schema",
		},
		{
			name:    "deleted ACP semantic path fails",
			rel:     "protocol/acp/semantic/coordination.go",
			want:    "must not recreate protocol/acp/semantic; ACP permission translation belongs to control/acppermission",
			wantSub: "protocol/acp/semantic",
		},
		{
			name:    "deleted appserver surface fails",
			rel:     "surfaces/appserver/server.go",
			want:    "must not recreate surfaces/appserver; the Control Host belongs to app/controlserver and its typed clients and wire codec to control/appserver",
			wantSub: "surfaces/appserver",
		},
		{
			name:    "deleted prompt projection surface fails",
			rel:     "surfaces/promptview/status.go",
			want:    "must not recreate surfaces/promptview; shared presentation projection belongs to surfaces/internal/promptview",
			wantSub: "surfaces/promptview",
		},
		{
			name:    "deleted status projection surface fails",
			rel:     "surfaces/statusbar/status.go",
			want:    "must not recreate surfaces/statusbar; shared presentation projection belongs to surfaces/internal/statusbar",
			wantSub: "surfaces/statusbar",
		},
		{
			name:    "deleted transcript projection surface fails",
			rel:     "surfaces/transcript/event.go",
			want:    "must not recreate surfaces/transcript; shared presentation projection belongs to surfaces/internal/transcript",
			wantSub: "surfaces/transcript",
		},
		{
			name:    "deleted ACP server surface fails",
			rel:     "surfaces/acpserver/server.go",
			want:    "must not recreate surfaces/acpserver; product ACP Surface ownership belongs to surfaces/acp",
			wantSub: "surfaces/acpserver",
		},
		{
			name:    "deleted app ACP assembly fails",
			rel:     "app/gatewayapp/acpagent/agent.go",
			want:    "must not recreate app/gatewayapp/acpagent; product ACP Surface ownership belongs to surfaces/acp",
			wantSub: "app/gatewayapp/acpagent",
		},
		{
			name:    "deleted Control protocol package fails",
			rel:     "protocol/control/v1/wire.go",
			want:    "must not recreate protocol/control; the domain-bound Control wire codec belongs to control/appserver/wirev1",
			wantSub: "protocol/control/v1",
		},
		{
			name:    "deleted product control client port fails",
			rel:     "ports/controlclient/service.go",
			want:    "must not recreate ports/controlclient; product client contracts and behavior belong to control/appserver",
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
			name:    "deleted sdk model catalog path fails",
			rel:     "agent-sdk/model/catalog/model_catalog.go",
			want:    "must not recreate agent-sdk/model/catalog; concrete model catalogs belong to control/modelcatalog",
			wantSub: "agent-sdk/model/catalog",
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
			name:    "deleted root ACP facade fails",
			rel:     "protocol/acp/agent.go",
			want:    "must not recreate the root protocol/acp facade; standard wire contracts belong to acp-go-sdk and residual code to an owning ACP subpackage",
			wantSub: "protocol/acp",
		},
		{
			name:    "deleted protocol control path fails",
			rel:     "protocol/acp/control/service.go",
			want:    "must not recreate protocol/acp/control; prompt contracts belong to internal/controlprompt, status data to control/status, and rendering to surfaces/internal/promptview",
			wantSub: "protocol/acp/control",
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

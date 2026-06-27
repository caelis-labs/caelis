package main

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestBoundaryRuleRejectsPublicContractsImportingInternal(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/OnslaughtSnail/caelis"
	tests := []struct {
		name       string
		rel        string
		importPath string
		want       string
	}{
		{
			name:       "ports must not import internal packages",
			rel:        "ports/gateway/service.go",
			importPath: modulePath + "/internal/kernel",
			want:       "ports must not depend on internal packages",
		},
		{
			name:       "root kernel must not mirror internal kernel",
			rel:        "kernel/service.go",
			importPath: modulePath + "/internal/kernel",
			want:       "kernel must not depend on internal/kernel",
		},
		{
			name:       "internal kernel production code must not import impl",
			rel:        "internal/kernel/gateway.go",
			importPath: modulePath + "/impl/session/memory",
			want:       "internal/kernel must not depend on app, impl, or surfaces",
		},
		{
			name:       "internal kernel may import public display policy",
			rel:        "internal/kernel/stream_projection.go",
			importPath: modulePath + "/ports/displaypolicy",
			want:       "",
		},
		{
			name:       "protocol must not import internal packages",
			rel:        "protocol/acp/client/client.go",
			importPath: modulePath + "/internal/kernel",
			want:       "protocol must not depend on internal packages",
		},
		{
			name:       "protocol must not import surfaces",
			rel:        "protocol/acp/projector/projector.go",
			importPath: modulePath + "/surfaces/tui/app",
			want:       "protocol must not depend on app, impl, or surfaces",
		},
		{
			name:       "surfaces must not import app",
			rel:        "surfaces/gui/app.go",
			importPath: modulePath + "/app/gatewayapp",
			want:       "surfaces must not depend directly on app",
		},
		{
			name:       "surfaces must not import impl",
			rel:        "surfaces/gui/app.go",
			importPath: modulePath + "/impl/model/providers",
			want:       "surfaces must not depend directly on impl",
		},
		{
			name:       "protocol projector must not import old internal display policy",
			rel:        "protocol/acp/projector/projector.go",
			importPath: modulePath + "/internal/displaypolicy",
			want:       "protocol must not depend on internal packages",
		},
		{
			name:       "protocol projector may import public display policy",
			rel:        "protocol/acp/projector/projector.go",
			importPath: modulePath + "/ports/displaypolicy",
			want:       "",
		},
		{
			name:       "protocol must not import root internal winproc",
			rel:        "protocol/acp/transport/stdio/transport.go",
			importPath: modulePath + "/internal/winproc",
			want:       "protocol must not depend on internal packages",
		},
		{
			name:       "acp server must not import app",
			rel:        "surfaces/acpserver/server.go",
			importPath: modulePath + "/app/gatewayapp",
			want:       "surfaces must not depend directly on app",
		},
		{
			name:       "internal kernel tests may use existing session fixtures",
			rel:        "internal/kernel/gateway_test.go",
			importPath: modulePath + "/impl/session/memory",
			want:       "",
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

	const modulePath = "github.com/OnslaughtSnail/caelis"
	source := `package demo

import "github.com/OnslaughtSnail/caelis/ports/session"

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

func TestSemanticBoundaryRuleRejectsDirectEventProtocolAliasReads(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/OnslaughtSnail/caelis"
	source := `package demo

import "github.com/OnslaughtSnail/caelis/ports/session"

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

	const modulePath = "github.com/OnslaughtSnail/caelis"
	source := `package demo

import "github.com/OnslaughtSnail/caelis/ports/session"

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

func TestSemanticBoundaryRuleAllowsPortsSessionEventProtocolAliases(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/OnslaughtSnail/caelis"
	source := `package session

func normalize(protocol EventProtocol) bool {
	return protocol.Participant != nil
}
`
	rule, subject, _ := semanticRuleForSource(t, "ports/session/protocol.go", source, modulePath)
	if rule != "" || subject != "" {
		t.Fatalf("semantic rule = (%q, %q), want ports/session alias access allowed", rule, subject)
	}
}

func TestSemanticBoundaryRuleRejectsEventProtocolHandoffAliasWrite(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/OnslaughtSnail/caelis"
	source := `package demo

import "github.com/OnslaughtSnail/caelis/ports/session"

func writeAlias() *session.EventProtocol {
	return &session.EventProtocol{
		Handoff: &session.ProtocolHandoff{Phase: "activation"},
	}
}
`
	rule, subject, _ := semanticRuleForSource(t, "impl/agent/local/demo.go", source, modulePath)
	if !strings.Contains(rule, "EventProtocol") || subject != "EventProtocol.Handoff" {
		t.Fatalf("semantic rule = (%q, %q), want Handoff alias write rejection", rule, subject)
	}
}

func TestSemanticBoundaryRuleRejectsTopLevelTerminalMetaKeys(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/OnslaughtSnail/caelis"
	source := `package demo

var meta = map[string]any{
	"terminal_output": "stdout",
}
`
	rule, subject, _ := semanticRuleForSource(t, "protocol/acp/projector/demo.go", source, modulePath)
	if !strings.Contains(rule, "terminal metadata") || subject != "terminal_output" {
		t.Fatalf("semantic rule = (%q, %q), want top-level terminal metadata rejection", rule, subject)
	}
}

func TestSemanticBoundaryRuleRejectsNonWhitelistedGatewayBridgeCall(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/OnslaughtSnail/caelis"
	source := `package demo

import (
	"github.com/OnslaughtSnail/caelis/ports/gateway"
	acpprojector "github.com/OnslaughtSnail/caelis/protocol/acp/projector"
)

func bridge(env gateway.EventEnvelope) {
	_ = acpprojector.ProjectGatewayEventEnvelope(env)
}
`
	rule, subject, _ := semanticRuleForSource(t, "app/gatewayapp/demo.go", source, modulePath)
	if !strings.Contains(rule, "ProjectGatewayEventEnvelope") || subject != "acpprojector.ProjectGatewayEventEnvelope" {
		t.Fatalf("semantic rule = (%q, %q), want gateway bridge rejection", rule, subject)
	}
}

func TestSemanticBoundaryRuleAllowsWhitelistedGatewayBridgeCall(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/OnslaughtSnail/caelis"
	source := `package kernel

import (
	"github.com/OnslaughtSnail/caelis/ports/gateway"
	acpprojector "github.com/OnslaughtSnail/caelis/protocol/acp/projector"
)

func bridge(env gateway.EventEnvelope) {
	_ = acpprojector.ProjectGatewayEventEnvelope(env)
}
`
	rule, subject, _ := semanticRuleForSource(t, "internal/kernel/handle.go", source, modulePath)
	if rule != "" || subject != "" {
		t.Fatalf("semantic rule = (%q, %q), want whitelisted gateway bridge allowed", rule, subject)
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

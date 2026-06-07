package main

import "testing"

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
			name:       "runner must not import builtin tool implementations",
			rel:        "runner/runner.go",
			importPath: modulePath + "/tool/builtin/spawn",
			want:       "runner must not depend on built-in tool implementations",
		},
		{
			name:       "runner must not import app",
			rel:        "runner/runner.go",
			importPath: modulePath + "/app",
			want:       "runner must not depend on control or presentation packages",
		},
		{
			name:       "runner must not import acp protocol",
			rel:        "runner/runner.go",
			importPath: modulePath + "/protocol/acp",
			want:       "runner must not depend on ACP, control, or presentation packages",
		},
		{
			name:       "sandbox must not import app composition",
			rel:        "sandbox/backend.go",
			importPath: modulePath + "/app/gatewayapp",
			want:       "sandbox must not depend on app, cmd, protocol, impl, or surfaces",
		},
		{
			name:       "sandbox must not import command surfaces",
			rel:        "sandbox/seatbelt/backend.go",
			importPath: modulePath + "/cmd/caelis/internal/cli",
			want:       "sandbox must not depend on app, cmd, protocol, impl, or surfaces",
		},
		{
			name:       "acp protocol must not import old ports",
			rel:        "protocol/acp/projector/projector.go",
			importPath: modulePath + "/ports/session",
			want:       "protocol/acp must not depend on old ports",
		},
		{
			name:       "active internals must not import old ports",
			rel:        "internal/sandboxrouter/router.go",
			importPath: modulePath + "/ports/sandbox",
			want:       "active packages must not depend on old ports",
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

func TestDeletedLegacyRootRuleRejectsOldProductionRoots(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rel  string
		want string
	}{
		{
			name: "old impl root",
			rel:  "impl/model/providers/openai.go",
			want: "legacy production roots must not be active in the rewrite branch",
		},
		{
			name: "old gateway app root",
			rel:  "app/gatewayapp/stack.go",
			want: "legacy production roots must not be active in the rewrite branch",
		},
		{
			name: "old command root",
			rel:  "cmd/caelis/main.go",
			want: "legacy production roots must not be active in the rewrite branch",
		},
		{
			name: "old ports root",
			rel:  "ports/session/session.go",
			want: "legacy production roots must not be active in the rewrite branch",
		},
		{
			name: "old internal kernel root",
			rel:  "internal/kernel/gateway.go",
			want: "legacy production roots must not be active in the rewrite branch",
		},
		{
			name: "layer4 provider remains allowed",
			rel:  "model/providers/openai.go",
			want: "",
		},
		{
			name: "layer3 gateway placeholder remains allowed",
			rel:  "gateway/kernel/doc.go",
			want: "",
		},
		{
			name: "old ports root",
			rel:  "ports/model/model.go",
			want: "legacy production roots must not be active in the rewrite branch",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := deletedLegacyRootRule(tt.rel); got != tt.want {
				t.Fatalf("deletedLegacyRootRule(%q) = %q, want %q", tt.rel, got, tt.want)
			}
		})
	}
}

func TestTextRuleRejectsLegacyStreamEventInLayer4Runtime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rel  string
		text string
		want string
	}{
		{
			name: "model providers must use ResponseEvent",
			rel:  "model/providers/example.go",
			text: "func Generate() iter.Seq2[*model.StreamEvent, error] { return nil }",
			want: "layer4 runtime must use model.ResponseEvent, not legacy StreamEvent",
		},
		{
			name: "runner must use ResponseEvent",
			rel:  "runner/example.go",
			text: "var _ = model.StreamEventTurnDone",
			want: "layer4 runtime must use model.ResponseEvent, not legacy StreamEvent",
		},
		{
			name: "legacy impl is handled by deleted root rule",
			rel:  "impl/model/providers/example.go",
			text: "func Generate() iter.Seq2[*model.StreamEvent, error] { return nil }",
			want: "",
		},
		{
			name: "old ports text rule is superseded by deleted root rule",
			rel:  "ports/model/model.go",
			text: "type StreamEvent struct{}",
			want: "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := textRule(tt.rel, []byte(tt.text)); got != tt.want {
				t.Fatalf("textRule(%q) = %q, want %q", tt.rel, got, tt.want)
			}
		})
	}
}

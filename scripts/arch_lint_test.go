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
			name:       "internal kernel production code must not import impl",
			rel:        "internal/kernel/gateway.go",
			importPath: modulePath + "/impl/session/memory",
			want:       "internal/kernel must not depend on app, impl, or surfaces",
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
			name:       "protocol projector display policy exception",
			rel:        "protocol/acp/projector/projector.go",
			importPath: modulePath + "/internal/displaypolicy",
			want:       "",
		},
		{
			name:       "protocol must not import root internal winproc",
			rel:        "protocol/acp/transport/stdio/transport.go",
			importPath: modulePath + "/internal/winproc",
			want:       "protocol must not depend on internal packages",
		},
		{
			name:       "acp server composition exception",
			rel:        "surfaces/acpserver/server.go",
			importPath: modulePath + "/app/gatewayapp",
			want:       "",
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

package main

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestBoundaryRuleRejectsPublicContractsImportingInternal(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/caelis-labs/caelis"
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
			want:       "must not import impl/session/memory; use agent-sdk/session/memory",
		},
		{
			name:       "internal kernel production code must not import impl session file",
			rel:        "internal/kernel/gateway.go",
			importPath: modulePath + "/impl/session/file",
			want:       "must not import impl/session/file; use agent-sdk/session/file",
		},
		{
			name:       "internal kernel production code must not import impl stream memory",
			rel:        "internal/kernel/gateway.go",
			importPath: modulePath + "/impl/stream/memory",
			want:       "must not import impl/stream/memory; use agent-sdk/task/stream/memory",
		},
		{
			name:       "production code must not import impl skill fs",
			rel:        "impl/tool/builtin/skill/skill.go",
			importPath: modulePath + "/impl/skill/fs",
			want:       "must not import impl/skill/fs; use agent-sdk/skill/fs for reusable discovery and app/gatewayapp/internal/skilldiscovery for Caelis system skill discovery",
		},
		{
			name:       "production code must not import impl skill system",
			rel:        "app/gatewayapp/reconfigure.go",
			importPath: modulePath + "/impl/skill/system",
			want:       "must not import impl/skill/system; use agent-sdk/skill/fs for reusable discovery and app/gatewayapp/internal/skilldiscovery for Caelis system skill discovery",
		},
		{
			name:       "promptassembly may import skilldiscovery product bridge",
			rel:        "app/gatewayapp/internal/promptassembly/prompt.go",
			importPath: modulePath + "/app/gatewayapp/internal/skilldiscovery",
			want:       "",
		},
		{
			name:       "skilldiscovery may import agent-sdk skill fs",
			rel:        "app/gatewayapp/internal/skilldiscovery/bridge.go",
			importPath: modulePath + "/agent-sdk/skill/fs",
			want:       "",
		},
		{
			name:       "skilldiscovery must not import impl packages",
			rel:        "app/gatewayapp/internal/skilldiscovery/bridge.go",
			importPath: modulePath + "/impl/skill/system",
			want:       "must not import impl/skill/system; use agent-sdk/skill/fs for reusable discovery and app/gatewayapp/internal/skilldiscovery for Caelis system skill discovery",
		},
		{
			name:       "agent-sdk skill fs must not import impl packages",
			rel:        "agent-sdk/skill/fs/discovery_meta.go",
			importPath: modulePath + "/impl/skill/system",
			want:       "must not import impl/skill/system; use agent-sdk/skill/fs for reusable discovery and app/gatewayapp/internal/skilldiscovery for Caelis system skill discovery",
		},
		{
			name:       "production code must not import impl policy presets",
			rel:        "app/gatewayapp/runtime_config.go",
			importPath: modulePath + "/impl/policy/presets",
			want:       "must not import impl/policy/presets; use agent-sdk/policy/presets",
		},
		{
			name:       "impl policy presets compat may import agent-sdk policy presets",
			rel:        "impl/policy/presets/presets.go",
			importPath: modulePath + "/agent-sdk/policy/presets",
			want:       "",
		},
		{
			name:       "impl policy presets compat must not import ports policy",
			rel:        "impl/policy/presets/presets.go",
			importPath: modulePath + "/ports/policy",
			want:       "production code must not depend on ports/policy; use agent-sdk/policy",
		},
		{
			name:       "agent-sdk policy must not import ports packages",
			rel:        "agent-sdk/policy/policy.go",
			importPath: modulePath + "/ports/policy",
			want:       "production code must not depend on ports/policy; use agent-sdk/policy",
		},
		{
			name:       "agent-sdk policy presets may import agent-sdk sandbox",
			rel:        "agent-sdk/policy/presets/presets.go",
			importPath: modulePath + "/agent-sdk/sandbox",
			want:       "",
		},
		{
			name:       "agent-sdk packages may import agent-sdk internal helpers",
			rel:        "agent-sdk/runtime/runtime.go",
			importPath: modulePath + "/agent-sdk/internal/runstate",
			want:       "",
		},
		{
			name:       "agent-sdk packages must not import retained product ports",
			rel:        "agent-sdk/runtime/runtime.go",
			importPath: modulePath + "/ports/plugin",
			want:       "agent-sdk/runtime must not depend on product-host or old ports packages",
		},
		{
			name:       "agent-sdk packages must not import platform packages",
			rel:        "agent-sdk/runtime/runtime.go",
			importPath: modulePath + "/platform/runtime",
			want:       "agent-sdk must not depend on non-SDK Caelis packages",
		},
		{
			name:       "impl agent local compat may import agent-sdk runtime",
			rel:        "impl/agent/local/local.go",
			importPath: modulePath + "/agent-sdk/runtime",
			want:       "",
		},
		{
			name:       "impl agent local compat must not import ports policy",
			rel:        "impl/agent/local/local.go",
			importPath: modulePath + "/ports/policy",
			want:       "production code must not depend on ports/policy; use agent-sdk/policy",
		},
		{
			name:       "impl agent local compat tests may import agent-sdk runtime",
			rel:        "impl/agent/local/compat_test.go",
			importPath: modulePath + "/agent-sdk/runtime/compact",
			want:       "",
		},
		{
			name:       "internal kernel must not import ports display policy",
			rel:        "internal/kernel/stream_projection.go",
			importPath: modulePath + "/ports/displaypolicy",
			want:       "production code must not depend on ports/displaypolicy; use agent-sdk/display",
		},
		{
			name:       "internal kernel may import agent-sdk display",
			rel:        "internal/kernel/gateway_replay_task_panels.go",
			importPath: modulePath + "/agent-sdk/display",
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
			name:       "surfaces must not import deleted impl agent local",
			rel:        "surfaces/gui/app.go",
			importPath: modulePath + "/impl/agent/local",
			want:       "must not import impl/agent/local; use agent-sdk/runtime",
		},
		{
			name:       "protocol projector must not import old internal display policy",
			rel:        "protocol/acp/projector/projector.go",
			importPath: modulePath + "/internal/displaypolicy",
			want:       "protocol must not depend on internal packages",
		},
		{
			name:       "protocol projector must not import ports display policy",
			rel:        "protocol/acp/projector/projector.go",
			importPath: modulePath + "/ports/displaypolicy",
			want:       "production code must not depend on ports/displaypolicy; use agent-sdk/display",
		},
		{
			name:       "protocol projector may import agent-sdk display",
			rel:        "protocol/acp/projector/projector.go",
			importPath: modulePath + "/agent-sdk/display",
			want:       "",
		},
		{
			name:       "agent-sdk runtime must not import ports compact",
			rel:        "agent-sdk/runtime/compaction.go",
			importPath: modulePath + "/ports/compact",
			want:       "production code must not depend on ports/compact; use agent-sdk/runtime/compact",
		},
		{
			name:       "agent-sdk runtime tests must not import ports compact",
			rel:        "agent-sdk/runtime/runtime_compaction_test.go",
			importPath: modulePath + "/ports/compact",
			want:       "production code must not depend on ports/compact; use agent-sdk/runtime/compact",
		},
		{
			name:       "agent-sdk runtime tests must not import ports assembly",
			rel:        "agent-sdk/runtime/runtime_test.go",
			importPath: modulePath + "/ports/assembly",
			want:       "production code must not depend on ports/assembly; use internal/controlassembly",
		},
		{
			name:       "production code must not import ports assembly",
			rel:        "app/gatewayapp/stack.go",
			importPath: modulePath + "/ports/assembly",
			want:       "production code must not depend on ports/assembly; use internal/controlassembly",
		},
		{
			name:       "production code must not import ports agent",
			rel:        "internal/kernel/gateway_turns.go",
			importPath: modulePath + "/ports/agent",
			want:       "production code must not depend on ports/agent; use agent-sdk",
		},
		{
			name:       "production code may import agent-sdk root",
			rel:        "internal/kernel/gateway_turns.go",
			importPath: modulePath + "/agent-sdk",
			want:       "",
		},
		{
			name:       "production code must not import ports session",
			rel:        "internal/kernel/gateway_turns.go",
			importPath: modulePath + "/ports/session",
			want:       "production code must not depend on ports/session; use agent-sdk/session",
		},
		{
			name:       "production code must not import ports model",
			rel:        "internal/kernel/gateway_turns.go",
			importPath: modulePath + "/ports/model",
			want:       "production code must not depend on ports/model; use agent-sdk/model",
		},
		{
			name:       "production code must not import ports sandbox",
			rel:        "app/gatewayapp/sandbox_service.go",
			importPath: modulePath + "/ports/sandbox",
			want:       "production code must not depend on ports/sandbox; use agent-sdk/sandbox",
		},
		{
			name:       "production code must not import ports policy",
			rel:        "internal/kernel/gateway_request_policy.go",
			importPath: modulePath + "/ports/policy",
			want:       "production code must not depend on ports/policy; use agent-sdk/policy",
		},
		{
			name:       "ports gateway may import agent-sdk root",
			rel:        "ports/gateway/types.go",
			importPath: modulePath + "/agent-sdk",
			want:       "",
		},
		{
			name:       "ports gateway must not import ports agent",
			rel:        "ports/gateway/types.go",
			importPath: modulePath + "/ports/agent",
			want:       "production code must not depend on ports/agent; use agent-sdk",
		},
		{
			name:       "impl session memory compat tests must not import ports model",
			rel:        "impl/session/memory/compat_test.go",
			importPath: modulePath + "/ports/model",
			want:       "production code must not depend on ports/model; use agent-sdk/model",
		},
		{
			name:       "agent-sdk runtime must not import ports controller",
			rel:        "agent-sdk/runtime/controlplane.go",
			importPath: modulePath + "/ports/controller",
			want:       "production code must not depend on ports/controller; use agent-sdk/runtime/controller",
		},
		{
			name:       "agent-sdk runtime tests must not import ports controller",
			rel:        "agent-sdk/runtime/runtime_test.go",
			importPath: modulePath + "/ports/controller",
			want:       "production code must not depend on ports/controller; use agent-sdk/runtime/controller",
		},
		{
			name:       "production code must not import impl agent local",
			rel:        "app/gatewayapp/stack.go",
			importPath: modulePath + "/impl/agent/local",
			want:       "must not import impl/agent/local; use agent-sdk/runtime",
		},
		{
			name:       "production code must not import impl agent local chat",
			rel:        "app/gatewayapp/system_agent_runtime.go",
			importPath: modulePath + "/impl/agent/local/chat",
			want:       "must not import impl/agent/local/chat; use agent-sdk/runtime/chat",
		},
		{
			name:       "impl agent local compat may import agent-sdk runtime chat",
			rel:        "impl/agent/local/chat/chat.go",
			importPath: modulePath + "/agent-sdk/runtime/chat",
			want:       "",
		},
		{
			name:       "production code must not import ports controller",
			rel:        "app/gatewayapp/services.go",
			importPath: modulePath + "/ports/controller",
			want:       "production code must not depend on ports/controller; use agent-sdk/runtime/controller",
		},
		{
			name:       "agent-sdk runtime controller must not import ports controller",
			rel:        "agent-sdk/runtime/controller/controller.go",
			importPath: modulePath + "/ports/controller",
			want:       "production code must not depend on ports/controller; use agent-sdk/runtime/controller",
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
			name:       "internal kernel tests may use agent-sdk session fixtures",
			rel:        "internal/kernel/gateway_test.go",
			importPath: modulePath + "/agent-sdk/session/memory",
			want:       "",
		},
		{
			name:       "production code must not import impl model providers",
			rel:        "app/gatewayapp/services.go",
			importPath: modulePath + "/impl/model/providers",
			want:       "must not import impl/model/providers; use agent-sdk/model/providers",
		},
		{
			name:       "production code must not import impl model catalog",
			rel:        "app/gatewayapp/services.go",
			importPath: modulePath + "/impl/model/catalog",
			want:       "must not import impl/model/catalog; use agent-sdk/model/catalog",
		},
		{
			name:       "production code must not import impl approval agentreview",
			rel:        "app/gatewayapp/reconfigure.go",
			importPath: modulePath + "/impl/approval/agentreview",
			want:       "must not import impl/approval/agentreview; use agent-sdk/approval",
		},
		{
			name:       "impl model providers compat may import agent-sdk model providers",
			rel:        "impl/model/providers/factory.go",
			importPath: modulePath + "/agent-sdk/model/providers",
			want:       "",
		},
		{
			name:       "impl model providers compat must not import ports/model",
			rel:        "impl/model/providers/factory.go",
			importPath: modulePath + "/ports/model",
			want:       "production code must not depend on ports/model; use agent-sdk/model",
		},
		{
			name:       "impl model providers compat tests may import agent-sdk model",
			rel:        "impl/model/providers/compat_test.go",
			importPath: modulePath + "/agent-sdk/model",
			want:       "",
		},
		{
			name:       "agent-sdk model providers must not import ports/model",
			rel:        "agent-sdk/model/providers/gemini.go",
			importPath: modulePath + "/ports/model",
			want:       "production code must not depend on ports/model; use agent-sdk/model",
		},
		{
			name:       "agent-sdk model providers must not import impl/model/internal/codefreecaps",
			rel:        "agent-sdk/model/providers/discovery.go",
			importPath: modulePath + "/impl/model/internal/codefreecaps",
			want:       "must not import impl/model/internal/codefreecaps; use agent-sdk/model/codefreecaps",
		},
		{
			name:       "agent-sdk model providers must not import impl model catalog",
			rel:        "agent-sdk/model/providers/factory.go",
			importPath: modulePath + "/impl/model/catalog",
			want:       "must not import impl/model/catalog; use agent-sdk/model/catalog",
		},
		{
			name:       "codefreecaps must not import impl model catalog",
			rel:        "agent-sdk/model/codefreecaps/codefreecaps.go",
			importPath: modulePath + "/impl/model/catalog",
			want:       "must not import impl/model/catalog; use agent-sdk/model/catalog",
		},
		{
			name:       "codefreecaps must not import ports packages",
			rel:        "agent-sdk/model/codefreecaps/codefreecaps.go",
			importPath: modulePath + "/ports/model",
			want:       "production code must not depend on ports/model; use agent-sdk/model",
		},
		{
			name:       "production code must not import impl tool registry",
			rel:        "app/gatewayapp/reconfigure.go",
			importPath: modulePath + "/impl/tool/registry",
			want:       "must not import impl/tool/registry; use agent-sdk/tool/registry",
		},
		{
			name:       "impl tool registry compat may import agent-sdk tool registry",
			rel:        "impl/tool/registry/memory.go",
			importPath: modulePath + "/agent-sdk/tool/registry",
			want:       "",
		},
		{
			name:       "impl tool registry compat must not import ports/tool",
			rel:        "impl/tool/registry/memory.go",
			importPath: modulePath + "/ports/tool",
			want:       "production code must not depend on ports/tool; use agent-sdk/tool",
		},
		{
			name:       "impl tool registry compat tests must not import ports/tool",
			rel:        "impl/tool/registry/compat_test.go",
			importPath: modulePath + "/ports/tool",
			want:       "production code must not depend on ports/tool; use agent-sdk/tool",
		},
		{
			name:       "agent-sdk tool registry must not import ports/tool",
			rel:        "agent-sdk/tool/registry/memory.go",
			importPath: modulePath + "/ports/tool",
			want:       "production code must not depend on ports/tool; use agent-sdk/tool",
		},
		{
			name:       "agent-sdk tool registry must not import impl packages",
			rel:        "agent-sdk/tool/registry/memory.go",
			importPath: modulePath + "/impl/tool/builtin",
			want:       "must not import impl/tool/builtin; use agent-sdk/tool/builtin",
		},
		{
			name:       "impl tool builtin root compat must not import ports/tool",
			rel:        "impl/tool/builtin/core.go",
			importPath: modulePath + "/ports/tool",
			want:       "production code must not depend on ports/tool; use agent-sdk/tool",
		},
		{
			name:       "impl tool builtin may import agent-sdk/tool",
			rel:        "impl/tool/builtin/core.go",
			importPath: modulePath + "/agent-sdk/tool",
			want:       "",
		},
		{
			name:       "impl tool builtin skill compat must not import ports/skill",
			rel:        "impl/tool/builtin/skill/skill.go",
			importPath: modulePath + "/ports/skill",
			want:       "production code must not depend on ports/skill; use agent-sdk/skill",
		},
		{
			name:       "agent-sdk skill must not import ports/skill",
			rel:        "agent-sdk/skill/skill.go",
			importPath: modulePath + "/ports/skill",
			want:       "production code must not depend on ports/skill; use agent-sdk/skill",
		},
		{
			name:       "gatewayapp production must not import ports/skill",
			rel:        "app/gatewayapp/reconfigure.go",
			importPath: modulePath + "/ports/skill",
			want:       "production code must not depend on ports/skill; use agent-sdk/skill",
		},
		{
			name:       "gatewayapp production may import agent-sdk skill",
			rel:        "app/gatewayapp/reconfigure.go",
			importPath: modulePath + "/agent-sdk/skill",
			want:       "",
		},
		{
			name:       "gatewayapp compat tests must not import ports/skill",
			rel:        "app/gatewayapp/submission_references_test.go",
			importPath: modulePath + "/ports/skill",
			want:       "production code must not depend on ports/skill; use agent-sdk/skill",
		},
		{
			name:       "production code must not import impl tool mcp",
			rel:        "app/gatewayapp/reconfigure.go",
			importPath: modulePath + "/impl/tool/mcp",
			want:       "must not import impl/tool/mcp; use agent-sdk/tool/mcp",
		},
		{
			name:       "impl tool mcp compat may import agent-sdk tool mcp",
			rel:        "impl/tool/mcp/compat.go",
			importPath: modulePath + "/agent-sdk/tool/mcp",
			want:       "",
		},
		{
			name:       "impl tool mcp compat may import ports/plugin",
			rel:        "impl/tool/mcp/compat.go",
			importPath: modulePath + "/ports/plugin",
			want:       "",
		},
		{
			name:       "impl tool mcp compat must not import ports/model",
			rel:        "impl/tool/mcp/compat.go",
			importPath: modulePath + "/ports/model",
			want:       "production code must not depend on ports/model; use agent-sdk/model",
		},
		{
			name:       "agent-sdk tool mcp must not import ports/plugin",
			rel:        "agent-sdk/tool/mcp/client.go",
			importPath: modulePath + "/ports/plugin",
			want:       "agent-sdk/tool/mcp must not depend on ports packages",
		},
		{
			name:       "agent-sdk tool mcp must not import impl packages",
			rel:        "agent-sdk/tool/mcp/client.go",
			importPath: modulePath + "/impl/tool/builtin",
			want:       "must not import impl/tool/builtin; use agent-sdk/tool/builtin",
		},
		{
			name:       "production code must not import impl tool builtin internal toolutil",
			rel:        "agent-sdk/tool/builtin/filesystem/read.go",
			importPath: modulePath + "/impl/tool/builtin/internal/toolutil",
			want:       "must not import impl/tool/builtin/internal/toolutil; use agent-sdk/tool/builtin/toolutil",
		},
		{
			name:       "impl tool builtin internal toolutil compat may import agent-sdk tool builtin toolutil",
			rel:        "impl/tool/builtin/internal/toolutil/toolutil.go",
			importPath: modulePath + "/agent-sdk/tool/builtin/toolutil",
			want:       "",
		},
		{
			name:       "impl tool builtin internal toolutil compat may import agent-sdk tool",
			rel:        "impl/tool/builtin/internal/toolutil/toolutil.go",
			importPath: modulePath + "/agent-sdk/tool",
			want:       "",
		},
		{
			name:       "impl tool builtin internal toolutil compat must not import ports/model",
			rel:        "impl/tool/builtin/internal/toolutil/toolutil.go",
			importPath: modulePath + "/ports/model",
			want:       "production code must not depend on ports/model; use agent-sdk/model",
		},
		{
			name:       "agent-sdk tool builtin toolutil must not import ports/tool",
			rel:        "agent-sdk/tool/builtin/toolutil/toolutil.go",
			importPath: modulePath + "/ports/tool",
			want:       "production code must not depend on ports/tool; use agent-sdk/tool",
		},
		{
			name:       "agent-sdk tool builtin toolutil must not import impl packages",
			rel:        "agent-sdk/tool/builtin/toolutil/toolutil.go",
			importPath: modulePath + "/impl/tool/builtin",
			want:       "must not import impl/tool/builtin; use agent-sdk/tool/builtin",
		},
		{
			name:       "production code must not import impl tool internal argparse",
			rel:        "agent-sdk/tool/builtin/filesystem/read.go",
			importPath: modulePath + "/impl/tool/internal/argparse",
			want:       "must not import impl/tool/internal/argparse; use agent-sdk/tool/builtin/argparse",
		},
		{
			name:       "impl tool internal argparse compat may import agent-sdk tool builtin argparse",
			rel:        "impl/tool/internal/argparse/argparse.go",
			importPath: modulePath + "/agent-sdk/tool/builtin/argparse",
			want:       "",
		},
		{
			name:       "impl tool internal argparse compat must not import ports/tool",
			rel:        "impl/tool/internal/argparse/argparse.go",
			importPath: modulePath + "/ports/tool",
			want:       "production code must not depend on ports/tool; use agent-sdk/tool",
		},
		{
			name:       "agent-sdk tool builtin argparse must not import ports/tool",
			rel:        "agent-sdk/tool/builtin/argparse/argparse.go",
			importPath: modulePath + "/ports/tool",
			want:       "production code must not depend on ports/tool; use agent-sdk/tool",
		},
		{
			name:       "agent-sdk tool builtin argparse must not import impl packages",
			rel:        "agent-sdk/tool/builtin/argparse/argparse.go",
			importPath: modulePath + "/impl/tool/builtin",
			want:       "must not import impl/tool/builtin; use agent-sdk/tool/builtin",
		},
		{
			name:       "impl tool builtin tests must not import ports/tool",
			rel:        "impl/tool/builtin/core_test.go",
			importPath: modulePath + "/ports/tool",
			want:       "production code must not depend on ports/tool; use agent-sdk/tool",
		},
		{
			name:       "production code must not import impl tool builtin plan",
			rel:        "app/gatewayapp/reconfigure.go",
			importPath: modulePath + "/impl/tool/builtin/plan",
			want:       "must not import impl/tool/builtin/plan; use agent-sdk/tool/builtin/plan",
		},
		{
			name:       "impl tool builtin plan compat may import agent-sdk tool builtin plan",
			rel:        "impl/tool/builtin/plan/plan.go",
			importPath: modulePath + "/agent-sdk/tool/builtin/plan",
			want:       "",
		},
		{
			name:       "impl tool builtin plan compat must not import ports/model",
			rel:        "impl/tool/builtin/plan/plan.go",
			importPath: modulePath + "/ports/model",
			want:       "production code must not depend on ports/model; use agent-sdk/model",
		},
		{
			name:       "agent-sdk tool builtin plan must not import ports/tool",
			rel:        "agent-sdk/tool/builtin/plan/plan.go",
			importPath: modulePath + "/ports/tool",
			want:       "production code must not depend on ports/tool; use agent-sdk/tool",
		},
		{
			name:       "agent-sdk tool builtin spawn must not import impl packages",
			rel:        "agent-sdk/tool/builtin/spawn/tool.go",
			importPath: modulePath + "/impl/tool/builtin",
			want:       "must not import impl/tool/builtin; use agent-sdk/tool/builtin",
		},
		{
			name:       "impl tool builtin spawn compat may import agent-sdk task delegation",
			rel:        "impl/tool/builtin/spawn/tool.go",
			importPath: modulePath + "/agent-sdk/task/delegation",
			want:       "",
		},
		{
			name:       "production code must not import impl tool builtin filesystem",
			rel:        "app/gatewayapp/reconfigure.go",
			importPath: modulePath + "/impl/tool/builtin/filesystem",
			want:       "must not import impl/tool/builtin/filesystem; use agent-sdk/tool/builtin/filesystem",
		},
		{
			name:       "impl tool builtin filesystem compat may import agent-sdk tool builtin filesystem",
			rel:        "impl/tool/builtin/filesystem/compat.go",
			importPath: modulePath + "/agent-sdk/tool/builtin/filesystem",
			want:       "",
		},
		{
			name:       "impl tool builtin filesystem compat may import agent-sdk sandbox",
			rel:        "impl/tool/builtin/filesystem/compat.go",
			importPath: modulePath + "/agent-sdk/sandbox",
			want:       "",
		},
		{
			name:       "impl tool builtin filesystem compat must not import ports/sandbox",
			rel:        "impl/tool/builtin/filesystem/compat.go",
			importPath: modulePath + "/ports/sandbox",
			want:       "production code must not depend on ports/sandbox; use agent-sdk/sandbox",
		},
		{
			name:       "agent-sdk tool builtin filesystem must not import impl packages",
			rel:        "agent-sdk/tool/builtin/filesystem/read.go",
			importPath: modulePath + "/impl/tool/builtin",
			want:       "must not import impl/tool/builtin; use agent-sdk/tool/builtin",
		},
		{
			name:       "production code must not import impl tool builtin shell",
			rel:        "app/gatewayapp/reconfigure.go",
			importPath: modulePath + "/impl/tool/builtin/shell",
			want:       "must not import impl/tool/builtin/shell; use agent-sdk/tool/builtin/shell",
		},
		{
			name:       "impl tool builtin shell compat may import agent-sdk tool builtin shell",
			rel:        "impl/tool/builtin/shell/compat.go",
			importPath: modulePath + "/agent-sdk/tool/builtin/shell",
			want:       "",
		},
		{
			name:       "impl tool builtin shell compat must not import ports/sandbox",
			rel:        "impl/tool/builtin/shell/compat.go",
			importPath: modulePath + "/ports/sandbox",
			want:       "production code must not depend on ports/sandbox; use agent-sdk/sandbox",
		},
		{
			name:       "agent-sdk tool builtin shell must not import impl packages",
			rel:        "agent-sdk/tool/builtin/shell/run_command.go",
			importPath: modulePath + "/impl/sandbox/host",
			want:       "must not import impl/sandbox/host; use agent-sdk/sandbox/host",
		},
		{
			name:       "agent-sdk sandbox windows must not import impl sandbox host",
			rel:        "agent-sdk/sandbox/windows/runtime_windows.go",
			importPath: modulePath + "/impl/sandbox/host",
			want:       "must not import impl/sandbox/host; use agent-sdk/sandbox/host",
		},
		{
			name:       "production code must not import impl sandbox bwrap",
			rel:        "app/gatewayapp/sandbox_backends_linux.go",
			importPath: modulePath + "/impl/sandbox/bwrap",
			want:       "must not import impl/sandbox/bwrap; use agent-sdk/sandbox/bwrap",
		},
		{
			name:       "agent-sdk sandbox windows must not import impl sandbox policy helper",
			rel:        "agent-sdk/sandbox/windows/runtime_windows.go",
			importPath: modulePath + "/impl/sandbox/internal/policy",
			want:       "must not import impl/sandbox/internal/policy; use agent-sdk/sandbox/backend/policy",
		},
		{
			name:       "agent-sdk sandbox bwrap must not import impl packages",
			rel:        "agent-sdk/sandbox/bwrap/runtime.go",
			importPath: modulePath + "/impl/sandbox/internal/policy",
			want:       "must not import impl/sandbox/internal/policy; use agent-sdk/sandbox/backend/policy",
		},
		{
			name:       "agent-sdk sandbox backend policy may import agent-sdk sandbox backend fsboundary",
			rel:        "agent-sdk/sandbox/backend/policyfs/fs.go",
			importPath: modulePath + "/agent-sdk/sandbox/backend/fsboundary",
			want:       "",
		},
		{
			name:       "agent-sdk sandbox windows must not import impl sandbox consoleoutput",
			rel:        "agent-sdk/sandbox/windows/runtime_windows.go",
			importPath: modulePath + "/impl/sandbox/internal/consoleoutput",
			want:       "must not import impl/sandbox/internal/consoleoutput; use agent-sdk/sandbox/consoleoutput",
		},
		{
			name:       "agent-sdk sandbox host may import agent-sdk sandbox consoleoutput",
			rel:        "agent-sdk/sandbox/host/runtime.go",
			importPath: modulePath + "/agent-sdk/sandbox/consoleoutput",
			want:       "",
		},
		{
			name:       "agent-sdk sandbox windows must not import impl winps helper",
			rel:        "agent-sdk/sandbox/windows/runtime_windows.go",
			importPath: modulePath + "/impl/sandbox/internal/winps",
			want:       "must not import impl/sandbox/internal/winps; use agent-sdk/sandbox/windows/winps",
		},
		{
			name:       "agent-sdk sandbox consoleoutput must not import impl packages",
			rel:        "agent-sdk/sandbox/consoleoutput/buffer.go",
			importPath: modulePath + "/impl/sandbox/internal/consoleoutput",
			want:       "must not import impl/sandbox/internal/consoleoutput; use agent-sdk/sandbox/consoleoutput",
		},
		{
			name:       "agent-sdk sandbox textstream must not import repository internal packages",
			rel:        "agent-sdk/sandbox/textstream/utf8.go",
			importPath: modulePath + "/internal/winproc",
			want:       "agent-sdk/sandbox/textstream must not depend on repository internal packages",
		},
		{
			name:       "production code must not import impl tool builtin web",
			rel:        "app/gatewayapp/reconfigure.go",
			importPath: modulePath + "/impl/tool/builtin/web",
			want:       "must not import impl/tool/builtin/web; use agent-sdk/tool/builtin/web",
		},
		{
			name:       "agent-sdk tool builtin web must not import impl packages",
			rel:        "agent-sdk/tool/builtin/web/search.go",
			importPath: modulePath + "/impl/tool/builtin",
			want:       "must not import impl/tool/builtin; use agent-sdk/tool/builtin",
		},
		{
			name:       "production code must not import impl tool builtin root",
			rel:        "app/gatewayapp/reconfigure.go",
			importPath: modulePath + "/impl/tool/builtin",
			want:       "must not import impl/tool/builtin; use agent-sdk/tool/builtin",
		},
		{
			name:       "agent-sdk tool builtin root may import agent-sdk tool builtin skill",
			rel:        "agent-sdk/tool/builtin/core.go",
			importPath: modulePath + "/agent-sdk/tool/builtin/skill",
			want:       "",
		},
		{
			name:       "agent-sdk tool builtin root must not import impl packages",
			rel:        "agent-sdk/tool/builtin/core.go",
			importPath: modulePath + "/impl/tool/builtin/skill",
			want:       "must not import impl/tool/builtin/skill; use agent-sdk/tool/builtin/skill",
		},
		{
			name:       "production code must not import impl tool builtin skill",
			rel:        "app/gatewayapp/reconfigure.go",
			importPath: modulePath + "/impl/tool/builtin/skill",
			want:       "must not import impl/tool/builtin/skill; use agent-sdk/tool/builtin/skill",
		},
		{
			name:       "agent-sdk tool builtin skill must not import ports/skill",
			rel:        "agent-sdk/tool/builtin/skill/skill.go",
			importPath: modulePath + "/ports/skill",
			want:       "production code must not depend on ports/skill; use agent-sdk/skill",
		},
		{
			name:       "agent-sdk tool builtin skill must not import impl packages",
			rel:        "agent-sdk/tool/builtin/skill/skill.go",
			importPath: modulePath + "/impl/skill/fs",
			want:       "must not import impl/skill/fs; use agent-sdk/skill/fs for reusable discovery and app/gatewayapp/internal/skilldiscovery for Caelis system skill discovery",
		},
		{
			name:       "gatewayapp production may import agent-sdk tool builtin",
			rel:        "app/gatewayapp/reconfigure.go",
			importPath: modulePath + "/agent-sdk/tool/builtin",
			want:       "",
		},
		{
			name:       "production code must not import impl sandbox windows",
			rel:        "app/gatewayapp/sandbox_backends_windows.go",
			importPath: modulePath + "/impl/sandbox/windows",
			want:       "must not import impl/sandbox/windows; use agent-sdk/sandbox/windows",
		},
		{
			name:       "agent-sdk sandbox windows may import agent-sdk sandbox backend policy",
			rel:        "agent-sdk/sandbox/windows/runtime_windows.go",
			importPath: modulePath + "/agent-sdk/sandbox/backend/policy",
			want:       "",
		},
		{
			name:       "agent-sdk sandbox windows must not import impl sandbox windows internal pathutil",
			rel:        "agent-sdk/sandbox/windows/runtime_windows.go",
			importPath: modulePath + "/impl/sandbox/windows/internal/pathutil",
			want:       "must not import impl/sandbox/windows/internal/pathutil; use agent-sdk/sandbox/windows/internal/pathutil",
		},
		{
			name:       "production code must not import impl sandbox windows internal acl",
			rel:        "app/gatewayapp/sandbox_backends_windows.go",
			importPath: modulePath + "/impl/sandbox/windows/internal/acl",
			want:       "must not import impl/sandbox/windows/internal/acl; use agent-sdk/sandbox/windows/internal/acl",
		},
		{
			name:       "production code must not import impl sandbox winps",
			rel:        "app/gatewayapp/sandbox_backends_windows.go",
			importPath: modulePath + "/impl/sandbox/internal/winps",
			want:       "must not import impl/sandbox/internal/winps; use agent-sdk/sandbox/windows/winps",
		},
		{
			name:       "agent-sdk sandbox windows must not import repository internal packages",
			rel:        "agent-sdk/sandbox/windows/runtime_windows.go",
			importPath: modulePath + "/internal/winproc",
			want:       "agent-sdk/sandbox/windows must not depend on repository internal packages",
		},
		{
			name:       "impl sandbox test must not import ports/sandbox",
			rel:        "impl/sandbox/host/compat_test.go",
			importPath: modulePath + "/ports/sandbox",
			want:       "production code must not depend on ports/sandbox; use agent-sdk/sandbox",
		},
		{
			name:       "internal sandboxrouter may import agent-sdk sandbox",
			rel:        "internal/sandboxrouter/router.go",
			importPath: modulePath + "/agent-sdk/sandbox",
			want:       "",
		},
		{
			name:       "internal sandboxrouter must not import ports/sandbox",
			rel:        "internal/sandboxrouter/router.go",
			importPath: modulePath + "/ports/sandbox",
			want:       "production code must not depend on ports/sandbox; use agent-sdk/sandbox",
		},
		{
			name:       "app gatewayapp sandboxpolicy may import agent-sdk sandbox",
			rel:        "app/gatewayapp/internal/sandboxpolicy/config.go",
			importPath: modulePath + "/agent-sdk/sandbox",
			want:       "",
		},
		{
			name:       "app gatewayapp sandboxpolicy must not import ports/sandbox",
			rel:        "app/gatewayapp/internal/sandboxpolicy/config.go",
			importPath: modulePath + "/ports/sandbox",
			want:       "production code must not depend on ports/sandbox; use agent-sdk/sandbox",
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

import "github.com/caelis-labs/caelis/ports/gateway"

func consume(event gateway.Event) string {
	return string(event.Kind)
}
`
	rule, subject, _ := semanticRuleForSource(t, "surfaces/gui/demo.go", source, modulePath)
	if !strings.Contains(rule, "eventstream.Envelope") || subject != "gateway.Event" {
		t.Fatalf("semantic rule = (%q, %q), want gateway.Event surface rejection", rule, subject)
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

func TestSemanticBoundaryRuleRejectsGatewayAggregateAccessors(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/caelis-labs/caelis"
	tests := []struct {
		name        string
		source      string
		wantSubject string
	}{
		{
			name: "Kernel",
			source: `package demo

type stackish interface{ Kernel() any }

func run(stack stackish) any {
	return stack.Kernel()
}
`,
			wantSubject: "stack.Kernel()",
		},
		{
			name: "CurrentGateway",
			source: `package demo

type stackish interface{ CurrentGateway() any }

func run(stack stackish) any {
	return stack.CurrentGateway()
}
`,
			wantSubject: "stack.CurrentGateway()",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rule, subject, _ := semanticRuleForSource(t, "internal/cli/demo.go", tt.source, modulePath)
			if !strings.Contains(rule, "narrow Stack gateway accessors") || subject != tt.wantSubject {
				t.Fatalf("semantic rule = (%q, %q), want aggregate accessor rejection for %s", rule, subject, tt.wantSubject)
			}
		})
	}
}

func TestSemanticBoundaryRuleAllowsGatewayAggregateAccessorsInTestsAndShims(t *testing.T) {
	t.Parallel()

	const modulePath = "github.com/caelis-labs/caelis"
	source := `package demo

type stackish interface{ Kernel() any }

func run(stack stackish) any {
	return stack.Kernel()
}
`
	for _, rel := range []string{"internal/cli/demo_test.go", "app/gatewayapp/services.go"} {
		rule, subject, _ := semanticRuleForSource(t, rel, source, modulePath)
		if rule != "" || subject != "" {
			t.Fatalf("semantic rule for %s = (%q, %q), want aggregate accessor allowed", rel, rule, subject)
		}
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

func TestDeletedSDKCompatFileRuleRejectsDeletedCompatPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rel     string
		want    string
		wantSub string
	}{
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
			want:    "must not recreate impl/model/catalog; use agent-sdk/model/catalog",
			wantSub: "impl/model/catalog",
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
			name: "retained ports gateway path passes",
			rel:  "ports/gateway/types.go",
			want: "",
		},
		{
			name: "retained ports plugin path passes",
			rel:  "ports/plugin/plugin.go",
			want: "",
		},
		{
			name: "retained ports controlcommand path passes",
			rel:  "ports/controlcommand/registry.go",
			want: "",
		},
		{
			name: "retained ports controlprompt path passes",
			rel:  "ports/controlprompt/prompt.go",
			want: "",
		},
		{
			name: "retained ports agentprofile path passes",
			rel:  "ports/agentprofile/profile.go",
			want: "",
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
			rule, subject, line := deletedSDKCompatFileRule(tt.rel)
			if tt.want == "" {
				if rule != "" || subject != "" || line != 0 {
					t.Fatalf("deletedSDKCompatFileRule(%q) = (%q, %q, %d), want no violation", tt.rel, rule, subject, line)
				}
				return
			}
			if rule != tt.want {
				t.Fatalf("deletedSDKCompatFileRule(%q) rule = %q, want %q", tt.rel, rule, tt.want)
			}
			if tt.wantSub != "" && subject != tt.wantSub {
				t.Fatalf("deletedSDKCompatFileRule(%q) subject = %q, want %q", tt.rel, subject, tt.wantSub)
			}
			if line != 1 {
				t.Fatalf("deletedSDKCompatFileRule(%q) line = %d, want 1", tt.rel, line)
			}
		})
	}
}

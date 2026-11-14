package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestCodeModePromptDeliveryStaticAndDynamicRequests(t *testing.T) {
	for _, dynamic := range []bool{false, true} {
		t.Run(map[bool]string{false: "static", true: "dynamic"}[dynamic], func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			const legacyKey = "code_mode_catalog_v5"
			const legacyGuidance = "LEGACY_V5_GUIDANCE"
			_, err := store.UpdateState(t.Context(), session.UpdateStateRequest{SessionRef: active.SessionRef, Update: func(state map[string]any) (map[string]any, error) {
				if state == nil {
					state = map[string]any{}
				}
				state[legacyKey] = legacyGuidance
				return state, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			def := fixtureMCPDefinition("v1")
			def.Name = "hints__read"
			def.Description = "prompt hint sentinel"
			source := &jsExecSource{items: []tool.Tool{tool.NamedTool{Def: def}}}
			llm := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return h1Script(`return {ok:true};`)
				}
				return nil
			}}
			tools := []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}
			spec := agent.AgentSpec{Model: llm, Tools: tools, DeferredTools: source, Metadata: map[string]any{"system_prompt": "PROFILE_INSTRUCTIONS", codemode.MetadataSelectedTools: []string{def.Name}}}
			if dynamic {
				spec.ResolveModelRequest = func(context.Context) (agent.ModelRequestSnapshot, error) {
					return agent.ModelRequestSnapshot{Model: llm, Tools: tools, DeferredTools: source, Instructions: []model.Part{model.NewTextPart("PROFILE_INSTRUCTIONS")}, Validate: func(context.Context) (func(), error) { return func() {}, nil }}, nil
				}
			}
			runCodeMode(t, store, active, spec, nil, nil)
			state, err := store.SnapshotState(t.Context(), active.SessionRef)
			if err != nil {
				t.Fatal(err)
			}
			catalog, ok := state[codemode.MetadataCatalog].(string)
			if !ok || !strings.HasPrefix(catalog, codemode.Guidance) || state[legacyKey] != legacyGuidance {
				t.Fatal("current guidance must have its own snapshot and preserve historical state")
			}
			if len(llm.requests) != 2 {
				t.Fatalf("requests=%d", len(llm.requests))
			}
			var prefix string
			for _, req := range llm.requests {
				var text strings.Builder
				for _, p := range req.Instructions {
					if p.Text != nil {
						text.WriteString(p.Text.Text)
					}
				}
				got := text.String()
				if !strings.HasPrefix(got, "PROFILE_INSTRUCTIONS") || strings.Count(got, catalog) != 1 || !strings.Contains(got, "prompt hint sentinel") || strings.Contains(got, legacyGuidance) {
					t.Fatalf("guidance propagation=%q", got)
				}
				for _, message := range req.Messages {
					if strings.Contains(message.TextContent(), codemode.Guidance) {
						t.Fatal("SDK guidance belongs in Instructions, not conversational messages")
					}
				}
				entryCount := 0
				for _, spec := range req.Tools {
					if spec.Function != nil && spec.Function.Name == codemode.ToolName {
						entryCount++
						if spec.Function.Description != tools[0].Definition().Description {
							t.Fatal("entry summary changed during model request assembly")
						}
					}
				}
				if entryCount != 1 {
					t.Fatalf("js_exec definitions=%d", entryCount)
				}
				raw, _ := json.Marshal(req.Instructions)
				if prefix != "" && prefix != string(raw) {
					t.Fatal("instruction prefix changed within Turn")
				}
				prefix = string(raw)
			}
		})
	}
}

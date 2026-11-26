package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestLegacyMCPSearchReceiptAfterStoreReopen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		marked bool
	}{
		{"marked", true},
		{"unmarked", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			source := &changingBindingSource{}
			source.publish(tool.NamedTool{Def: fixtureMCPDefinition("v1")})
			first := &mcpScriptModel{steps: func(step int, _ *model.Request) []model.ToolCall {
				if step == 0 {
					return h1Script(`return await tools.search({name:"docs__read"});`)
				}
				return nil
			}}
			_, rt := runCodeMode(t, store, active, agent.AgentSpec{
				Model: first, DeferredTools: source,
				Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}},
			}, nil, nil)
			if err := rt.ReleaseCodeModeEnvironments(); err != nil {
				t.Fatal(err)
			}
			if len(first.requests) != 2 {
				t.Fatalf("search turn made %d model requests, want 2", len(first.requests))
			}
			storeRoot := filepath.Join(root, "sessions")
			writeLegacyMCPSearchReceipt(t, storeRoot, tc.marked)
			reopened := file.NewStore(file.Config{RootDir: storeRoot})
			events, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			if got := codeModeBindings(events); len(got) != 0 {
				t.Fatalf("legacy receipt restored a binding: %v", got)
			}

			var effects atomic.Int32
			source.publish(tool.NamedTool{Def: fixtureMCPDefinition("v2"), Invoke: func(context.Context, tool.Call) (tool.Result, error) {
				effects.Add(1)
				return tool.Result{}, nil
			}})
			resumed := &mcpScriptModel{steps: func(step int, _ *model.Request) []model.ToolCall {
				if step == 0 {
					return []model.ToolCall{{ID: "after-upgrade", Name: codemode.ToolName, Args: `{"code":"await tools.search({name:'docs__read'}); return await tools.call('docs__read', {});"}`}}
				}
				return nil
			}}
			runCodeMode(t, reopened, active, agent.AgentSpec{
				Model: resumed, DeferredTools: source,
				Tools: []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}},
			}, nil, nil)
			want := append(model.CloneMessages(first.requests[1].Messages), model.NewTextMessage(model.RoleAssistant, "done"), model.NewTextMessage(model.RoleUser, "synthetic Code Mode task"))
			if len(resumed.requests) == 0 || !reflect.DeepEqual(resumed.requests[0].Messages, want) {
				t.Fatal("reopened history differs from the original model context")
			}
			if effects.Load() != 1 {
				t.Fatalf("fresh search and call dispatched %d effects, want 1", effects.Load())
			}
			events, err = file.NewStore(file.Config{RootDir: storeRoot}).Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			var receipts []*session.Event
			for _, event := range events {
				if event.Type == session.EventTypeToolResult {
					receipts = append(receipts, event)
				}
			}
			if got := codeModeBindings(receipts)["docs__read"]; got != mcp.BindingForDefinition(fixtureMCPDefinition("v2")) {
				t.Fatalf("did not restore new binding after re-open: %+v", got)
			}
		})
	}
}

func writeLegacyMCPSearchReceipt(t *testing.T, root string, marked bool) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, "*", "*", "*", "*", "*.events.jsonl"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("finding fixture event log: %v, %v", paths, err)
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(raw, []byte{'\n'})
	count := 0
	for i, line := range lines {
		var event session.Event
		if len(line) == 0 {
			continue
		}
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type != session.EventTypeToolResult || event.Tool == nil || event.Tool.Name != mcp.SearchToolName {
			continue
		}
		if event.Tool.Result == nil || !event.Tool.Discovery || event.Tool.Outcome != session.ToolOutcomeSucceeded {
			t.Fatal("fixture did not produce a successful discovery receipt")
		}
		// v0.83.1 wrote plain model parts with its marker in result.metadata.
		// Replace only this wire record so the resumed Store must decode that shape.
		legacy := struct {
			Content  []model.Part    `json:"content"`
			Metadata map[string]bool `json:"metadata,omitempty"`
		}{Content: tool.ModelParts(event.Tool.Result.Content)}
		if marked {
			legacy.Metadata = map[string]bool{"caelis.mcp.discovery.v1": true}
		}
		row := struct {
			*session.Event
			Tool struct {
				*session.EventTool
				Result any `json:"result"`
			} `json:"tool"`
		}{Event: &event}
		event.Tool.Discovery = false
		row.Tool.EventTool, row.Tool.Result = event.Tool, legacy
		lines[i], err = json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 1 {
		t.Fatalf("found %d discovery receipts, want 1", count)
	}
	if err := os.WriteFile(paths[0], bytes.Join(lines, []byte{'\n'}), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fixtureMCPDefinition(source string) tool.Definition {
	return tool.Definition{Name: "docs__read", InputSchema: map[string]any{"type": "object"}, Metadata: map[string]any{
		mcp.MetadataKind: mcp.KindTool, mcp.MetadataPluginID: "fixture",
		mcp.MetadataServer: "docs", mcp.MetadataTool: "read",
		mcp.MetadataSourceFingerprint: source,
	}}
}

type mcpScriptModel struct {
	requests []*model.Request
	steps    func(int, *model.Request) []model.ToolCall
}

func (*mcpScriptModel) Name() string { return "mcp-script" }
func (*mcpScriptModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}
func (m *mcpScriptModel) Generate(_ context.Context, request *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.requests = append(m.requests, model.CloneRequest(request))
	index := len(m.requests) - 1
	return func(yield func(*model.StreamEvent, error) bool) {
		calls := m.steps(index, request)
		message := model.NewTextMessage(model.RoleAssistant, "done")
		reason := model.FinishReasonStop
		if len(calls) > 0 {
			message = model.MessageFromToolCalls(model.RoleAssistant, calls, "")
			reason = model.FinishReasonToolCalls
		}
		yield(model.StreamEventFromResponse(&model.Response{Message: message, Status: model.ResponseStatusCompleted, FinishReason: reason, StepComplete: true, TurnComplete: true}), nil)
	}
}
func mcpFixturePolicy() policy.Registry {
	return staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(context.Context, policy.ToolContext) (policy.Decision, error) {
		return policy.Decision{Action: policy.ActionAllow}, nil
	}}}
}

// mcpAskFixturePolicy supplies an explicit SDK per-call Ask.
func mcpAskFixturePolicy() policy.Registry {
	return staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
		if !mcp.IsDefinition(input.Tool) {
			return policy.Decision{Action: policy.ActionAllow}, nil
		}
		decision, err := mcpFixtureApproval(input, "allow_once")
		if err != nil {
			return policy.Decision{}, err
		}
		binding := mcp.BindingForDefinition(input.Tool)
		raw, err := json.Marshal(input.Call)
		if err != nil {
			return policy.Decision{}, err
		}
		digest := sha256.Sum256(raw)
		decision.Authorization = &session.ApprovalAuthorization{Schema: session.AuthorizationSchemaVersion, Scope: session.AuthorizationScope{Authority: "fixture", Workspace: input.Session.CWD, SessionID: input.Session.SessionID, SessionIncarnation: input.Session.CreatedAt.UTC().Format(time.RFC3339Nano), SourceID: binding.Identity, SourceRevision: binding.Version, PolicyRevision: "fixture-ask-v1", Tool: input.Tool.Name}, CallDigest: hex.EncodeToString(digest[:]), PerCall: true, Choices: []session.AuthorizationChoice{{OptionID: "allow_once", Persistence: session.AuthorizationOnce}}}
		return decision, nil
	}}}
}

func mcpFixtureApproval(input policy.ToolContext, allowID string) (policy.Decision, error) {
	args, err := policy.CallArgs(input.Call)
	if err != nil {
		return policy.Decision{}, err
	}
	return policy.Decision{
		Action: policy.ActionAskApproval,
		Reason: "MCP fixture requires approval",
		Approval: &session.ProtocolApproval{
			ToolCall: session.ProtocolToolCall{ID: input.Call.ID, Name: input.Tool.Name, Kind: "other", Status: "pending", RawInput: args},
			Options:  []session.ProtocolApprovalOption{{ID: allowID, Kind: "allow_once"}, {ID: "cancel", Kind: "reject_once"}},
		},
	}, nil
}

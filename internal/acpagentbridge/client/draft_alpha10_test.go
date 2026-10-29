package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpmeta"
	"github.com/caelis-labs/caelis/internal/acpdraft"
)

func TestDraftAlpha10CommandsRecoverEntriesAndKeepTextInput(t *testing.T) {
	agent := &draftTestAgent{}
	client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
	t.Cleanup(func() {
		if err := client.draft.failureCause(); err != nil {
			t.Errorf("draft failure: %v", err)
		}
	})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	for _, commands := range []string{
		`[{"type":"command","name":"help","description":"Help","input":{"type":"text","hint":"topic"},"_meta":{"n":9007199254740993}}]`,
		`[null,{},42,{"type":"future","name":"later","description":"Later","_meta":false},{"type":"command","name":"help","description":"Help","input":{"type":"text","hint":"topic"},"_meta":{"n":9007199254740993}}]`, `[]`,
	} {
		if err := agent.update(ctx, "session", json.RawMessage(`{"sessionUpdate":"available_commands_update","availableCommands":`+commands+`}`)); err != nil {
			t.Fatal(err)
		}
		update := requireDraftUpdate(t, ctx, updates).Update.(AvailableCommandsUpdate)
		if commands == `[]` {
			if update.AvailableCommands == nil || len(update.AvailableCommands) != 0 {
				t.Fatalf("empty replacement lost: %#v", update)
			}
		} else if len(update.AvailableCommands) != 1 || update.AvailableCommands[0].Input == nil || update.AvailableCommands[0].Input.Unstructured == nil || update.AvailableCommands[0].Input.Unstructured.Hint != "topic" || string(update.AvailableCommands[0].Meta["n"]) != "9007199254740993" {
			t.Fatalf("command/input metadata lost: %#v", update)
		}
	}
}

func TestDraftAlpha10RecoveredPatchCannotClearOrDisconnect(t *testing.T) {
	agent := &draftTestAgent{}
	client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	send := func(raw string) ToolCallUpdate {
		t.Helper()
		if err := agent.update(ctx, "session", json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
		return requireDraftUpdate(t, ctx, updates).Update.(ToolCallUpdate)
	}
	send(`{"sessionUpdate":"tool_call_update","toolCallId":"tool","content":[{"type":"terminal","terminalId":"term"}],"_meta":{"large":9007199254740993,"huge":1e400}}`)
	patched := send(`{"sessionUpdate":"tool_call_update","toolCallId":"tool","content":false,"locations":false,"_meta":false,"title":false}`)
	for _, key := range []string{"content", "locations", "_meta", "title"} {
		if _, present := patched.RawPatch[key]; present {
			t.Fatalf("discarded %s remained a patch: %#v", key, patched)
		}
	}
	if err := client.draft.failureCause(); err != nil {
		t.Fatal(err)
	}
	retained := send(`{"sessionUpdate":"tool_call_update","toolCallId":"tool","_meta":{"added":true}}`)
	if retained.Meta["large"] != json.Number("9007199254740993") || retained.Meta["huge"] != json.Number("1e400") {
		t.Fatalf("retained metadata lost: %#v", retained.Meta)
	}
	cleared := send(`{"sessionUpdate":"tool_call_update","toolCallId":"tool","content":[],"_meta":null}`)
	if _, present := cleared.RawPatch["content"]; !present || cleared.Meta != nil {
		t.Fatalf("valid explicit clear lost: %#v", cleared)
	}
}

func TestDraftAlpha10TerminalExitRetainsInt64(t *testing.T) {
	for _, code := range []string{"-9223372036854775808", "9007199254740993", "9223372036854775807"} {
		for _, beforeReference := range []bool{false, true} {
			t.Run(fmt.Sprint(code, "/", beforeReference), func(t *testing.T) {
				agent := &draftTestAgent{}
				client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
				if _, err := client.Initialize(ctx); err != nil {
					t.Fatal(err)
				}
				tool := json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"tool","content":[{"type":"terminal","terminalId":"term"}]}`)
				exit := json.RawMessage(`{"sessionUpdate":"terminal_update","terminalId":"term","exitStatus":{"exitCode":` + code + `}}`)
				if beforeReference {
					if err := agent.update(ctx, "session", exit); err != nil {
						t.Fatal(err)
					}
					requireDraftUpdate(t, ctx, updates)
				}
				if err := agent.update(ctx, "session", tool); err != nil {
					t.Fatal(err)
				}
				requireDraftUpdate(t, ctx, updates)
				if !beforeReference {
					if err := agent.update(ctx, "session", exit); err != nil {
						t.Fatal(err)
					}
				}
				update := requireDraftUpdate(t, ctx, updates).Update.(ToolCallUpdate)
				status, ok := acpmeta.ReadTerminalExit(update.Meta)
				if !ok || status.ExitCode == nil || fmt.Sprint(*status.ExitCode) != code {
					t.Fatalf("exit %s became %+v", code, status)
				}
			})
		}
	}
}

func TestDraftAlpha10ClientRejectsInvalidRequestIDsBeforeDispatch(t *testing.T) {
	for _, method := range []string{"session/request_permission", "_vendor/work"} {
		for _, id := range []string{"null", "1.5", "9223372036854775808", `"valid"`, "9223372036854775807"} {
			t.Run(method+"/"+id, func(t *testing.T) {
				left, right := net.Pipe()
				defer right.Close()
				if err := right.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
					t.Fatal(err)
				}
				client, err := NewStreamClient(left, left, Config{ACPDraftSchema: acpdraft.SchemaTag})
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close(context.Background())
				if _, err := fmt.Fprintf(right, `{"jsonrpc":"2.0","id":%s,"method":%q,"params":{}}`+"\n", id, method); err != nil {
					t.Fatal(err)
				}
				raw, err := bufio.NewReader(right).ReadBytes('\n')
				if err != nil {
					t.Fatal(err)
				}
				var response struct {
					Error *acp.RequestError `json:"error"`
				}
				if err := json.Unmarshal(raw, &response); err != nil {
					t.Fatal(err)
				}
				want := -32600
				if id == `"valid"` || id == "9223372036854775807" {
					want = -32601
					if method == "session/request_permission" {
						want = -32602
					}
				}
				if response.Error == nil || response.Error.Code != want {
					t.Fatalf("ID %s: %s, want %d", id, raw, want)
				}
			})
		}
	}
}

func TestDraftAlpha10ReplacementConfigAndPlanRecoverInvalidEntries(t *testing.T) {
	agent := &draftTestAgent{}
	client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"sessionUpdate":"config_option_update","configOptions":[null,42,{}, {"type":"select","configId":"mode","name":"Mode","currentValue":"ask","options":[{"value":"ask","name":"Ask"}]}]}`,
		`{"sessionUpdate":"plan_update","plan":{"type":"items","planId":"plan","entries":[null,{},42,{"content":"Step","priority":"medium","status":"pending"}]}}`,
	} {
		if err := agent.update(ctx, "session", json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
		switch update := requireDraftUpdate(t, ctx, updates).Update.(type) {
		case ConfigOptionUpdate:
			if len(update.ConfigOptions) != 1 {
				t.Fatalf("config: %#v", update)
			}
		case PlanUpdate:
			if len(update.Entries) != 1 || update.Entries[0].Content != "Step" {
				t.Fatalf("plan: %#v", update)
			}
		default:
			t.Fatalf("unexpected update %T", update)
		}
	}
}

func TestDraftAlpha10ExplicitEmptyConfigurationRemainsReplacement(t *testing.T) {
	agent := &draftTestAgent{}
	client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := agent.update(ctx, "session", json.RawMessage(`{"sessionUpdate":"config_option_update","configOptions":[]}`)); err != nil {
		t.Fatal(err)
	}
	update := requireDraftUpdate(t, ctx, updates).Update.(ConfigOptionUpdate)
	if update.ConfigOptions == nil || len(update.ConfigOptions) != 0 {
		t.Fatalf("empty configuration replacement lost: %#v", update)
	}
}

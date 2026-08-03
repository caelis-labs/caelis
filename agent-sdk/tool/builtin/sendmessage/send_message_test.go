package sendmessage

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestSendMessageSchemaKeepsOnlyRoutingEssentials(t *testing.T) {
	t.Parallel()

	def := New().Definition()
	properties, _ := def.InputSchema["properties"].(map[string]any)
	if len(properties) != 2 || properties["to"] == nil || properties["message"] == nil {
		t.Fatalf("properties = %#v, want only to and message", properties)
	}
	required, _ := def.InputSchema["required"].([]string)
	if !slices.Equal(required, []string{"to", "message"}) {
		t.Fatalf("required = %#v, want [to message]", required)
	}
	to, _ := properties["to"].(map[string]any)
	if description, _ := to["description"].(string); !strings.Contains(description, "parent") || !strings.Contains(description, "Session-scoped subagent handle") {
		t.Fatalf("to description = %q, want parent and subagent handle guidance", description)
	}
}

func TestSendMessageDescriptionDefinesChannelAndResultSemantics(t *testing.T) {
	t.Parallel()

	description := New().Definition().Description
	for _, want := range []string{
		"incremental update or question",
		"terminal answer in the final response",
		"Task read or wait",
		"do not send the same terminal answer through both channels",
		"routing boundary owns delivery",
		"not that the target finished",
		"state is delivery or target observation",
		"started_turn",
		"turn_id only groups",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("description missing %q: %q", want, description)
		}
	}
}

func TestSendMessageCallValidatesBeforeRuntimeRouting(t *testing.T) {
	t.Parallel()

	for name, args := range map[string]map[string]any{
		"missing target": {"message": "hello"},
		"blank message":  {"to": "parent", "message": "   "},
		"unknown arg":    {"to": "parent", "message": "hello", "wait": true},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(args)
			if err != nil {
				t.Fatal(err)
			}
			_, err = New().Call(context.Background(), tool.Call{Name: ToolName, Input: raw})
			if err == nil || strings.Contains(err.Error(), "runtime wrapper") {
				t.Fatalf("Call() error = %v, want argument validation failure", err)
			}
		})
	}
}

func TestSendMessageCallRequiresRuntimeRouting(t *testing.T) {
	t.Parallel()

	raw, _ := json.Marshal(map[string]any{"to": "parent", "message": "hello"})
	_, err := New().Call(context.Background(), tool.Call{Name: ToolName, Input: raw})
	if err == nil || !strings.Contains(err.Error(), "runtime wrapper") {
		t.Fatalf("Call() error = %v, want runtime wrapper error", err)
	}
}

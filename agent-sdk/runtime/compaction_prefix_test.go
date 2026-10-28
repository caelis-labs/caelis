package runtime

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestCompactionPrefixSafe(t *testing.T) {
	calls := func(ids ...string) model.Message {
		parts := make([]model.Part, 0, len(ids))
		for _, id := range ids {
			parts = append(parts, model.NewToolUsePart(id, "lookup", nil))
		}
		return model.NewMessage(model.RoleAssistant, parts...)
	}
	results := func(ids ...string) model.Message {
		parts := make([]model.Part, 0, len(ids))
		for _, id := range ids {
			parts = append(parts, model.NewToolResultJSONPart(id, "lookup", nil, false))
		}
		return model.NewMessage(model.RoleTool, parts...)
	}
	withRole := func(message model.Message, role model.Role) model.Message {
		message.Role = role
		return message
	}
	user := model.NewTextMessage(model.RoleUser, "continue")
	assistant := model.NewTextMessage(model.RoleAssistant, "done")
	mixed := model.NewMessage(model.RoleAssistant, append(calls("a").Parts, results("a").Parts...)...)
	resultThenCall := model.NewMessage(model.RoleAssistant, append(results("a").Parts, calls("b").Parts...)...)
	callWithText := model.NewMessage(model.RoleAssistant, append([]model.Part{model.NewTextPart("looking up")}, calls("a").Parts...)...)

	for _, tt := range []struct {
		name     string
		messages []model.Message
		want     bool
	}{
		{"empty", nil, true},
		{"ordinary conversation", []model.Message{user, assistant}, true},
		{"single closed batch", []model.Message{user, calls("a"), results("a")}, true},
		{"conversation after batch", []model.Message{calls("a"), results("a"), assistant, user}, true},
		{"assistant text with calls", []model.Message{callWithText, results("a")}, true},
		{"parallel grouped results", []model.Message{calls("a", "b"), results("a", "b")}, true},
		{"parallel out of order results", []model.Message{calls("a", "b"), results("b"), results("a")}, true},
		{"parallel grouped out of order", []model.Message{calls("a", "b"), results("b", "a")}, true},
		{"separate batches", []model.Message{calls("a"), results("a"), calls("b"), results("b")}, true},
		{"reused ID after closed batch", []model.Message{calls("a"), results("a"), calls("a"), results("a")}, true},
		{"reused parallel IDs", []model.Message{calls("a", "b"), results("b", "a"), calls("b", "a"), results("a", "b")}, true},
		{"IDs are not trimmed", []model.Message{calls(" a", "a"), results("a", " a")}, true},
		{"trailing call", []model.Message{calls("a")}, false},
		{"trailing parallel calls", []model.Message{calls("a", "b")}, false},
		{"partially completed parallel calls", []model.Message{calls("a", "b"), results("a")}, false},
		{"orphan result", []model.Message{results("a")}, false},
		{"orphan after closed batch", []model.Message{calls("a"), results("a"), results("b")}, false},
		{"unmatched result", []model.Message{calls("a"), results("b")}, false},
		{"empty call ID", []model.Message{calls(""), results("")}, false},
		{"empty result ID", []model.Message{calls("a"), results("")}, false},
		{"duplicate call IDs", []model.Message{calls("a", "a"), results("a")}, false},
		{"duplicate result in message", []model.Message{calls("a", "b"), results("a", "a", "b")}, false},
		{"duplicate result across messages", []model.Message{calls("a", "b"), results("a"), results("a"), results("b")}, false},
		{"duplicate result after batch", []model.Message{calls("a"), results("a"), results("a")}, false},
		{"new calls before results", []model.Message{calls("a"), calls("b"), results("a", "b")}, false},
		{"new calls during parallel batch", []model.Message{calls("a", "b"), results("a"), calls("c"), results("b", "c")}, false},
		{"reuse before batch closed", []model.Message{calls("a", "b"), results("a"), calls("a"), results("b", "a")}, false},
		{"user interrupts batch", []model.Message{calls("a"), user, results("a")}, false},
		{"assistant interrupts batch", []model.Message{calls("a"), assistant, results("a")}, false},
		{"empty message interrupts batch", []model.Message{calls("a"), model.NewMessage(model.RoleTool), results("a")}, false},
		{"text tool message interrupts batch", []model.Message{calls("a"), model.NewTextMessage(model.RoleTool, "result"), results("a")}, false},
		{"user tool call", []model.Message{withRole(calls("a"), model.RoleUser), results("a")}, false},
		{"tool role tool call", []model.Message{withRole(calls("a"), model.RoleTool), results("a")}, false},
		{"system tool call", []model.Message{withRole(calls("a"), model.RoleSystem), results("a")}, false},
		{"user tool result", []model.Message{calls("a"), withRole(results("a"), model.RoleUser)}, false},
		{"assistant tool result", []model.Message{calls("a"), withRole(results("a"), model.RoleAssistant)}, false},
		{"system tool result", []model.Message{calls("a"), withRole(results("a"), model.RoleSystem)}, false},
		{"calls and results in one message", []model.Message{mixed}, false},
		{"result and next batch in one message", []model.Message{calls("a"), resultThenCall, results("b")}, false},
		{"mismatched whitespace ID", []model.Message{calls(" a"), results("a")}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := model.CloneMessages(tt.messages)
			if got := compactionPrefixSafe(tt.messages); got != tt.want {
				t.Errorf("compactionPrefixSafe() = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(tt.messages, before) {
				t.Fatal("compactionPrefixSafe mutated the messages")
			}
		})
	}
}

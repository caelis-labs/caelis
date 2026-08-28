package acpingress

import (
	"encoding/json"
	"reflect"
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

func TestPermissionRequestProjectsExternalPayloadWithoutAliasingMetadata(t *testing.T) {
	t.Parallel()

	title := "Write file"
	kind := acpsdk.ToolKindEdit
	status := acpsdk.ToolCallStatusPending
	oldText := "old"
	line := uint32(7)
	in := client.RequestPermissionRequest{
		SessionId: "remote-session",
		ToolCall: acpsdk.ToolCallUpdate{
			ToolCallId: "call-1",
			Title:      &title,
			Kind:       &kind,
			Status:     &status,
			RawInput:   map[string]any{"path": "a.txt"},
			RawOutput:  map[string]any{"preview": "new"},
			Content:    []acpsdk.ToolCallContent{acpsdk.ToolDiffContent("a.txt", "new", oldText)},
			Locations:  []acpsdk.ToolCallLocation{{Path: "a.txt", Line: &line}},
			Meta:       map[string]json.RawMessage{"vendor": json.RawMessage(`{"trace":"tool"}`)},
		},
		Options: []acpsdk.PermissionOption{{
			OptionId: "allow_once", Name: "Allow once", Kind: acpsdk.PermissionOptionKindAllowOnce,
		}},
		Meta: map[string]json.RawMessage{"vendor": json.RawMessage(`{"trace":"request"}`)},
	}

	got, err := PermissionRequest(in)
	if err != nil {
		t.Fatalf("PermissionRequest() error = %v", err)
	}
	want := eventstream.RequestPermissionRequest{
		SessionID: "remote-session",
		ToolCall: eventstream.ToolCallUpdate{
			ToolCallID: "call-1",
			Title:      &title,
			Kind:       stringPtr(string(kind)),
			Status:     stringPtr(string(status)),
			RawInput:   map[string]any{"path": "a.txt"},
			RawOutput:  map[string]any{"preview": "new"},
			Content: []eventstream.ToolCallContent{{
				Type: "diff", Path: "a.txt", OldText: &oldText, NewText: "new",
			}},
			Locations: []eventstream.ToolCallLocation{{Path: "a.txt", Line: intPtr(int(line))}},
			Meta:      map[string]any{"vendor": map[string]any{"trace": "tool"}},
		},
		Options: append([]acpsdk.PermissionOption(nil), in.Options...),
		Meta:    map[string]any{"vendor": map[string]any{"trace": "request"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PermissionRequest() = %#v, want %#v", got, want)
	}

	got.ToolCall.Meta["vendor"].(map[string]any)["trace"] = "changed"
	got.Meta["vendor"].(map[string]any)["trace"] = "changed"
	*got.ToolCall.Locations[0].Line = 99
	if string(in.ToolCall.Meta["vendor"]) != `{"trace":"tool"}` ||
		string(in.Meta["vendor"]) != `{"trace":"request"}` ||
		*in.ToolCall.Locations[0].Line != 7 {
		t.Fatalf("PermissionRequest() aliased external input: %#v", in)
	}
}

func stringPtr(value string) *string { return &value }

func intPtr(value int) *int { return &value }

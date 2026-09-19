package client

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDecodeToolNameV1(t *testing.T) {
	t.Parallel()
	for _, updateType := range []string{UpdateToolCall, UpdateToolCallState} {
		for _, field := range []string{``, `,"name":null`, `,"name":"ReadFile"`, `,"name":""`} {
			t.Run(updateType+field, func(t *testing.T) {
				raw := json.RawMessage(`{"sessionUpdate":"` + updateType + `","toolCallId":"call-1","title":"Read a file"` + field + `}`)
				decoded, err := decodeUpdate(raw)
				if err != nil {
					t.Fatal(err)
				}
				var want struct {
					Name *string `json:"name"`
				}
				if err := json.Unmarshal(raw, &want); err != nil {
					t.Fatal(err)
				}
				var got *string
				switch update := NormalizeInboundUpdate(decoded).(type) {
				case ToolCall:
					got = update.Name
				case ToolCallUpdate:
					got = update.Name
				default:
					t.Fatalf("update = %T", update)
				}
				if !reflect.DeepEqual(got, want.Name) {
					t.Fatalf("name = %#v, want %#v", got, want.Name)
				}
			})
		}
	}
}

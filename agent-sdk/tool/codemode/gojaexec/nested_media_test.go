package gojaexec

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

func TestNestedAuthenticatedMediaKeepsEncodingBudgets(t *testing.T) {
	h := testHost{call: func(context.Context, string, json.RawMessage) (codemode.Reply, error) {
		return codemode.Reply{Parts: []codemode.Part{{Kind: "json", JSON: json.RawMessage(`{}`)}, {Kind: "media", Handle: "owned-A"}, {Kind: "media", Handle: "owned-B"}}}, nil
	}}
	for _, tc := range []struct{ code, want string }{
		{`const value=r.json();value.selected=r.media;return value;`, "returned media budget exceeded"},
		{`const value=r.json();value.self=value;return value;`, "cyclic JSON"},
		{`let v=r.media[0];for(let i=0;i<130;i++)v={nested:v};const value=r.json();value.selected=v;return value;`, "JSON depth exceeded"},
	} {
		_, err := (&Executor{}).Execute(t.Context(), fixed(`const r=await tools.call("Target",{});`+tc.code), codemode.Limits{Calls: 1}, h)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("got %v; want %s", err, tc.want)
		}
	}
}

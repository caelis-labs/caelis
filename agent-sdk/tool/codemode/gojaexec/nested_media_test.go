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
		{`r.parts[0].json.selected=[r.parts[1],r.parts[2]];r.parts=[r.parts[0]];return r;`, "returned media budget exceeded"},
		{`r.parts[0].json.self=r.parts[0];return r;`, "cyclic JSON"},
		{`let v=r.parts[1];for(let i=0;i<130;i++)v={nested:v};r.parts[0].json.selected=v;return r;`, "JSON depth exceeded"},
	} {
		_, err := (&Executor{}).Execute(t.Context(), fixed(`const r=await tools.call("Target",{});`+tc.code), codemode.Limits{Calls: 1}, h)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("got %v; want %s", err, tc.want)
		}
	}
}

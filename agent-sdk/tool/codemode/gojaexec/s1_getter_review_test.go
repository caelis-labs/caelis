package gojaexec

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

func TestReviewS1AuthenticatedReplyGetterEvaluatedOnce(t *testing.T) {
	h := testHost{call: func(context.Context, string, json.RawMessage) (codemode.Reply, error) {
		return codemode.Reply{Outcome: codemode.OutcomeSucceeded, Parts: []codemode.Part{{Kind: "media", Handle: "owned-A", MimeType: "image/png"}}}, nil
	}}
	for _, tc := range []struct{ name, code string }{
		{"reply_parts_getter", `const r=await tools.call("Target",{});const real=r.parts;let n=0;Object.defineProperty(r,"parts",{enumerable:true,get(){return ++n===1?[{kind:"json",json:{reads:n}}]:real;}});return r;`},
		{"reply_extra_property", `const r=await tools.call("Target",{});let n=0;r.note="ordinary data";Object.defineProperty(r,"parts",{enumerable:true,get(){return [{kind:"json",json:{reads:++n}}];}});return r;`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := (&Executor{}).Execute(t.Context(), fixed(tc.code), codemode.Limits{}, h)
			if err != nil {
				t.Fatal(err)
			}
			var value struct {
				Parts []struct {
					JSON struct {
						Reads int `json:"reads"`
					} `json:"json"`
				} `json:"parts"`
			}
			_ = json.Unmarshal(output.Value, &value)
			if len(output.Parts) != 0 || len(value.Parts) != 1 || value.Parts[0].JSON.Reads != 1 {
				t.Fatalf("getter preflight changed returned value/media: value=%s selected=%+v", output.Value, output.Parts)
			}
		})
	}
}

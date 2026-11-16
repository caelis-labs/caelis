package gojaexec

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

// Selection uses the actual single budgeted element read, including accessors.
func TestReplyPartElementAccessorEvaluatedOnce(t *testing.T) {
	h := testHost{call: func(context.Context, string, json.RawMessage) (codemode.Reply, error) {
		return codemode.Reply{Outcome: codemode.OutcomeSucceeded, Parts: []codemode.Part{{Kind: "media", Handle: "owned-A"}}}, nil
	}}
	code := `const r=await tools.call("Target",{});const real=r.media[0];const selected={parts:[real]};let n=0;Object.defineProperty(selected.parts,"0",{enumerable:true,get(){return ++n===1?{kind:"json",json:{reads:n}}:real}});return selected;`
	output, err := (&Executor{}).Execute(t.Context(), fixed(code), codemode.Limits{}, h)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Parts []struct {
			JSON struct {
				Reads int `json:"reads"`
			} `json:"json"`
		} `json:"parts"`
	}
	if json.Unmarshal(output.Value, &got) != nil || len(got.Parts) != 1 || got.Parts[0].JSON.Reads != 1 || len(output.Parts) != 0 {
		t.Fatalf("element getter changed value/media: %+v", output)
	}
}

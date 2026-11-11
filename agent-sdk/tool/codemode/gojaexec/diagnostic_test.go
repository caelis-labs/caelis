package gojaexec

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"strings"
	"testing"
)

func TestUserSourceAndJSONFieldDiagnostics(t *testing.T) {
	for _, tc := range []struct{ name, code, want string }{
		{"file_throw", "const x=1;\nthrow new Error('user failure');", "/task/probe.js:2"},
		{"undefined", `return {nested:{optional:undefined}};`, "$.nested.optional"},
		{"array_undefined", `return {items:[undefined]};`, "$.items[0]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := fixed(tc.code)
			src.Path = "/task/probe.js"
			_, err := (&Executor{}).Execute(t.Context(), src, codemode.Limits{}, testHost{})
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "<eval>") {
				t.Fatalf("%v", err)
			}
		})
	}
	out, err := (&Executor{}).Execute(t.Context(), fixed(`const optional=undefined;return {optional:optional===undefined?null:optional};`), codemode.Limits{}, testHost{})
	if err != nil || string(out.Value) != `{"optional":null}` {
		t.Fatalf("%s %v", out.Value, err)
	}
	out, err = (&Executor{}).Execute(t.Context(), fixed(`const x=1;`), codemode.Limits{}, testHost{})
	if err != nil || string(out.Value) != "null" {
		t.Fatalf("%s %v", out.Value, err)
	}
}

func TestCaughtValidationDetailAndTransportRedaction(t *testing.T) {
	h := testHost{call: func(context.Context, string, json.RawMessage) (codemode.Reply, error) {
		return codemode.Reply{}, tool.NewError(tool.ErrorCodeInvalidInput, `required $.path: missing path`)
	}}
	out, err := (&Executor{}).Execute(t.Context(), fixed(`try {await tools.call("Read",{});} catch(e) {return e.message;}`), codemode.Limits{}, h)
	for _, want := range []string{"Read", "invalid_input", "$.path", "missing path"} {
		if err != nil || !strings.Contains(string(out.Value), want) {
			t.Fatalf("%s %v", out.Value, err)
		}
	}
	secret := "https://user:password@host/?token=secret raw network body"
	h = testHost{call: func(context.Context, string, json.RawMessage) (codemode.Reply, error) {
		return codemode.Reply{}, errors.New(secret)
	}}
	out, err = (&Executor{}).Execute(t.Context(), fixed(`try {await tools.call("remote__read",{});}catch(e){return e.message;}`), codemode.Limits{}, h)
	if err != nil || strings.Contains(string(out.Value), "password") || strings.Contains(string(out.Value), "token") || strings.Contains(string(out.Value), "network body") {
		t.Fatalf("%s %v", out.Value, err)
	}
}

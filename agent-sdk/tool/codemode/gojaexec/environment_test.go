package gojaexec

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

func TestContinuousEnvironmentBindingsAndBody(t *testing.T) {
	e := (&Executor{}).NewEnvironment()
	defer e.Close()
	h := testHost{call: func(context.Context, string, json.RawMessage) (codemode.Reply, error) {
		return codemode.Reply{CallID: "original", Value: json.RawMessage(`{"content":"kept","n":9223372036854775807}`), Parts: []codemode.Part{{Kind: "text", Text: "first"}, {Kind: "json", JSON: json.RawMessage(`{"content":"kept","n":9223372036854775807}`)}, {Kind: "media", Handle: "image-original"}}}, nil
	}}
	for _, tc := range []struct {
		code, want string
		media      int
	}{
		{`let response=await tools.call("Read",{});const file=response.json();var count=1;let v=1;let {content}=file;return {value:content,repeat:response.json()===file,text:response.text()};`, `"repeat":true`, 0},
		{`count+=1;v=2;if(v!==2)throw Error("assignment did not update v");content+="-next";return {count,content,file:file.content,state:runtime.state()};`, `"content":"kept-next"`, 0},
		{`var count=3;return count;`, `3`, 0},
		{`return response.media[0];`, `image-original`, 1},
		{`return {...response.media[0]};`, `image-original`, 0},
		{`delete file.content;return {selected:file};`, `"selected"`, 0},
		{`return response;`, ``, 0},
	} {
		out, err := e.Execute(t.Context(), fixed(tc.code), codemode.Limits{}, h)
		if err != nil {
			t.Fatalf("%s: %v", tc.code, err)
		}
		if !strings.Contains(string(out.Value), tc.want) || len(out.Parts) != tc.media {
			t.Fatalf("%s: %+v", tc.code, out)
		}
		if tc.code == `return response;` && out.CallID != "original" {
			t.Fatalf("lost complete Body: %+v", out)
		}
	}
	for _, code := range []string{`let response=1;return response;`, `const file=1;return file;`, `file={};return file;`} {
		if _, err := e.Execute(t.Context(), fixed(code), codemode.Limits{}, h); err == nil {
			t.Fatalf("accepted redeclaration/constant assignment: %s", code)
		}
	}
	out, err := e.Execute(t.Context(), fixed(`return count;`), codemode.Limits{}, h)
	if err != nil || string(out.Value) != "3" {
		t.Fatalf("ordinary JS error lost environment: %+v %v", out, err)
	}
}

func TestBodyReadsAllTextAndDoesNotGuessFirstJSON(t *testing.T) {
	h := testHost{call: func(context.Context, string, json.RawMessage) (codemode.Reply, error) {
		return codemode.Reply{Parts: []codemode.Part{{Kind: "text", Text: `{"a":1}`}, {Kind: "text", Text: `{"b":2}`}}}, nil
	}}
	out, err := (&Executor{}).Execute(t.Context(), fixed(`const r=await tools.call("Many",{});let rejected=false;try{r.json()}catch(e){rejected=true}return {text:r.text(),repeat:r.text()===r.text(),rejected};`), codemode.Limits{}, h)
	if err != nil || !strings.Contains(string(out.Value), `"rejected":true`) || !strings.Contains(string(out.Value), `\u000a`) {
		t.Fatalf("multi-content rule: %+v %v", out, err)
	}
}

func TestContinuousEnvironmentClosuresAndScope(t *testing.T) {
	e := (&Executor{}).NewEnvironment()
	defer e.Close()
	for _, code := range []string{`let n=1, advance=()=>++n;function value(){return n;}class Box {get(){return n;}}return advance();`, `n=10;return {next:advance(),value:value(),box:new Box().get()};`} {
		out, err := e.Execute(t.Context(), fixed(code), codemode.Limits{}, testHost{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(code, "n=") && string(out.Value) != `{"next":11,"value":11,"box":11}` {
			t.Fatalf("bindings diverged from closures: %s", out.Value)
		}
	}
	other := (&Executor{}).NewEnvironment()
	defer other.Close()
	out, err := other.Execute(t.Context(), fixed(`return typeof n;`), codemode.Limits{}, testHost{})
	if err != nil || string(out.Value) != `"undefined"` {
		t.Fatalf("cross-environment leak: %+v %v", out, err)
	}
}

func TestContinuousEnvironmentFunctionScopedVars(t *testing.T) {
	e := (&Executor{}).NewEnvironment()
	defer e.Close()
	for _, code := range []string{
		`if(true){var block=1;var {value:destructured}={value:2};}for(var i=0;i<2;i++){var last=i;}for(var key in {a:1}){}for(var {n} of [{n:3}]){}return last;`,
		`return {block,destructured,i,last,key,n};`,
	} {
		out, err := e.Execute(t.Context(), fixed(code), codemode.Limits{}, testHost{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(code, "return") && string(out.Value) != `{"block":1,"destructured":2,"i":2,"last":1,"key":"a","n":3}` {
			t.Fatal(string(out.Value))
		}
	}
}

func TestContinuousEnvironmentIntrinsicBindingsAndFunctionAssignments(t *testing.T) {
	e := (&Executor{}).NewEnvironment()
	defer e.Close()
	for _, code := range []string{
		`function current(){return current;}var original=current;current=42;let JSON={},Object={},globalThis={},SyntaxError=1;return original();`,
		`let subsequent=2;return {subsequent,state:runtime.state()};`,
	} {
		out, err := e.Execute(t.Context(), fixed(code), codemode.Limits{}, testHost{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(code, "function") && string(out.Value) != "42" {
			t.Fatalf("function declaration captured its old self: %s", out.Value)
		}
	}
}

func TestContinuousEnvironmentDoWhileDeclarations(t *testing.T) {
	code := `const seen=new Set();let after;let pages=0;do{const s={next:pages<2?"more":null};after=s.next;if(++pages>37)throw Error("loop");}while(after);return pages;`
	script, err := prepareSource("inline.js", code)
	if err != nil {
		t.Fatal(err)
	}
	e := (&Executor{}).NewEnvironment()
	defer e.Close()
	out, err := e.Execute(t.Context(), fixed(code), codemode.Limits{}, testHost{})
	if err != nil {
		t.Fatalf("%v\n%s", err, script.code)
	}
	if string(out.Value) != "3" {
		t.Fatal(string(out.Value))
	}
}

func TestContinuousEnvironmentRecordsStartupOnce(t *testing.T) {
	e := (&Executor{}).NewEnvironment().(*environment)
	defer e.Close()
	_, err := e.Execute(t.Context(), fixed(`return 1;`), codemode.Limits{}, testHost{})
	if err != nil {
		t.Fatal(err)
	}
	e.child.mu.Lock()
	first := e.child.metrics.Startup
	e.child.mu.Unlock()
	_, err = e.Execute(t.Context(), fixed(`return 2;`), codemode.Limits{}, testHost{})
	if err != nil {
		t.Fatal(err)
	}
	e.child.mu.Lock()
	second := e.child.metrics.Startup
	e.child.mu.Unlock()
	if first <= 0 || second != first {
		t.Fatalf("process startup changed across executions: %v -> %v", first, second)
	}
}

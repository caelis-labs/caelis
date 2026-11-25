package gojaexec

import (
	"fmt"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

func TestContinuousEnvironmentRedeclaresEveryBindingKind(t *testing.T) {
	run := newBindingFixture(t)
	for _, first := range []string{"let", "const", "var"} {
		for _, next := range []string{"let", "const", "var"} {
			name := first + "_" + next
			run(fmt.Sprintf("%s %s=1;const capture_%s=()=>%s;return %s;", first, name, name, name, name), "1", "")
			run(fmt.Sprintf("%s %s=2;return [%s,capture_%s()];", next, name, name, name), "[2,1]", "")
			run("return "+name+";", "2", "")
		}
	}
	run(`function item(){return 1;}const original=()=>item();return item();`, "1", "")
	run(`class item {static value=2;}return [item.value,original()];`, "[2,1]", "")
	run(`return item.value;`, "2", "")
	run(`return ready();async function ready(){return 3;}`, "3", "")
	run(`return await ready();`, "3", "")
}

func TestContinuousEnvironmentPublishesOnlyInitializedBindings(t *testing.T) {
	run := newBindingFixture(t)
	run(`const x=1;let y=2;var z=3;return [x,y,z];`, "[1,2,3]", "")
	run(`const {value:x}=await Promise.resolve({value:4});return x;`, "4", "")
	run(`let {y}=await Promise.reject(Error("initializer failed"));`, "", "initializer failed")
	run(`var z=(()=>{throw Error("initializer failed")})();`, "", "initializer failed")
	run(`return [x,y,z];`, "[4,2,3]", "")
	run(`const x=5,y=(()=>{throw Error("initializer failed")})();`, "", "initializer failed")
	run(`try {var x=6,z=(()=>{throw Error("caught")})();} catch(e) {} return [x,typeof z];`, `[6,"undefined"]`, "")
	run(`return [x,y,z];`, "[6,2,3]", "")
	run(`return 7;const x=8;`, "7", "")
	run(`return x;`, "6", "")
	run(`for (var z of [undefined]) continue; return typeof z;`, `"undefined"`, "")
	run(`return typeof z;`, `"undefined"`, "")
	run(`for(var x=(8), i=0; i<1; ++i) {x++;}return x;`, "9", "")
	run(`return [x,i];`, "[9,1]", "")
	run(`z=10;return z;`, "10", "")
	run(`for(var z of []) {} return typeof z;`, `"undefined"`, "")
	run(`return z;`, "10", "")
	run(`var z=((undefined));throw Error("after successful initialization");`, "", "after successful initialization")
	run(`return typeof z;`, `"undefined"`, "")
}

func TestContinuousEnvironmentBindingScopeAndReservedNames(t *testing.T) {
	run := newBindingFixture(t)
	run(`const count=1;const getCount=()=>count;let v=1;const getV=()=>v;return count;`, "1", "")
	run(`count+=2;v=4;return [count,getCount(),v,getV()];`, "[3,1,4,4]", "")
	run(`const count=5;count=6;`, "", "TypeError")
	run(`let count=1;const count=2;`, "", "SyntaxError")
	run(`return count;const count=7;`, "", "ReferenceError")
	run(`{let count=8;const hidden=1;} return [count,typeof hidden,arguments.length];`, `[5,"undefined",0]`, "")
	run(`const tools={};`, "", "SyntaxError")
	run(`var runtime={};`, "", "SyntaxError")
	run(`return [typeof tools.call,typeof runtime.state];`, `["function","function"]`, "")
}

func newBindingFixture(t *testing.T) func(string, string, string) {
	t.Helper()
	env := (&Executor{}).NewEnvironment()
	t.Cleanup(func() { _ = env.Close() })
	return func(code, want, wantError string) {
		t.Helper()
		out, err := env.Execute(t.Context(), fixed(code), codemode.Limits{}, testHost{})
		if wantError != "" {
			if err == nil || !strings.Contains(err.Error(), wantError) {
				t.Fatalf("%s: got %v, want error containing %q", code, err, wantError)
			}
		} else if err != nil || string(out.Value) != want {
			t.Fatalf("%s: got %s (%v), want %s", code, out.Value, err, want)
		}
	}
}

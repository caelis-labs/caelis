package sandbox

import (
	"encoding/json"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestEnvironmentConfigJSONNullCollectionsAndStrictEntries(t *testing.T) {
	for _, data := range []string{`{}`, `{"inherit":null,"set":null,"unset":null}`, `{"set":{},"unset":[]}`} {
		var cfg EnvironmentConfig
		if err := json.Unmarshal([]byte(data), &cfg); err != nil {
			t.Fatalf("%s: %v", data, err)
		}
	}
	for _, data := range []string{`{"set":{"A":null}}`, `{"set":{"A":2}}`, `{"unset":[null]}`, `{"unknown":true}`} {
		var cfg EnvironmentConfig
		if err := json.Unmarshal([]byte(data), &cfg); err == nil {
			t.Fatalf("%s unexpectedly accepted", data)
		}
	}
	var cfg EnvironmentConfig
	if err := json.Unmarshal([]byte(`{"set":{"A":""},"unset":["B"]}`), &cfg); err != nil || cfg.Set["A"] != "" || !reflect.DeepEqual(cfg.Unset, []string{"B"}) {
		t.Fatalf("valid config: %+v %v", cfg, err)
	}
}

func TestExecutionEnvironmentPrecedenceAndPresence(t *testing.T) {
	inherit := false
	for _, tc := range []struct {
		name  string
		cfg   *ExecutionConfig
		base  []string
		unset []string
		set   map[string]string
		want  []string
	}{
		{name: "missing config inherits", base: []string{"ONE=1", "EMPTY=", "GONE=3"}, want: []string{"EMPTY=", "GONE=3", "ONE=1"}},
		{name: "empty config inherits", cfg: &ExecutionConfig{}, base: []string{"ONE=1"}, want: []string{"ONE=1"}},
		{name: "explicit empty base", base: []string{}, want: []string{}},
		{name: "empty inherited values survive", cfg: &ExecutionConfig{Environment: EnvironmentConfig{Set: map[string]string{"ZERO": ""}}}, base: []string{"EMPTY=", "ONE=1"}, want: []string{"EMPTY=", "ONE=1", "ZERO="}},
		{name: "empty inheritance then config then request", cfg: &ExecutionConfig{Environment: EnvironmentConfig{Inherit: &inherit, Unset: []string{"ONE"}, Set: map[string]string{"ONE": "config", "TWO": "config"}}}, base: []string{"ONE=host", "HOST=secret"}, unset: []string{"TWO", "ONE"}, set: map[string]string{"ONE": "request", "THREE": ""}, want: []string{"ONE=request", "THREE="}},
		{name: "unset inherited and restore at config", cfg: &ExecutionConfig{Environment: EnvironmentConfig{Unset: []string{"ONE", "GONE"}, Set: map[string]string{"ONE": "replacement"}}}, base: []string{"ONE=host", "GONE=host"}, want: []string{"ONE=replacement"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NewEnvironmentSnapshot(tc.cfg, tc.base).Build(tc.unset, tc.set)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Build() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestEnvironmentSnapshotDoesNotObserveLaterMutation(t *testing.T) {
	base := []string{"FIRST=original"}
	cfg := &ExecutionConfig{Environment: EnvironmentConfig{Set: map[string]string{"NEXT": "original"}}}
	snapshot := NewEnvironmentSnapshot(cfg, base)
	base[0] = "FIRST=changed"
	cfg.Environment.Set["NEXT"] = "changed"
	got := snapshot.ForCommand(CommandRequest{Env: map[string]string{"FIRST": "request"}})
	if !reflect.DeepEqual(got, []string{"FIRST=request", "NEXT=original"}) {
		t.Fatalf("snapshot = %#v", got)
	}
}

func TestCloneConfigDeepCopiesExecution(t *testing.T) {
	inherit := false
	in := Config{BaseEnv: []string{}, BackendCandidates: []Backend{BackendHost}, ResourceLimits: &ResourceLimits{ReadPaths: []string{}}, Execution: &ExecutionConfig{Environment: EnvironmentConfig{Inherit: &inherit, Set: map[string]string{"A": "one"}, Unset: []string{"B"}}}}
	out := CloneConfig(in)
	if out.BaseEnv == nil || out.ResourceLimits.ReadPaths == nil || out.Execution.Environment.Inherit == nil || *out.Execution.Environment.Inherit {
		t.Fatalf("presence lost: %+v", out)
	}
	out.Execution.Environment.Set["A"] = "two"
	out.Execution.Environment.Unset[0] = "C"
	*out.Execution.Environment.Inherit = true
	if in.Execution.Environment.Set["A"] != "one" || in.Execution.Environment.Unset[0] != "B" || *in.Execution.Environment.Inherit {
		t.Fatal("clone changed source")
	}
	if CloneExecutionConfig(nil) != nil {
		t.Fatal("nil clone must stay nil")
	}
}

func TestExecutionValidationAndShellFlags(t *testing.T) {
	for _, req := range []CommandRequest{
		{Env: map[string]string{"": "value"}},
		{UnsetEnv: []string{"BAD=KEY"}},
		{Env: map[string]string{"GOOD": "bad\x00value"}},
	} {
		if err := ValidateCommandEnvironment(req); err == nil {
			t.Fatalf("ValidateCommandEnvironment(%+v) succeeded", req)
		}
	}
	if runtime.GOOS == "windows" {
		if err := ValidateCommandEnvironment(CommandRequest{Env: map[string]string{"PATH": "a", "Path": "b"}}); err == nil {
			t.Fatal("case-ambiguous environment accepted")
		}
		if err := ValidateExecutionConfig(&ExecutionConfig{Environment: EnvironmentConfig{Set: map[string]string{"PATH": "a", "Path": "b"}}}); err == nil {
			t.Fatal("case-ambiguous config accepted")
		}
	}
	for _, cfg := range []*ExecutionConfig{
		{Environment: EnvironmentConfig{Set: map[string]string{"BAD=NAME": "x"}}},
		{Environment: EnvironmentConfig{Unset: []string{""}}},
		{Environment: EnvironmentConfig{Set: map[string]string{"GOOD": "nul\x00byte"}}},
	} {
		if err := ValidateExecutionConfig(cfg); err == nil {
			t.Fatalf("ValidateExecutionConfig(%+v) succeeded", cfg)
		}
	}
	if err := ValidateExecutionConfig(nil); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if err := ValidateExecutionConfig(&ExecutionConfig{Shell: ShellConfig{Login: true}}); err == nil {
			t.Fatal("Windows login accepted")
		}
		if err := ValidateExecutionConfig(&ExecutionConfig{Shell: ShellConfig{Path: `C:\shell.exe`}}); err == nil {
			t.Fatal("Windows custom shell accepted")
		}
	} else {
		if err := ValidateExecutionConfig(&ExecutionConfig{Shell: ShellConfig{Path: "zsh"}}); err == nil {
			t.Fatal("relative shell accepted")
		}
		path, args := ShellArgs(nil, "printf ok")
		if path != "/bin/bash" || !reflect.DeepEqual(args, []string{"-c", "printf ok"}) {
			t.Fatalf("default = %q %q", path, args)
		}
		path, args = ShellArgs(&ExecutionConfig{Shell: ShellConfig{Path: "/bin/zsh", Login: true}}, "printf ok")
		if path != "/bin/zsh" || !reflect.DeepEqual(args, []string{"-lc", "printf ok"}) {
			t.Fatalf("custom = %q %q", path, args)
		}
		if err := ValidateConfig(Config{ResourceLimits: &ResourceLimits{}, Execution: &ExecutionConfig{Shell: ShellConfig{Login: true}}}); err == nil || !strings.Contains(err.Error(), "resource limits") {
			t.Fatalf("resource limit login validation = %v", err)
		}
	}
}

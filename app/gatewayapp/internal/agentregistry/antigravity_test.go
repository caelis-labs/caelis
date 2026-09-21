package agentregistry

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAntigravityLaunchContractByPlatform(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		goos    string
		command string
		args    []string
	}{
		{goos: "darwin", command: "agy_acp_server.par"},
		{goos: "linux", command: "agy_acp_server.par", args: []string{"--uid="}},
		{goos: "windows", command: "agy_acp_server.exe"},
	} {
		t.Run(test.goos, func(t *testing.T) {
			agent := antigravityACPAgent(test.goos)
			if agent.Config.Command != test.command || !reflect.DeepEqual(agent.Config.Args, test.args) {
				t.Fatalf("launcher = %#v, want %s %v", agent.Config, test.command, test.args)
			}
			if agent.Agent.Installation.Command != test.command || !reflect.DeepEqual(agent.Agent.Installation.Args, test.args) {
				t.Fatalf("installation launch differs from connection: %#v", agent.Agent.Installation)
			}
		})
	}
}

func TestAntigravityFindsUserInstallationWithoutPATHChange(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	agent, _ := LookupConnectableAgent("antigravity")
	setup, err := agent.Installation.Setup()
	if err != nil {
		t.Fatal(err)
	}
	if got := FindInstalledCommand("antigravity"); got != "" {
		t.Fatalf("unexpected existing runtime: %s", got)
	}
	if err := os.MkdirAll(setup.Directory, 0o700); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(setup.Directory, setup.Command)
	if err := os.WriteFile(command, []byte("runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := FindInstalledCommand("antigravity"); got != command {
		t.Fatalf("runtime = %s, want %s", got, command)
	}
}

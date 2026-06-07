package terminal

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/OnslaughtSnail/caelis/acp"
)

func TestTerminalSubdomainUsesCanonicalWireTypes(t *testing.T) {
	if MethodCreate != acp.MethodTerminalCreate ||
		MethodOutput != acp.MethodTerminalOutput ||
		MethodWaitForExit != acp.MethodTerminalWaitForExit ||
		MethodKill != acp.MethodTerminalKill ||
		MethodRelease != acp.MethodTerminalRelease {
		t.Fatalf("terminal method constants drifted")
	}

	limit := 4096
	req := CreateRequest{
		SessionID:       "sess-1",
		Command:         "go",
		Args:            []string{"test", "./..."},
		CWD:             "/repo",
		Env:             []EnvVariable{{Name: "GOFLAGS", Value: "-count=1"}},
		OutputByteLimit: &limit,
	}
	// Verify CreateRequest is a type alias of acp.CreateTerminalRequest (compile-time check).
	assertTypeAlias[acp.CreateTerminalRequest](req)
	roundTrip := req

	data, err := json.Marshal(roundTrip)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["sessionId"] != "sess-1" || got["command"] != "go" || got["cwd"] != "/repo" {
		t.Fatalf("json wire shape = %s", data)
	}

	code := 0
	output := OutputResponse{
		Output:     "ok\n",
		ExitStatus: &ExitStatus{ExitCode: &code},
	}
	// Verify OutputResponse is a type alias of acp.TerminalOutputResponse (compile-time check).
	assertTypeAlias[acp.TerminalOutputResponse](output)
	if output.ExitStatus == nil || output.ExitStatus.ExitCode == nil || *output.ExitStatus.ExitCode != 0 {
		t.Fatalf("canonical output = %#v", output)
	}
}

func TestProviderMatchesRootTerminalProvider(t *testing.T) {
	var _ Provider = providerStub{}
}

type providerStub struct{}

func (providerStub) CreateTerminal(context.Context, CreateRequest) (CreateResponse, error) {
	return CreateResponse{TerminalID: "term-1"}, nil
}

func (providerStub) TerminalOutput(context.Context, OutputRequest) (OutputResponse, error) {
	return OutputResponse{}, nil
}

func (providerStub) TerminalWaitForExit(context.Context, WaitForExitRequest) (WaitForExitResponse, error) {
	return WaitForExitResponse{}, nil
}

func (providerStub) TerminalKill(context.Context, KillRequest) error {
	return nil
}

func (providerStub) TerminalRelease(context.Context, ReleaseRequest) error {
	return nil
}

// assertTypeAlias is a compile-time check that T and the argument's type are identical.
func assertTypeAlias[T any](_ T) {}

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
	var canonical acp.CreateTerminalRequest = req
	var roundTrip CreateRequest = canonical

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
	var canonicalOutput acp.TerminalOutputResponse = output
	if canonicalOutput.ExitStatus == nil || canonicalOutput.ExitStatus.ExitCode == nil || *canonicalOutput.ExitStatus.ExitCode != 0 {
		t.Fatalf("canonical output = %#v", canonicalOutput)
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

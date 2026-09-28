// This fixture deliberately uses only the public Control client contracts.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// Usage: execution_client <create|inspect|prompt|prompt-restarted> <base-url> <credential-file> <workspace-or-session-id>.
func run(ctx context.Context, args []string) error {
	if len(args) != 4 {
		return fmt.Errorf("expected action, URL, credential file, workspace or Session ID")
	}
	secret, err := os.ReadFile(args[2])
	if err != nil {
		return err
	}
	client, err := httpclient.New(httpclient.Config{BaseURL: args[1], BearerToken: strings.TrimSpace(string(secret)), Compatibility: appserver.CurrentCompatibility()})
	if err != nil {
		return err
	}
	info, err := client.Initialize(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, capability := range info.Capabilities {
		if capability == appserver.CapabilityExecutionConfiguration {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("host does not advertise execution-configuration-v1")
	}
	switch args[0] {
	case "create":
		inherit := false
		profile := application.Profile{
			Version: "external-client/1", Instructions: "Execute the controlled native command.",
			Model: "openai-compatible/gpt-4.1", ToolsVersion: "external-client/1", Execution: "workspace-write",
			Workspace: application.Workspace{CWD: args[3]},
			ExecutionConfig: &sandbox.ExecutionConfig{Environment: sandbox.EnvironmentConfig{
				Inherit: &inherit, Set: map[string]string{"CAELIS_EXTERNAL_FIXTURE": "external-value"},
			}},
		}
		result, err := client.CreateApplicationSession(ctx, appserver.CreateApplicationSessionRequest{WriteBase: appserver.WriteBase{OperationID: "external-create"}, Profile: profile})
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	case "inspect":
		binding, err := client.ApplicationSession(ctx, args[3])
		if err != nil {
			return err
		}
		state, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: args[3]})
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			Binding application.Binding    `json:"binding"`
			State   appserver.SessionState `json:"state"`
		}{binding, state})
	case "prompt", "prompt-restarted":
		result, err := client.PromptApplication(ctx, appserver.ApplicationPromptRequest{PromptRequest: appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: args[3], OperationID: "external-" + args[0]}, Input: "Execute the synthetic fixture."}, SourceKind: "user"})
		if err != nil {
			return err
		}
		until := time.NewTicker(20 * time.Millisecond)
		defer until.Stop()
		for {
			state, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: args[3]})
			if err != nil {
				return err
			}
			if !state.Run.Active {
				return json.NewEncoder(os.Stdout).Encode(result)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-until.C:
			}
		}
	default:
		return fmt.Errorf("unknown action %q", args[0])
	}
}

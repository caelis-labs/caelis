package httpclient_test

import (
	"context"
	"fmt"
	"log"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

// All surfaces authenticate with the Host identity. Persist the command identity
// before dispatch so a lost response can be reconciled without replay.
func ExampleClient_CreateSession_profile() {
	ctx := context.Background()
	client, err := httpclient.New(httpclient.Config{
		BaseURL:     "http://127.0.0.1:7777",
		BearerToken: "host-auth-token",
	})
	if err != nil {
		log.Fatal(err)
	}
	info, err := client.Initialize(ctx)
	if err != nil {
		log.Fatal(err)
	}
	supported := false
	for _, capability := range info.Capabilities {
		if capability == "session-profiles-v1" {
			supported = true
		}
	}
	if !supported {
		log.Fatal("Host lacks session-profiles-v1")
	}

	// Persist this exact operation ID and profile before the first request.
	request := appserver.CreateSessionRequest{
		WriteBase: appserver.WriteBase{OperationID: "example-create-1"},
		Profile: &sessionprofile.Profile{
			Version: "example-role/1", Instructions: "Answer using only the supplied evidence.",
			Model: "configured-provider-model-id", ToolsVersion: "example-tools/1",
			Execution: "tools-only",
		},
	}
	result, err := client.CreateSession(ctx, request)
	if err != nil || result.Outcome == appserver.OutcomeUnknown {
		// Observation, not redispatch. Unknown does not authorize a new ID.
		receipt, queryErr := client.Operation(ctx, request.OperationID)
		if queryErr != nil {
			log.Fatal(queryErr)
		}
		fmt.Println(receipt.OperationID, receipt.Outcome)
		return
	}
	fmt.Println(result.SessionID, result.Outcome)
	// Creation admission is not a native Turn terminal. Use Prompt
	// and canonical Reconnect/Subscribe to submit and observe subsequent work.
}

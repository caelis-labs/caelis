package httpclient_test

import (
	"context"
	"fmt"
	"log"
	"slices"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
)

func ExampleClient_CreateWorker() {
	ctx := context.Background()
	client, err := httpclient.New(httpclient.Config{BaseURL: "http://127.0.0.1:7777", BearerToken: "persisted-scoped-application-credential"})
	if err != nil {
		log.Fatal(err)
	}
	info, err := client.Initialize(ctx)
	if err != nil || !slices.Contains(info.Capabilities, appserver.CapabilitySharedWorkers) || !slices.Contains(info.Capabilities, appserver.CapabilityTurnSteering) {
		log.Fatal("Host lacks shared Worker support")
	}
	// Persist request bytes and ID before dispatch; reuse neither for another task.
	created, err := client.CreateWorker(ctx, appserver.CreateWorkerRequest{WriteBase: appserver.WriteBase{OperationID: "worker-create-1"}, CWD: "/absolute/user/workspace", Title: "Shared work"})
	if err != nil || created.Outcome != appserver.OutcomeCommitted {
		receipt, queryErr := client.ApplicationOperation(ctx, "worker-create-1")
		fmt.Println(receipt.Outcome, queryErr) // Reconcile; never invent another ID.
		return
	}
	sid := created.SessionID
	feed, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sid})
	if err != nil {
		log.Fatal(err)
	}
	defer feed.Subscription.Close()
	turn, err := client.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "worker-prompt-1", SessionID: sid}, Input: "Inspect this workspace and explain its test entry points."})
	if err != nil || turn.Outcome != appserver.OutcomeCommitted {
		return
	}
	// While this exact Turn is active, an authorized instruction can use:
	// client.Steer(ctx, appserver.SteerRequest{WriteBase: appserver.WriteBase{
	//   OperationID: "worker-steer-1", SessionID: sid}, Target: turn.Target,
	//   Input: "Focus on the integration tests."})
	var assembler appserver.FeedDeliveryAssembler
	var history []eventstream.Envelope
	var cursor string
	for delivery := range feed.Subscription.Deliveries() {
		events, replace, err := assembler.Accept(delivery)
		if err != nil {
			log.Fatal(err)
		}
		if replace {
			history = events
		} else {
			history = append(history, events...)
		}
		// Persist history and cursor atomically in the Bot's own local projection.
		if delivery.NextCursor != "" {
			cursor = delivery.NextCursor
		}
		for _, e := range events {
			if e.InputStatus == "applied" {
				fmt.Println("steering applied", e.InputOperationID)
			}
			if eventstream.IsTurnTerminalLifecycle(e) {
				fmt.Println(e.TurnID, e.Lifecycle.State)
			}
		}
		// Keep observing after completion: the user can start another Turn in TUI.
	}
	// Reconnect with the saved cursor/history after a transport failure. Apply any
	// replacement transaction atomically; never resend CreateWorker or Prompt.
	fmt.Println(cursor, len(history), feed.Subscription.Err())
}

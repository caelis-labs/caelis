package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func check(ok bool, message string) {
	if !ok {
		panic(message)
	}
}
func write(path string, value any) {
	raw, err := json.MarshalIndent(value, "", "  ")
	must(err)
	must(os.WriteFile(path, raw, 0600))
}
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	url, root, provider, phase := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	token, err := os.ReadFile(filepath.Join(root, "host.token"))
	must(err)
	makeClient := func(token string) *httpclient.Client {
		c, err := httpclient.New(httpclient.Config{BaseURL: url, BearerToken: strings.TrimSpace(token), Compatibility: appserver.CurrentCompatibility(sessionprofile.Capability)})
		must(err)
		return c
	}
	host := makeClient(string(token))
	info, err := host.Initialize(ctx)
	must(err)
	client := makeClient(string(token))
	saved := filepath.Join(root, "saved.json")
	var receipt struct {
		Create, Prompt appserver.CommandResult
		Terminal       eventstream.Envelope
		Config         sessionprofile.Configuration
		Resource       sessionprofile.Resource
		Grant          sessionprofile.BackgroundGrant
		Native         appserver.CommandResult
	}
	if phase == "create" {
		status, err := host.SessionStatus(ctx, appserver.StatusRequest{})
		must(err)
		connected, err := host.ConnectModel(ctx, appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{OperationID: "connect", ExpectedRevision: &status.Configuration.Revision}, Config: appserver.ConnectConfig{Provider: "custom", Model: "matrix", BaseURL: provider + "/v1", APIKey: "synthetic-matrix-only", ContextWindowTokens: 32768, MaxOutputTokens: 1024}})
		must(err)
		check(connected.Outcome == appserver.OutcomeCommitted, fmt.Sprintf("connect: %+v", connected))
		profile := sessionprofile.Profile{Version: "matrix/1", Instructions: "Use Code Mode once.", Model: "custom/matrix", ToolsVersion: "matrix/1", Execution: "tools-only"}
		create := appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "create"}, Profile: &profile}
		receipt.Create, err = client.CreateSession(ctx, create)
		must(err)
		check(receipt.Create.Outcome == appserver.OutcomeCommitted, "create outcome")
		duplicate, err := client.CreateSession(ctx, create)
		must(err)
		check(reflect.DeepEqual(duplicate, receipt.Create), "creation receipt changed")
		id := receipt.Create.SessionID
		instructions := "MATRIX_CONFIG: use Code Mode once."
		update := appserver.UpdateSessionConfigurationRequest{WriteBase: appserver.WriteBase{OperationID: "configure", SessionID: id}, ExpectedConfigurationRevision: 1, Patch: sessionprofile.ConfigurationPatch{Instructions: &instructions}}
		configured, err := client.UpdateSessionConfiguration(ctx, update)
		must(err)
		check(configured.Outcome == appserver.OutcomeCommitted && configured.Configuration != nil, "configuration receipt missing")
		receipt.Config = *configured.Configuration
		must(err)
		check(receipt.Config.Revision == 2, "configuration revision")
		again, err := client.UpdateSessionConfiguration(ctx, update)
		must(err)
		check(again.Configuration != nil && reflect.DeepEqual(*again.Configuration, receipt.Config), "configuration receipt changed")
		data := []byte("MATRIX_RESOURCE")
		digest := sha256.Sum256(data)
		uploaded, err := client.CreateSessionResource(ctx, appserver.SessionResourceRequest{WriteBase: appserver.WriteBase{OperationID: "upload", SessionID: id}, Name: "matrix.txt", MediaType: "text/plain", Data: data, SHA256: hex.EncodeToString(digest[:])})
		must(err)
		check(uploaded.Outcome == appserver.OutcomeCommitted && uploaded.SessionResource != nil, "resource receipt missing")
		receipt.Resource = *uploaded.SessionResource
		granted, err := client.CreateSessionBackgroundGrant(ctx, appserver.CreateSessionBackgroundGrantRequest{WriteBase: appserver.WriteBase{OperationID: "grant", SessionID: id}, Source: "synthetic-schedule", AuthorizationOperationID: "synthetic-user-authorization"})
		must(err)
		check(granted.Outcome == appserver.OutcomeCommitted && granted.BackgroundGrant != nil, "grant receipt missing")
		receipt.Grant = *granted.BackgroundGrant
		receipt.Native, err = host.CreateSession(ctx, appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "native-create"}, CWD: filepath.Join(root, "workspace"), Title: "TUI shared Control"})
		must(err)
		_, err = host.InspectSession(ctx, appserver.StateRequest{SessionID: receipt.Native.SessionID})
		must(err)
		receipt.Prompt, err = client.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "prompt", SessionID: id}, Input: "Look up one synthetic value.", SourceKind: "user"})
		must(err)
		must(waitTranscript(ctx, client, id, receipt.Prompt.Target, &receipt.Terminal))
		write(saved, receipt)
	} else {
		raw, err := os.ReadFile(saved)
		must(err)
		must(json.Unmarshal(raw, &receipt))
		op, err := client.Operation(ctx, receipt.Create.OperationID)
		must(err)
		check(reflect.DeepEqual(op, receipt.Create), "original create receipt lost after restart")
		op, err = client.Operation(ctx, receipt.Prompt.OperationID)
		must(err)
		check(reflect.DeepEqual(op, receipt.Prompt), "original prompt receipt lost after restart")
		config, err := client.Operation(ctx, "configure")
		must(err)
		check(config.Configuration != nil && reflect.DeepEqual(*config.Configuration, receipt.Config), "configuration operation changed")
		must(waitTranscript(ctx, client, receipt.Create.SessionID, receipt.Prompt.Target, &receipt.Terminal))
	}
	resource, err := client.SessionResourceContent(ctx, receipt.Create.SessionID, receipt.Resource.ID)
	must(err)
	check(string(resource.Data) == "MATRIX_RESOURCE", "resource readback")
	binding, err := client.SessionProfile(ctx, receipt.Create.SessionID)
	must(err)
	check(binding.SessionID == receipt.Create.SessionID, "binding scope changed")
	grant, err := client.SessionBackgroundGrant(ctx, receipt.Create.SessionID, receipt.Grant.ID)
	must(err)
	check(reflect.DeepEqual(grant, receipt.Grant), "background grant changed")
	state, err := host.InspectSession(ctx, appserver.StateRequest{SessionID: receipt.Native.SessionID})
	must(err)
	check(state.SessionID == receipt.Native.SessionID, "native session missing")
	_, err = client.InspectSession(ctx, appserver.StateRequest{SessionID: receipt.Native.SessionID})
	must(err)
	write(filepath.Join(root, "evidence-"+phase+".json"), map[string]any{"phase": phase, "server": info, "session": receipt.Create.SessionID, "configuration_revision": receipt.Config.Revision, "operation_ids": []string{"create", "configure", "prompt"}, "resource_verified": true, "sse_replay_verified": true, "same_owner_native_session_verified": true})
	fmt.Println("PASS", phase, receipt.Create.SessionID)
}
func waitTranscript(ctx context.Context, client *httpclient.Client, id string, target appserver.TurnTarget, terminal *eventstream.Envelope) error {
	result, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: id})
	if err != nil {
		return err
	}
	defer result.Subscription.Close()
	return consumeTranscript(ctx, result.Subscription, id, target, terminal)
}

func consumeTranscript(ctx context.Context, subscription appserver.FeedSubscription, id string, target appserver.TurnTarget, terminal *eventstream.Envelope) error {
	if target.TurnID == "" || terminal == nil {
		return errors.New("prompt receipt has no Turn identity")
	}
	var assembler appserver.FeedDeliveryAssembler
	messages := map[string]string{}
	// Historical canonical replacement does not repeat transient Turn terminals.
	// Observe phases reuse the exact completed terminal captured by the create
	// phase; they still require a complete replacement and the replay sync cut.
	matches := func(event eventstream.Envelope) bool {
		return event.SessionID == id && event.TurnID == target.TurnID && event.Scope == eventstream.ScopeMain &&
			((event.HandleID == "" && event.RunID == "") || (event.HandleID == target.HandleID && event.RunID == target.RunID))
	}
	confirmed := eventstream.IsTurnTerminalLifecycle(*terminal) && matches(*terminal) && terminal.Lifecycle.State == eventstream.LifecycleStateCompleted
	replaying := confirmed
	if terminal.Kind != "" && !confirmed {
		return errors.New("saved terminal does not prove the target Turn completed")
	}
	for {
		select {
		case delivery, ok := <-subscription.Deliveries():
			if err := subscription.Err(); err != nil {
				return err
			}
			if !ok {
				return errors.New("feed closed before committed transcript and target terminal")
			}
			events, replacement, err := assembler.Accept(delivery)
			if err != nil {
				return err
			}
			if replacement {
				clear(messages)
			}
			for _, event := range events {
				if !matches(event) {
					continue
				}
				if eventstream.IsTurnTerminalLifecycle(event) {
					if event.Lifecycle.State != eventstream.LifecycleStateCompleted {
						return fmt.Errorf("target Turn ended in %s", event.Lifecycle.State)
					}
					*terminal = eventstream.CloneEnvelope(event)
					confirmed = true
				}
				// The live feed may suppress an identical canonical final message.
				// Apply its committed display deltas/replacements, then require the
				// target terminal below before accepting their text.
				chunk, ok := event.Update.(eventstream.ContentChunk)
				if !ok || chunk.SessionUpdate != eventstream.UpdateAgentMessage || chunk.MessageID == "" {
					continue
				}
				content, ok := chunk.Content.(eventstream.TextContent)
				if !ok {
					continue
				}
				if chunk.Replacement {
					messages[chunk.MessageID] = content.Text
				} else {
					messages[chunk.MessageID] += content.Text
				}
			}
			if confirmed && !assembler.Pending() && (!replaying || delivery.Kind == appserver.FeedDeliverySync) {
				if err := subscription.Err(); err != nil {
					return err
				}
				for _, text := range messages {
					if strings.Contains(text, "MATRIX_FINAL") {
						return nil
					}
				}
				return errors.New("completed target Turn has no expected assistant text")
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

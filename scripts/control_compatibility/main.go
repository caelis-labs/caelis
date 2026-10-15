package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
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
func equalJSON(a, b any) bool {
	x, e := json.Marshal(a)
	must(e)
	y, e := json.Marshal(b)
	must(e)
	return string(x) == string(y)
}
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	url, root, provider, phase := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	token, err := os.ReadFile(filepath.Join(root, "host.token"))
	must(err)
	makeClient := func(token string) *httpclient.Client {
		c, err := httpclient.New(httpclient.Config{BaseURL: url, BearerToken: strings.TrimSpace(token), Compatibility: appserver.CurrentCompatibility(application.Capability)})
		must(err)
		return c
	}
	host := makeClient(string(token))
	info, err := host.Initialize(ctx)
	must(err)
	token, err = os.ReadFile(filepath.Join(root, "app.token"))
	must(err)
	client := makeClient(string(token))
	saved := filepath.Join(root, "saved.json")
	var receipt struct {
		Create, Prompt appserver.CommandResult
		Config         application.Configuration
		Resource       application.Resource
		Call           application.Call
		Grant          application.BackgroundGrant
		Native         appserver.CommandResult
	}
	if phase == "create" {
		status, err := host.SessionStatus(ctx, appserver.StatusRequest{})
		must(err)
		connected, err := host.ConnectModel(ctx, appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{OperationID: "connect", ExpectedRevision: &status.Configuration.Revision}, Config: appserver.ConnectConfig{Provider: "custom", Model: "matrix", BaseURL: provider + "/v1", APIKey: "synthetic-matrix-only", ContextWindowTokens: 32768, MaxOutputTokens: 1024}})
		must(err)
		check(connected.Outcome == appserver.OutcomeCommitted, fmt.Sprintf("connect: %+v", connected))
		_, err = host.RegisterApplication(ctx, application.Registration{OperationID: "enroll", Name: "matrix", Credential: string(token)})
		must(err)
		_, err = client.Initialize(ctx)
		must(err)
		profile := application.Profile{Version: "matrix/1", Instructions: "Call MatrixLookup once.", Model: "custom/matrix", ToolsVersion: "matrix/1", Execution: "tools-only", Tools: []application.ToolDefinition{{Name: "MatrixLookup", Description: "Return synthetic matrix value.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}}}}}
		create := appserver.CreateApplicationSessionRequest{WriteBase: appserver.WriteBase{OperationID: "create"}, Profile: profile}
		receipt.Create, err = client.CreateApplicationSession(ctx, create)
		must(err)
		check(receipt.Create.Outcome == appserver.OutcomeCommitted, "create outcome")
		duplicate, err := client.CreateApplicationSession(ctx, create)
		must(err)
		check(reflect.DeepEqual(duplicate, receipt.Create), "creation receipt changed")
		id := receipt.Create.SessionID
		instructions := "MATRIX_CONFIG: call MatrixLookup once."
		update := application.UpdateConfigurationRequest{OperationID: "configure", ExpectedConfigurationRevision: 1, Patch: application.ConfigurationPatch{Instructions: &instructions}}
		receipt.Config, err = client.UpdateApplicationConfiguration(ctx, id, update)
		must(err)
		check(receipt.Config.Revision == 2, "configuration revision")
		again, err := client.UpdateApplicationConfiguration(ctx, id, update)
		must(err)
		check(reflect.DeepEqual(again, receipt.Config), "configuration receipt changed")
		data := []byte("MATRIX_RESOURCE")
		digest := sha256.Sum256(data)
		receipt.Resource, err = client.UploadApplicationResource(ctx, appserver.ApplicationResourceRequest{WriteBase: appserver.WriteBase{OperationID: "upload", SessionID: id}, Name: "matrix.txt", MediaType: "text/plain", Data: data, SHA256: hex.EncodeToString(digest[:])})
		must(err)
		receipt.Grant, err = client.CreateApplicationBackgroundGrant(ctx, id, application.BackgroundGrantRequest{OperationID: "grant", Source: "synthetic-schedule", AuthorizationOperationID: "synthetic-user-authorization"})
		must(err)
		receipt.Native, err = host.CreateSession(ctx, appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "native-create"}, CWD: filepath.Join(root, "workspace"), Title: "TUI shared Control"})
		must(err)
		_, err = host.InspectSession(ctx, appserver.StateRequest{SessionID: receipt.Native.SessionID})
		must(err)
		receipt.Prompt, err = client.PromptApplication(ctx, appserver.ApplicationPromptRequest{PromptRequest: appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "prompt", SessionID: id}, Input: "Look up one synthetic value."}, SourceKind: "user"})
		must(err)
		for {
			calls, err := client.ApplicationCalls(ctx, id)
			must(err)
			if len(calls) > 0 {
				check(len(calls) == 1, "more than one callback")
				claimed, err := client.ClaimApplicationCall(ctx, id, calls[0].ID)
				must(err)
				check(claimed.State == "claimed", "claim did not grant effect")
				check(claimed.ConfigurationRevision == 2, "wrong callback config")
				effect, err := os.OpenFile(filepath.Join(root, "callback-effect"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				must(err)
				_, err = effect.WriteString(claimed.ID)
				must(err)
				must(effect.Close())
				result := application.CallResult{Outcome: "succeeded", Content: json.RawMessage(`"MATRIX_CALLBACK_EFFECT"`)}
				must(client.CompleteApplicationCall(ctx, id, claimed.ID, result))
				receipt.Call, err = client.ApplicationCall(ctx, id, claimed.ID)
				must(err)
				check(receipt.Call.State == "completed", "callback missing completion")
				break
			}
			select {
			case <-ctx.Done():
				panic(ctx.Err())
			case <-time.After(25 * time.Millisecond):
			}
		}
		waitTranscript(ctx, client, id)
		write(saved, receipt)
	} else {
		raw, err := os.ReadFile(saved)
		must(err)
		must(json.Unmarshal(raw, &receipt))
		op, err := client.ApplicationOperation(ctx, receipt.Create.OperationID)
		must(err)
		check(op.Result != nil && reflect.DeepEqual(*op.Result, receipt.Create), "original create receipt lost after restart")
		op, err = client.ApplicationOperation(ctx, receipt.Prompt.OperationID)
		must(err)
		check(op.Result != nil && reflect.DeepEqual(*op.Result, receipt.Prompt), "original prompt receipt lost after restart")
		config, err := client.ApplicationConfigurationOperation(ctx, "configure")
		must(err)
		check(reflect.DeepEqual(config, receipt.Config), "configuration operation changed")
		call, err := client.ApplicationCall(ctx, receipt.Create.SessionID, receipt.Call.ID)
		must(err)
		check(equalJSON(call, receipt.Call), "callback receipt changed")
		_, err = client.ClaimApplicationCall(ctx, receipt.Create.SessionID, receipt.Call.ID)
		check(err != nil, "completed callback was claimable twice")
		waitTranscript(ctx, client, receipt.Create.SessionID)
	}
	effect, err := os.ReadFile(filepath.Join(root, "callback-effect"))
	must(err)
	check(string(effect) == receipt.Call.ID, "independent callback effect readback changed")
	resource, err := client.ReadApplicationResource(ctx, receipt.Create.SessionID, receipt.Resource.ID)
	must(err)
	check(string(resource.Data) == "MATRIX_RESOURCE", "resource readback")
	bindings, err := client.ListApplicationSessions(ctx)
	must(err)
	check(len(bindings) == 1 && bindings[0].SessionID == receipt.Create.SessionID, "binding scope changed")
	grant, err := client.ApplicationBackgroundGrant(ctx, receipt.Create.SessionID, receipt.Grant.ID)
	must(err)
	check(reflect.DeepEqual(grant, receipt.Grant), "background grant changed")
	state, err := host.InspectSession(ctx, appserver.StateRequest{SessionID: receipt.Native.SessionID})
	must(err)
	check(state.SessionID == receipt.Native.SessionID, "native session missing")
	_, err = client.InspectSession(ctx, appserver.StateRequest{SessionID: receipt.Native.SessionID})
	check(err != nil, "application could inspect unrelated native session")
	write(filepath.Join(root, "evidence-"+phase+".json"), map[string]any{"phase": phase, "server": info, "session": receipt.Create.SessionID, "configuration_revision": receipt.Config.Revision, "operation_ids": []string{"create", "configure", "prompt"}, "callback_id": receipt.Call.ID, "callback_effect_verified": true, "resource_verified": true, "sse_replay_verified": true, "unrelated_session_denied": true})
	fmt.Println("PASS", phase, receipt.Create.SessionID)
}
func waitTranscript(ctx context.Context, client *httpclient.Client, id string) {
	result, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: id})
	must(err)
	defer result.Subscription.Close()
	for {
		select {
		case delivery, ok := <-result.Subscription.Deliveries():
			check(ok, "feed closed before final transcript")
			raw, err := json.Marshal(delivery)
			must(err)
			if strings.Contains(string(raw), "MATRIX_FINAL") {
				return
			}
		case <-ctx.Done():
			panic(ctx.Err())
		}
	}
}

package gatewayapp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func TestApplicationCallbackCodeModeHTTP(t *testing.T) {
	for _, tc := range []struct {
		name       string
		data       map[string]any
		check      string
		withSchema bool
	}{
		{"payload", map[string]any{"score": json.Number("7")}, `data.score === 7 && Object.keys(data).join(",") === "score"`, true},
		{"empty", map[string]any{}, `typeof data === "object" && data !== null && Object.keys(data).length === 0`, false},
		{"absent", nil, `data === undefined`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			root := t.TempDir()
			provider := &multimodalHTTPModel{script: `
const result = await tools.call("ApplicationLookup", {key:"from-script"});
const data = result.data;
if (!(` + tc.check + `)) throw new Error("Unexpected callback data " + JSON.stringify(data));
return {verified:"APP_CALLBACK_JS_VERIFIED"};`}
			host := startApplicationHTTPHost(t, filepath.Join(root, "store"), t.TempDir(), provider)
			defer host.close(t)
			status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
			if err != nil {
				t.Fatal(err)
			}
			connected, err := host.host.ConnectModel(ctx, appserver.ConnectModelRequest{
				WriteBase: appserver.WriteBase{OperationID: "connect-model", ExpectedRevision: &status.Configuration.Revision},
				Config:    appserver.ConnectConfig{Provider: "custom-chat", Model: "gpt-4.1", BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC_ONLY"},
			})
			if err != nil || connected.Outcome != appserver.OutcomeCommitted {
				t.Fatalf("ConnectModel: %+v %v", connected, err)
			}
			client, _ := connectSessionOwnerHTTP(t, ctx, host, "owner", filepath.Join(root, "app.credential"))
			profile := multimodalHTTPProfile("gpt-4.1")
			if !tc.withSchema {
				profile.Tools[0].OutputSchema = nil
			}
			created, err := client.CreateSession(ctx, appserver.CreateSessionRequest{
				WriteBase: appserver.WriteBase{OperationID: "create-callback-session"}, Profile: &profile,
			})
			if err != nil || created.Outcome != appserver.OutcomeCommitted || created.SessionID == "" {
				t.Fatalf("CreateSession: %+v %v", created, err)
			}
			sid := created.SessionID
			prompt, err := client.Prompt(ctx, appserver.PromptRequest{
				WriteBase: appserver.WriteBase{SessionID: sid, OperationID: "callback-script-turn"}, Input: "Check the application result in JavaScript.", SourceKind: "user",
			})
			if err != nil || (prompt.Outcome != appserver.OutcomeAccepted && prompt.Outcome != appserver.OutcomeCommitted) {
				t.Fatalf("Prompt: %+v %v", prompt, err)
			}
			calls, err := client.WaitSessionToolCalls(ctx, sid)
			if err != nil || len(calls) != 1 || calls[0].Name != "ApplicationLookup" || string(calls[0].Arguments) != `{"key":"from-script"}` {
				t.Fatalf("nested callback: %+v %v", calls, err)
			}
			call := calls[0]
			if _, err := client.ClaimSessionToolCall(ctx, sid, call.ID); err != nil {
				t.Fatal(err)
			}
			content := json.RawMessage(`[{"type":"text","text":"APP_CALLBACK_TEXT"}]`)
			result := sessionprofile.CallResult{Outcome: "succeeded", ResultFormat: sessionprofile.ResultFormatContentV1, Content: content, StructuredContent: tc.data}
			if err := client.CompleteSessionToolCall(ctx, sid, call.ID, result); err != nil {
				t.Fatal(err)
			}
			stored, err := client.SessionToolCall(ctx, sid, call.ID)
			if err != nil || stored.State != "completed" || stored.Result == nil || stored.Result.Outcome != result.Outcome ||
				!bytes.Equal(stored.Result.Content, content) || !reflect.DeepEqual(stored.Result.StructuredContent, tc.data) {
				t.Fatalf("stored application receipt changed: %+v %v", stored, err)
			}
			waitApplicationHTTPIdle(t, ctx, client, sid)
			requests := provider.snapshot()
			if len(requests) != 2 || !bytes.Contains(requests[1], []byte("APP_CALLBACK_JS_VERIFIED")) {
				t.Fatalf("real js_exec did not verify the application payload: %q", requests)
			}
			for _, envelope := range applicationHTTPHistory(t, ctx, client, sid) {
				update, ok := envelope.Update.(eventstream.ToolCallUpdate)
				if !ok || update.ToolCallID != call.CallID || len(update.Content) == 0 {
					continue
				}
				encoded, err := json.Marshal(update.Content)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(encoded, []byte("APP_CALLBACK_TEXT")) && bytes.Contains(encoded, []byte("receipt_id")) && bytes.Contains(encoded, []byte(call.ID)) && bytes.Contains(encoded, []byte("succeeded")) {
					return
				}
			}
			t.Fatalf("canonical history lost visible callback receipt %q", call.ID)
		})
	}
}

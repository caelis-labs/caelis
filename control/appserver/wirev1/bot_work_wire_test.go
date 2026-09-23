package wirev1

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
)

func TestBotManagedWorkAndDesktopWireConformance(t *testing.T) {
	revision := uint64(math.MaxUint64)
	base := appserver.WriteBase{OperationID: "operation", SessionID: "bot-chat", ExpectedRevision: &revision}
	target := bot.Execution{InstanceID: "host", SessionID: "work-session", HandleID: "handle", RunID: "run", TurnID: "turn"}
	config := bot.Config{Name: "Assistant", ManagedWork: true, DesktopActions: true, WorkPermission: "workspace-write"}
	source := bot.RequestSource{ID: "source", Kind: "user_request", PrincipalID: "owner", BotID: "bot", ClientID: "client", OperationID: "prompt", Digest: "digest", Text: "report", Execution: target, ContentParts: []model.ContentPart{{Type: model.ContentPartImage, MimeType: "image/png", Data: "aW1n", FileName: "report.png"}}}
	work := bot.Work{ID: "work", PrincipalID: "owner", BotID: "bot", SessionID: "work-session", WorkspaceKey: "workspace", SourceID: source.ID, Assignment: "report", CreationOperationID: "create", CreationDigest: "digest", Config: config, Execution: target, Status: "succeeded", Result: "result"}
	client := bot.Client{ID: "client", BotID: "bot", PrincipalID: "owner", Actions: []string{"clock"}, Active: true, ActivationID: "activation", InstanceID: "host"}
	call := bot.DesktopCall{ID: "action", BotID: "bot", ClientID: "client", PrincipalID: "owner", ActivationID: "activation", Execution: target, ItemID: "item", SourceID: source.ID, Action: "clock", State: "completed", Result: json.RawMessage(`{"time":"now"}`)}
	values := map[string]any{
		"BotConfig":                config,
		"BotExecution":             target,
		"BotRequestSource":         source,
		"BotWork":                  work,
		"BotWorkList":              []bot.Work{work},
		"BotWorkOperation":         bot.WorkOperation{PrincipalID: "owner", BotID: "bot", WorkID: "work", OperationID: "create", Digest: "digest", Outcome: "unknown", Execution: target},
		"BotCompletion":            bot.Completion{ID: "completion", PrincipalID: "owner", BotID: "bot", WorkID: "work", Execution: target, Status: "succeeded", Summary: "done", ReportState: "claimed"},
		"BotWorkRequest":           appserver.BotWorkRequest{WriteBase: base, BotID: "bot", WorkID: "work", SourceID: "source", Assignment: "report", Target: target},
		"RegisterBotClientRequest": appserver.RegisterBotClientRequest{WriteBase: base, BotID: "bot", Actions: []string{"clock"}},
		"BotClientExitRequest":     appserver.BotClientExitRequest{WriteBase: base, BotID: "bot", ActivationID: "activation", CancelOwnedWork: true},
		"BotReminderRequest":       appserver.BotReminderRequest{WriteBase: base, BotID: "bot", GrantID: "grant", Version: "version", Due: time.Unix(123, 0).UTC()},
		"BotClient":                client,
		"BotClientRegistration":    bot.ClientRegistration{Client: client, Token: "native-only"},
		"BotDesktopCall":           call,
		"BotDesktopSnapshot":       bot.DesktopSnapshot{Cursor: "cursor", Calls: []bot.DesktopCall{call}},
		"BotDesktopClaim":          bot.DesktopClaim{Call: call, Token: "native-only"},
		"BotDesktopReceipt":        bot.DesktopReceipt{Token: "native-only", Result: call.Result},
		"BotReminderGrant":         bot.ReminderGrant{ID: "grant", Version: "version", ClientID: "client", PrincipalID: "owner", BotID: "bot", SourceID: "source", Arguments: bot.DesktopArguments{Operation: "save", ID: "native", Label: "Report", Prompt: "Read it", EveryMinutes: 60}, Active: true, LastOccurrence: time.Unix(123, 0).UTC(), CoalescedThrough: time.Unix(456, 0).UTC()},
		"BotReminderFireList":      []bot.ReminderFire{{ID: "fire", GrantID: "grant", Version: "version", PrincipalID: "owner", BotID: "bot", ClientID: "client", SourceID: "source", State: "claimed", Due: time.Unix(123, 0).UTC(), Execution: target}},
	}
	for name, value := range values {
		t.Run(name, func(t *testing.T) {
			validateWireValue(t, name, value)
			raw, err := Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			decoded := reflect.New(reflect.TypeOf(value))
			if strings.HasSuffix(name, "Request") {
				err = DecodeRequest(raw, decoded.Interface())
			} else {
				err = Unmarshal(raw, decoded.Interface())
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(value, decoded.Elem().Interface()) {
				t.Fatalf("wire round trip: %s", raw)
			}
		})
	}
}

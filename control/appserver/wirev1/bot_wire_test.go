package wirev1

import (
	"bytes"
	"math"
	"reflect"
	"testing"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
)

func TestBotWireUsesDecimalRevision(t *testing.T) {
	want := bot.Bot{
		ID: "bot-1", SessionID: "bot-chat-1", Revision: math.MaxUint64,
		Config: bot.Config{Name: "Ada", Description: "Investigate.", Model: "mimo", Effort: "high", Fast: true},
	}
	raw, err := Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"revision":"18446744073709551615"`)) {
		t.Fatalf("wire JSON = %s, want decimal string revision", raw)
	}
	var got bot.Bot
	if err := Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
}

func TestBotListWireRoundTripsDecimalRevision(t *testing.T) {
	want := []bot.Bot{
		{ID: "bot-1", SessionID: "bot-chat-1", Revision: 7, Config: bot.Config{Name: "Ada"}},
		{ID: "bot-2", SessionID: "bot-chat-2", Revision: math.MaxUint64, Config: bot.Config{Name: "Grace"}},
	}
	raw, err := Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"revision":"18446744073709551615"`)) {
		t.Fatalf("wire JSON = %s, want decimal string revision", raw)
	}
	var got []bot.Bot
	if err := Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
}

func TestBotRequestsEncodeExpectedRevisionAsDecimal(t *testing.T) {
	revision := uint64(math.MaxUint64)
	requests := map[string]any{
		"create": appserver.CreateBotRequest{
			WriteBase: appserver.WriteBase{OperationID: "bot-create-1"},
			Config:    bot.Config{Name: "Ada"},
		},
		"update": appserver.UpdateBotRequest{
			WriteBase: appserver.WriteBase{OperationID: "bot-update-1", SessionID: "bot-chat-1", ExpectedRevision: &revision},
			BotID:     "bot-1", Config: bot.Config{Name: "Ada"},
		},
	}
	for name, request := range requests {
		t.Run(name, func(t *testing.T) {
			raw, err := Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			if name == "update" && !bytes.Contains(raw, []byte(`"expected_revision":"18446744073709551615"`)) {
				t.Fatalf("wire JSON = %s, want decimal expected_revision", raw)
			}
		})
	}
}

func TestBotWirePreservesModelSelector(t *testing.T) {
	want := bot.Bot{
		ID: "bot-1", SessionID: "bot-chat-1", Revision: 3,
		Config:        bot.Config{Name: "Ada", Model: "openai@api-cn/openai/gpt-5.4"},
		ModelSelector: "openai@api-cn/gpt-5.4",
	}
	validateWireValue(t, "Bot", want)
	raw, err := Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"model_selector":"openai@api-cn/gpt-5.4"`)) {
		t.Fatalf("wire JSON = %s, want the public model selector", raw)
	}
	var got bot.Bot
	if err := Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
	if got.Config.Model != want.Config.Model || got.ModelSelector == got.Config.Model {
		t.Fatalf("wire collapsed display onto durable identity: %#v", got)
	}
}

func TestBotListWirePreservesModelSelector(t *testing.T) {
	want := []bot.Bot{{ID: "bot-1", SessionID: "bot-chat-1", Revision: 4, Config: bot.Config{Name: "Ada", Model: "mimo"}, ModelSelector: "xiaomi/mimo"}}
	validateWireValue(t, "BotList", want)
	raw, err := Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"model_selector":"xiaomi/mimo"`)) {
		t.Fatalf("wire JSON = %s, want the public model selector", raw)
	}
	var got []bot.Bot
	if err := Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
}

func TestBotWireRejectsNumericRevision(t *testing.T) {
	var got bot.Bot
	if err := Unmarshal([]byte(`{"id":"bot-1","session_id":"bot-chat-1","revision":7,"config":{"name":"Ada"}}`), &got); err == nil {
		t.Fatal("numeric revision was accepted as a uint64 decimal string")
	}
}

package acp

import (
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/client"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestACPEnvelopeFromUpdatePassesThroughUsageUpdate(t *testing.T) {
	t.Parallel()

	env := acpEnvelopeFromUpdate(client.UpdateEnvelope{
		SessionID: "remote-1",
		Update: client.UsageUpdate{
			SessionUpdate: client.UpdateUsage,
			Size:          200000,
			Used:          42000,
			Cost:          &client.UsageCost{Total: 0.47, Currency: "USD"},
			Meta:          map[string]any{"vendor": map[string]any{"trace": "abc"}},
		},
	}, nil, nil)
	if env == nil {
		t.Fatal("acpEnvelopeFromUpdate() = nil, want usage_update envelope")
	}
	update, ok := env.Update.(schema.UsageUpdate)
	if !ok {
		t.Fatalf("Update = %T, want schema.UsageUpdate", env.Update)
	}
	if update.Size != 200000 || update.Used != 42000 {
		t.Fatalf("usage update = %#v, want size/used preserved", update)
	}
	if update.Cost == nil || update.Cost.Total != 0.47 || update.Cost.Currency != "USD" {
		t.Fatalf("usage cost = %#v, want total/currency", update.Cost)
	}
}

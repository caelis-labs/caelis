package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/internal/acpdraft"
)

func TestDraftAlpha13RejectsWholeBatch(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	if err := right.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	client, err := NewStreamClient(left, left, Config{ACPDraftSchema: acpdraft.SchemaTag})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	reader := bufio.NewReader(right)
	for _, batch := range []string{`[]`, `[{"jsonrpc":"2.0","id":1,"method":"_vendor/work","params":{}}]`, `[{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"forbidden","update":{"sessionUpdate":"tool_call_update","toolCallId":"tool"}}}]`} {
		if _, err := fmt.Fprintln(right, batch); err != nil {
			t.Fatal(err)
		}
		line, err := reader.ReadBytes('\n')
		var response struct {
			ID    json.RawMessage   `json:"id"`
			Error *acp.RequestError `json:"error"`
		}
		if err != nil || json.Unmarshal(line, &response) != nil || response.Error == nil || response.Error.Code != -32600 || string(response.ID) != "null" {
			t.Fatalf("whole batch rejection: %s, %v", line, err)
		}
	}
	client.draft.mu.Lock()
	defer client.draft.mu.Unlock()
	if len(client.draft.sessions) != 0 {
		t.Fatal("batch notification allocated Session state")
	}
}

func TestDraftAlpha13EmptyPromptRejectedBeforeAdmission(t *testing.T) {
	agent := &draftTestAgent{}
	client, ctx, _ := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	for _, prompt := range [][]json.RawMessage{nil, {}} {
		if call, err := client.PreparePromptParts("session", prompt, nil); err == nil {
			call.Abandon()
			t.Fatal("empty prompt was prepared")
		}
		if _, err := client.SteerPartsWithAbort(ctx, "session", prompt, nil, nil); err == nil {
			t.Fatal("empty steering was accepted")
		}
	}
	client.draft.mu.Lock()
	defer client.draft.mu.Unlock()
	if len(client.draft.sessions) != 0 {
		t.Fatal("empty prompt allocated Session state")
	}
}

func TestDraftAlpha13UsageCostPatchesMaterializeSnapshots(t *testing.T) {
	agent := &draftTestAgent{}
	client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		session, cost string
		wantCost      bool
	}{
		{"session", `,"cost":{"amount":1.5,"currency":"credits","_meta":{"n":9007199254740993}}`, true},
		{"other", "", false},
		{"session", "", true},
		{"session", `,"cost":false`, true},
		{"session", `,"cost":null`, false},
		{"session", "", false},
	} {
		if err := agent.update(ctx, step.session, json.RawMessage(`{"sessionUpdate":"usage_update","size":100,"used":2`+step.cost+`}`)); err != nil {
			t.Fatal(err)
		}
		update := requireDraftUpdate(t, ctx, updates).Update.(UsageUpdate)
		if (update.Cost != nil) != step.wantCost {
			t.Fatalf("session %s cost patch %s: %+v", step.session, step.cost, update)
		}
		if step.wantCost {
			if update.Cost.Amount != 1.5 || update.Cost.Currency != "credits" || string(update.Cost.Meta["n"]) != "9007199254740993" {
				t.Fatalf("cost or metadata changed: %+v", update.Cost)
			}
			// A consumer cannot mutate the retained snapshot for a later patch.
			update.Cost.Amount = 99
			update.Cost.Meta["n"][0] = '0'
		}
	}
	if err := client.draft.failureCause(); err != nil {
		t.Fatal(err)
	}
}

func TestDraftAlpha13ConfigDropsEmptySelectsAndKeepsExplicitClear(t *testing.T) {
	agent := &draftTestAgent{}
	client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	for _, options := range []string{
		`[{"type":"select","configId":"empty","name":"Empty","currentValue":"x","options":[]},{"type":"select","configId":"mode","name":"Mode","currentValue":"ask","options":[{"groupId":"g","name":"Group","options":[null,{}, {"name":"Ask","value":"ask"}]}]}]`,
		`[]`,
	} {
		if err := agent.update(ctx, "session", json.RawMessage(`{"sessionUpdate":"config_option_update","configOptions":`+options+`}`)); err != nil {
			t.Fatal(err)
		}
		update := requireDraftUpdate(t, ctx, updates).Update.(ConfigOptionUpdate)
		if options == `[]` {
			if update.ConfigOptions == nil || len(update.ConfigOptions) != 0 {
				t.Fatalf("clear lost: %+v", update)
			}
		} else if len(update.ConfigOptions) != 1 || update.ConfigOptions[0].ID != "mode" {
			t.Fatalf("select recovery: %+v", update)
		}
	}
}

func TestDraftAlpha13BackgroundDoesNotOwnForegroundSettlement(t *testing.T) {
	agent := &draftTestAgent{}
	agent.prompt = func(ctx context.Context, _ v2.PromptRequest) (v2.PromptResponse, error) {
		if err := agent.state(ctx, "session", "foreground", "running", ""); err != nil {
			return v2.PromptResponse{}, err
		}
		if err := agent.echo(ctx, "session", "input"); err != nil {
			return v2.PromptResponse{}, err
		}
		return v2.PromptResponse{MessageId: "input", Meta: acpdraft.ActivityMeta("foreground")}, nil
	}
	client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := client.Prompt(ctx, "session", "input", nil); result <- err }()
	requireDraftUpdate(t, ctx, updates)
	background := `,"background":[{"type":"tool_call","toolCallId":"background-tool"},{"type":"terminal","terminalId":"background-terminal"},{"type":"future","n":9007199254740993}]`
	for _, state := range []string{"running", "requires_action"} {
		if err := agent.update(ctx, "session", json.RawMessage(`{"sessionUpdate":"state_update","state":"`+state+`","_meta":{"caelis/activity_id":"foreground"}`+background+`}`)); err != nil {
			t.Fatal(err)
		}
		assertDraftPending(t, result)
	}
	if err := agent.update(ctx, "session", json.RawMessage(`{"sessionUpdate":"state_update","state":"idle","stopReason":"end_turn","_meta":{"caelis/activity_id":"foreground"}`+background+`}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestDraftAlpha13CostUsesExistingCacheBudget(t *testing.T) {
	d := newDraftClient(nil)
	session, err := d.ensureSessionLocked("session")
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"sessionUpdate":"usage_update","size":100,"used":2,"cost":{"amount":1,"currency":"credits"}}`)
	if _, err := d.decodeUsage("session", raw); err != nil {
		t.Fatal(err)
	}
	retained := session.costCacheBytes
	if retained == 0 {
		t.Fatal("cost was not charged to the cache")
	}
	before := d.cacheBytes
	if _, err := d.decodeUsage("session", json.RawMessage(`{"sessionUpdate":"usage_update","size":100,"used":2,"cost":null}`)); err != nil {
		t.Fatal(err)
	}
	if d.cacheBytes != before-retained || session.costCacheBytes != 0 || session.cost != nil {
		t.Fatal("cost clear retained cache bytes")
	}
	session.cacheBytes = draftMaxSessionCacheBytes
	if _, err := d.decodeUsage("session", raw); err == nil {
		t.Fatal("cost bypassed the Session cache limit")
	}
}

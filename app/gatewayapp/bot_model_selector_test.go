package gatewayapp

import (
	"context"
	"fmt"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
	"github.com/caelis-labs/caelis/control/modelconfig"
)

// stubBotReader is the durable Bot read the projector decorates.
type stubBotReader struct{ bots []bot.Bot }

func (s stubBotReader) ListBots(context.Context, string) ([]bot.Bot, error) {
	return append([]bot.Bot(nil), s.bots...), nil
}

func (s stubBotReader) GetBot(_ context.Context, id string) (bot.Bot, error) {
	for _, value := range s.bots {
		if value.ID == id {
			return value, nil
		}
	}
	return bot.Bot{}, fmt.Errorf("bot %q not found", id)
}

// TestBotModelSelectorProjectorPublishesPublicSelectors proves that a durable
// Bot read gains Control's catalog-resolved public selector for the default
// endpoint, a non-default endpoint, and a custom alias, while the durable
// Config.Model identity is preserved unchanged.
func TestBotModelSelectorProjectorPublishesPublicSelectors(t *testing.T) {
	configs := []ModelConfig{
		{Provider: "openai-compatible", Model: "observation", EndpointID: "default", BaseURL: "http://127.0.0.1:1"},
		{Provider: "openai-compatible", Model: "observation", EndpointID: "api-cn", BaseURL: "http://127.0.0.1:2"},
		{Provider: "openai-compatible", Model: "observation", EndpointID: "api-cn2", BaseURL: "http://127.0.0.1:3", Alias: "custom-observation"},
	}
	lookup, err := newModelLookupFromDocument(AppConfig{Models: persistedModelConfig{Configs: configs}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var (
		bots []bot.Bot
		want []string
	)
	for i, raw := range configs {
		durable := modelconfig.NormalizeConfig(raw).ID
		bots = append(bots, bot.Bot{
			ID: fmt.Sprintf("bot-%d", i), SessionID: fmt.Sprintf("bot-chat-%d", i), Revision: uint64(i + 1),
			Config: bot.Config{Name: "Ada", Model: durable},
		})
		want = append(want, modelconfig.PublicSelector(modelconfig.NormalizeConfig(raw)))
	}
	if want[0] != "openai-compatible/observation" {
		t.Fatalf("default selector = %q", want[0])
	}
	if want[1] != "openai-compatible@api-cn/observation" {
		t.Fatalf("non-default selector = %q", want[1])
	}
	if want[2] != modelconfig.NormalizeConfig(configs[2]).ID || want[2] == "openai-compatible/observation" {
		t.Fatalf("custom-alias selector = %q", want[2])
	}

	projector := botModelSelectorProjector{reader: stubBotReader{bots: bots}, lookup: lookup}
	listed, err := projector.ListBots(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != len(bots) {
		t.Fatalf("listed %d Bots, want %d", len(listed), len(bots))
	}
	for i, value := range listed {
		if value.Config.Model != bots[i].Config.Model {
			t.Fatalf("Bot %d durable model changed: %q, want %q", i, value.Config.Model, bots[i].Config.Model)
		}
		if value.ModelSelector != want[i] {
			t.Fatalf("Bot %d selector = %q, want %q", i, value.ModelSelector, want[i])
		}
		// A standard alias publishes a short selector distinct from the durable
		// ID; a custom alias intentionally keeps its fully qualified config ID.
		if i < 2 && value.ModelSelector == value.Config.Model {
			t.Fatalf("Bot %d standard selector collapsed into the durable ID: %q", i, value.ModelSelector)
		}
	}
	one, err := projector.GetBot(context.Background(), bots[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	if one.ModelSelector != want[2] || one.Config.Model != bots[2].Config.Model {
		t.Fatalf("GetBot = %+v, want selector %q preserving %q", one, want[2], bots[2].Config.Model)
	}
}

// TestBotReadProjectsPublicSelectorFromLiveCatalog proves the projection runs
// on a real Host Bot read and tracks the current catalog: a Bot created against
// the default model reports that model's public selector, and after a second
// model is connected and selected the same read reports the new selector while
// the durable Config.Model identity is preserved.
func TestBotReadProjectsPublicSelectorFromLiveCatalog(t *testing.T) {
	stack, err := newGatewayAppTestStack(t, Config{
		StoreDir: t.TempDir(), WorkspaceCWD: t.TempDir(),
		Model: ModelConfig{Provider: "ollama", API: providers.APIOllama, Model: "llama3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	principal := appserver.Principal{ID: "local-user"}

	created := createTestBot(t, stack, "selector-live-catalog", "Ada")
	if created.Config.Model == "" {
		t.Fatal("Bot created without a snapshotted default model")
	}
	if created.ModelSelector == "" || created.ModelSelector == created.Config.Model {
		t.Fatalf("default Bot read = %+v, want a distinct public selector", created)
	}

	profile, err := stack.connectTestModel(ModelConfig{Provider: "ollama", API: providers.APIOllama, Model: "late-model"})
	if err != nil {
		t.Fatal(err)
	}
	lateID := profile.Backend.Provider.ModelConfigID
	if lateID == created.Config.Model {
		t.Fatalf("second model reused the default identity %q", lateID)
	}
	config := created.Config
	config.Model = lateID
	result, err := stack.Bots().UpdateBot(context.Background(), principal, appserver.UpdateBotRequest{
		WriteBase: appserver.WriteBase{OperationID: "selector-select-late", SessionID: created.SessionID, ExpectedRevision: &created.Revision},
		BotID:     created.ID, Config: config,
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("select late model: %+v, %v", result, err)
	}

	value, err := stack.Bots().GetBot(context.Background(), principal, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantSelector := modelconfig.PublicSelector(modelconfig.NormalizeConfig(ModelConfig{Provider: "ollama", API: providers.APIOllama, Model: "late-model"}))
	if value.Config.Model != lateID {
		t.Fatalf("durable model = %q, want %q", value.Config.Model, lateID)
	}
	if value.ModelSelector != wantSelector || value.ModelSelector == value.Config.Model {
		t.Fatalf("selector = %q, want distinct %q", value.ModelSelector, wantSelector)
	}
	listed, err := stack.Bots().ListBots(context.Background(), principal)
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListBots = %+v, %v", listed, err)
	}
	if listed[0].ModelSelector != wantSelector || listed[0].Config.Model != lateID {
		t.Fatalf("listed = %+v, want selector %q preserving %q", listed[0], wantSelector, lateID)
	}
}

func TestBotModelSelectorProjectorLeavesUnknownModelEmpty(t *testing.T) {
	lookup, err := newModelLookupFromDocument(AppConfig{Models: persistedModelConfig{Configs: []ModelConfig{
		{Provider: "openai-compatible", Model: "observation", EndpointID: "default", BaseURL: "http://127.0.0.1:1"},
	}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	const removed = "openai-compatible@removed/custom/observation"
	projector := botModelSelectorProjector{
		reader: stubBotReader{bots: []bot.Bot{{ID: "bot-1", SessionID: "bot-chat-1", Revision: 1, Config: bot.Config{Name: "Ada", Model: removed}}}},
		lookup: lookup,
	}
	value, err := projector.GetBot(context.Background(), "bot-1")
	if err != nil {
		t.Fatal(err)
	}
	if value.ModelSelector != "" {
		t.Fatalf("unknown model published selector %q", value.ModelSelector)
	}
	if value.Config.Model != removed {
		t.Fatalf("unknown model lost its durable identity: %q", value.Config.Model)
	}
}

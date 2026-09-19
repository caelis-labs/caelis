package tuiapp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
	"github.com/caelis-labs/caelis/internal/gatewayapptest/localclient"
)

func TestProductBotModelSelectionPreservesConfiguration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("model settings must not call the provider")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer provider.Close()
	workspace := t.TempDir()
	clients, closeHost, err := localclient.New(ctx, t.TempDir(), workspace, provider.URL, provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := closeHost(); err != nil {
			t.Error(err)
		}
	}()
	status, err := clients.Status.SessionStatus(ctx, appserver.StatusRequest{WorkspaceKey: "workspace", CWD: workspace})
	if err != nil {
		t.Fatal(err)
	}
	result, err := clients.Configuration.ConnectModel(ctx, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "connect-second", ExpectedRevision: &status.Configuration.Revision},
		Config: appserver.ConnectConfig{
			Provider: "openai-compatible", Model: "zz-model", BaseURL: provider.URL, APIKey: "test-token",
			ContextWindowTokens: 128000, MaxOutputTokens: 1024, ReasoningLevels: []string{"none", "low", "high"},
		},
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("connect: %+v, %v", result, err)
	}
	value := createProductBot(t, ctx, clients.Bots, "Ada")
	config := value.Config
	config.Model, config.Effort = "openai-compatible/zz-model", "high"
	result, err = clients.Bots.UpdateBot(ctx, appserver.UpdateBotRequest{
		WriteBase: appserver.WriteBase{OperationID: "select-model", SessionID: value.SessionID, ExpectedRevision: &value.Revision},
		BotID:     value.ID, Config: config,
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("select model: %+v, %v", result, err)
	}
	value, err = clients.Bots.GetBot(ctx, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if value.Config.Model == "" || value.ModelSelector == "" || value.ModelSelector == value.Config.Model {
		t.Fatalf("connected Bot read = %+v, want a public selector distinct from the durable model", value)
	}
	// The public selector must not replace the durable identity the Host resolves.
	adapter, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{
		Surface: "cli-bot", WorkspaceKey: "workspace", WorkspaceDir: workspace, RequireExistingSession: true,
		Sessions: clients.Sessions, Participants: clients.Participants, Status: clients.Status,
		Configuration: clients.Configuration, Agents: clients.Agents, Completion: clients.Completion, Plugins: clients.Plugins,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if _, err := adapter.ResumeSession(ctx, value.SessionID); err != nil {
		t.Fatal(err)
	}
	sender := &ProgramSender{}
	defer sender.Close()
	model := NewModel(ConfigFromControlService(adapter, sender, Config{Context: ctx, Bot: &BotSurface{Client: clients.Bots}}))
	model.bot.active, model.bot.hasActive = value, true
	if got := model.botModelText(); got != value.ModelSelector {
		t.Fatalf("Bot footer model = %q, want the public selector %q", got, value.ModelSelector)
	}
	if got, _ := model.activeBot(); got.Config.Model != value.Config.Model {
		t.Fatalf("footer display changed the durable model identity: %q", got.Config.Model)
	}
	candidates, err := model.cfg.SlashArgComplete(ctx, "model", "", 200)
	if err != nil {
		t.Fatal(err)
	}
	index := -1
	for i, candidate := range candidates {
		if candidate.ModelConfigID == value.Config.Model {
			index = i
			if candidate.Value == candidate.ModelConfigID {
				t.Fatal("fixture must distinguish public selector from durable model ID")
			}
		}
	}
	if index <= 0 {
		t.Fatalf("current model must not be the first catalog entry: %+v", candidates)
	}
	for _, modelOnly := range []bool{true, false} {
		runConnectTestCmd(model, model.startBotSettingsFlow(modelOnly))
		if modelOnly {
			if model.slashArgCandidates[model.slashArgIndex].ModelConfigID != value.Config.Model {
				t.Fatal("current model not selected")
			}
			runConnectTestCmd(model, model.acceptBotSettings())
		} else {
			model.wizardOverlay.fields[1].value = "new description"
			runConnectTestCmd(model, model.saveBotSettings())
		}
		got, err := clients.Bots.GetBot(ctx, value.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := value.Config
		if !modelOnly {
			want.Description = "new description"
		}
		if got.Config != want || (modelOnly && got.Revision != value.Revision) {
			t.Fatalf("modelOnly=%v: settings = %+v, want config %+v (unchanged revision %d)", modelOnly, got, want, value.Revision)
		}
	}
}

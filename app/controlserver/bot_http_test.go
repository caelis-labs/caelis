package controlserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/bot"
)

type focusedBotService struct {
	appserver.BotService
	principal appserver.Principal
	listCalls int
	getID     string
	created   appserver.CreateBotRequest
	updated   appserver.UpdateBotRequest
}

func (s *focusedBotService) ListBots(_ context.Context, principal appserver.Principal) ([]bot.Bot, error) {
	s.principal = principal
	s.listCalls++
	return []bot.Bot{{ID: "bot-1", SessionID: "bot-chat-1", Revision: 4, Config: bot.Config{Name: "Ada"}}}, nil
}

func (s *focusedBotService) GetBot(_ context.Context, principal appserver.Principal, botID string) (bot.Bot, error) {
	s.principal = principal
	s.getID = botID
	return bot.Bot{ID: botID, SessionID: "bot-chat-1", Revision: 4, Config: bot.Config{Name: "Ada"}}, nil
}

func (s *focusedBotService) CreateBot(_ context.Context, principal appserver.Principal, req appserver.CreateBotRequest) (appserver.CommandResult, error) {
	s.principal = principal
	s.created = req
	return appserver.CommandResult{
		OperationID: req.OperationID, Outcome: appserver.OutcomeCommitted, SessionID: "bot-chat-1",
		Resource: &appserver.CommandResource{Kind: appserver.CommandResourceBot, Ref: "bot-1"},
	}, nil
}

func (s *focusedBotService) UpdateBot(_ context.Context, principal appserver.Principal, req appserver.UpdateBotRequest) (appserver.CommandResult, error) {
	s.principal = principal
	s.updated = req
	return appserver.CommandResult{OperationID: req.OperationID, SessionID: req.SessionID, Outcome: appserver.OutcomeCommitted, Revision: 5}, nil
}

func TestBotClientsRoundTripThroughHTTPAppServer(t *testing.T) {
	bots := &focusedBotService{}
	services := testAppServerServices(&fakeService{}, staticStatusService{})
	services.Bots = bots
	server, err := New(HandlerConfig{
		Services: services, Authenticator: testAuthenticator(),
		AllowedHosts: []string{"127.0.0.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := httpclient.New(httpclient.Config{
		BaseURL: "http://127.0.0.1", BearerToken: "test-token",
		HTTPClient:    &http.Client{Transport: controlHandlerRoundTripper{handler: server}},
		Compatibility: appserver.CurrentCompatibility(),
	})
	if err != nil {
		t.Fatal(err)
	}

	listed, err := client.ListBots(context.Background())
	if err != nil || len(listed) != 1 || listed[0].ID != "bot-1" || listed[0].Revision != 4 || listed[0].Config.Name != "Ada" {
		t.Fatalf("ListBots() = %#v, %v", listed, err)
	}
	one, err := client.GetBot(context.Background(), "bot-1")
	if err != nil || one.ID != "bot-1" || one.SessionID != "bot-chat-1" || one.Revision != 4 {
		t.Fatalf("GetBot() = %#v, %v", one, err)
	}

	created, err := client.CreateBot(context.Background(), appserver.CreateBotRequest{
		WriteBase: appserver.WriteBase{OperationID: "bot-create-1"},
		Config:    bot.Config{Name: "Ada", Model: "mimo"},
	})
	if err != nil || created.Resource == nil || created.Resource.Kind != appserver.CommandResourceBot ||
		created.Resource.Ref != "bot-1" || created.SessionID != "bot-chat-1" {
		t.Fatalf("CreateBot() = %#v, %v", created, err)
	}

	revision := uint64(4)
	updated, err := client.UpdateBot(context.Background(), appserver.UpdateBotRequest{
		WriteBase: appserver.WriteBase{OperationID: "bot-update-1", SessionID: "bot-chat-1", ExpectedRevision: &revision},
		BotID:     "bot-1", Config: bot.Config{Name: "Ada Lovelace", Effort: "high"},
	})
	if err != nil || updated.Outcome != appserver.OutcomeCommitted || updated.Revision != 5 {
		t.Fatalf("UpdateBot() = %#v, %v", updated, err)
	}

	if bots.principal.ID != "trusted-owner" || bots.getID != "bot-1" || bots.listCalls != 1 {
		t.Fatalf("bot service observation = %#v calls=%d id=%q", bots.principal, bots.listCalls, bots.getID)
	}
	if bots.created.OperationID != "bot-create-1" || bots.created.SessionID != "" || bots.created.Config.Name != "Ada" {
		t.Fatalf("create request = %#v", bots.created)
	}
	if bots.updated.SessionID != "bot-chat-1" || bots.updated.BotID != "bot-1" ||
		bots.updated.ExpectedRevision == nil || *bots.updated.ExpectedRevision != revision {
		t.Fatalf("update request = %#v", bots.updated)
	}

	// CreateBot is Host-scoped: a Session address in the body is rejected before
	// the command service is reached.
	createCalls := bots.created.OperationID
	forgedCreate := httptest.NewRequest(
		http.MethodPost,
		"http://127.0.0.1"+apiPrefix+"/bots/create",
		strings.NewReader(`{"session_id":"bot-chat-1","config":{"name":"Ada"}}`),
	)
	forgedCreate.Header.Set("Authorization", "Bearer test-token")
	forgedCreate.Header.Set("Content-Type", "application/json")
	forgedCreate.Header.Set("Idempotency-Key", "bot-create-forged")
	forgedResponse := httptest.NewRecorder()
	server.ServeHTTP(forgedResponse, forgedCreate)
	if forgedResponse.Code != http.StatusBadRequest || bots.created.OperationID != createCalls {
		t.Fatalf("forged Host create status/reach = %d/%q, want 400/no-call", forgedResponse.Code, bots.created.OperationID)
	}

	// A missing Idempotency-Key is rejected before the command service runs.
	missingKey := httptest.NewRequest(
		http.MethodPost,
		"http://127.0.0.1"+apiPrefix+"/sessions/bot-chat-1/bots/update",
		strings.NewReader(`{"bot_id":"bot-1","config":{"name":"Ada"}}`),
	)
	missingKey.Header.Set("Authorization", "Bearer test-token")
	missingKey.Header.Set("Content-Type", "application/json")
	missingResponse := httptest.NewRecorder()
	server.ServeHTTP(missingResponse, missingKey)
	if missingResponse.Code != http.StatusBadRequest {
		t.Fatalf("missing Idempotency-Key status = %d, want 400", missingResponse.Code)
	}
}

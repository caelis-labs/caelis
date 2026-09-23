package controlserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRetiredBotRoutesDoNotDispatch(t *testing.T) {
	server := newTestServer(t, &fakeService{}, 0)
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/bots"},
		{http.MethodGet, "/bots/bot-1"},
		{http.MethodPost, "/bots/create"},
		{http.MethodPost, "/sessions/session-1/bots/update"},
		{http.MethodPost, "/bots/bot-1/work/create"},
		{http.MethodGet, "/bots/bot-1/client/actions/events"},
		{http.MethodPost, "/bots/bot-1/client/reminders/fire"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			request := httptest.NewRequest(route.method, apiPrefix+route.path, nil)
			authorizeTestRequest(request)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusNotFound {
				t.Fatalf("retired route returned HTTP %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

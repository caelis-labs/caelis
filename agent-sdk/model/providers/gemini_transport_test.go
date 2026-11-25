package providers

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestGeminiDefaultTransportReusesConnection(t *testing.T) {
	const wire = `{"candidates":[{"content":{"role":"model","parts":[{"text":"fixture answer"}]},"finishReason":"STOP"}]}`
	peers := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.Copy(io.Discard, req.Body)
		peers <- req.RemoteAddr
		if strings.Contains(req.URL.Path, ":streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: %s\n\n", wire)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, wire)
	}))
	defer server.Close()
	llm, err := New(Config{API: model.APIGemini, Model: "gemini-fixture", BaseURL: server.URL, Auth: AuthConfig{Token: "fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, stream := range []bool{false, true} {
		var answer string
		for event, err := range model.Generate(t.Context(), llm, &model.Request{Stream: stream, Messages: []model.Message{model.NewTextMessage(model.RoleUser, "fixture")}}) {
			if err != nil {
				t.Fatal(err)
			}
			if event.Response != nil {
				answer = event.Message.TextContent()
			}
		}
		if answer != "fixture answer" {
			t.Fatalf("stream=%v answer=%q", stream, answer)
		}
	}
	search, err := llm.(model.WebSearcher).SearchWeb(t.Context(), model.WebSearchRequest{Query: "fixture"})
	if err != nil || search.Answer != "fixture answer" {
		t.Fatalf("search=%+v err=%v", search, err)
	}
	first, stream, searchPeer := <-peers, <-peers, <-peers
	if first != stream || first != searchPeer {
		t.Fatalf("connection peer changed across requests: %q, %q, %q", first, stream, searchPeer)
	}
}

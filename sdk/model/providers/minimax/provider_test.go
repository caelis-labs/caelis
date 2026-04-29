package minimax

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
)

func TestGenerateStreamingEmitsStartBlockText(t *testing.T) {
	t.Parallel()

	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" && r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer compat-token" {
			t.Fatalf("expected auth header, got %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_stream\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"MiniMax-M2\",\"content\":[],\"stop_reason\":\"\",\"stop_sequence\":\"\",\"usage\":{\"input_tokens\":11,\"output_tokens\":0}}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"MiniMax streaming \"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"works.\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = fmt.Fprint(w, "event: message_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":\"\"},\"usage\":{\"input_tokens\":11,\"output_tokens\":12}}\n\n")
		_, _ = fmt.Fprint(w, "event: message_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	llm := New(Config{
		Model:      "MiniMax-M2",
		BaseURL:    server.URL,
		APIKey:     "compat-token",
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	})

	var chunks []string
	var final *sdkmodel.Response
	for event, err := range llm.Generate(context.Background(), &sdkmodel.Request{
		Messages: []sdkmodel.Message{sdkmodel.NewTextMessage(sdkmodel.RoleUser, "hello")},
		Stream:   true,
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		if event == nil {
			continue
		}
		if event.PartDelta != nil && event.PartDelta.Kind == sdkmodel.PartKindText {
			chunks = append(chunks, event.PartDelta.TextDelta)
		}
		if event.Response != nil && event.TurnComplete {
			final = event.Response
		}
	}

	if got := strings.Join(chunks, ""); got != "MiniMax streaming works." {
		t.Fatalf("joined text = %q", got)
	}
	if final == nil || final.Message.TextContent() != "MiniMax streaming works." {
		t.Fatalf("unexpected final response %+v", final)
	}
}

func TestGenerateNonStreaming_DefaultDoesNotApplyRequestTimeout(t *testing.T) {
	t.Parallel()

	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" && r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_nonstream","type":"message","role":"assistant","model":"MiniMax-M2","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":"","usage":{"input_tokens":11,"output_tokens":3}}`)
	}))
	defer server.Close()

	llm := New(Config{
		Model:      "MiniMax-M2",
		BaseURL:    server.URL,
		APIKey:     "compat-token",
		HTTPClient: server.Client(),
	})

	var (
		gotErr    error
		finalText string
	)
	for event, err := range llm.Generate(context.Background(), &sdkmodel.Request{
		Messages: []sdkmodel.Message{sdkmodel.NewTextMessage(sdkmodel.RoleUser, "hello")},
		Stream:   false,
	}) {
		if err != nil {
			gotErr = err
			continue
		}
		if event != nil && event.Response != nil && event.TurnComplete {
			finalText = event.Response.Message.TextContent()
		}
	}
	if gotErr != nil {
		t.Fatalf("expected no timeout error, got %v", gotErr)
	}
	if finalText != "ok" {
		t.Fatalf("unexpected final text %q", finalText)
	}
}

package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

func TestRuntimeGeminiStreamCitationReplay(t *testing.T) {
	const wire = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"你好"}]},"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://first.test","title":"第一"}}],"groundingSupports":[{"segment":{"endIndex":6},"groundingChunkIndices":[0]}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":" "}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"café 世界"}]},"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://second.test","title":"Café"}}],"groundingSupports":[{"segment":{"endIndex":5},"groundingChunkIndices":[0,1]},{"segment":{"startIndex":6,"endIndex":12},"groundingChunkIndices":[2]}]}}]}

data: {"candidates":[{"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://third.test","title":"世界"}}]},"finishReason":"STOP"}]}

`
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "citations", UserID: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var providerPrefixes []json.RawMessage
	llm, err := providers.New(providers.Config{API: model.APIGemini, Model: "fixture", BaseURL: "http://provider.invalid", Auth: providers.AuthConfig{Token: "fixture"},
		HTTPClient: &http.Client{Transport: compactionTerminalTransport(func(req *http.Request) (*http.Response, error) {
			payload := wire
			if calls.Add(1) > 1 {
				var body struct{ Contents []json.RawMessage }
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					return nil, err
				}
				if len(body.Contents) < 2 || !strings.Contains(string(body.Contents[1]), "你好 café 世界") {
					return nil, fmt.Errorf("missing merged assistant text on replay: %s", body.Contents)
				}
				providerPrefixes = append(providerPrefixes, body.Contents[1])
				payload = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"continued"}]},"finishReason":"STOP"}]}` + "\n\n"
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(payload)), Request: req}, nil
		})}})
	if err != nil {
		t.Fatal(err)
	}
	makeRuntime := func(store session.ManagedService) *Runtime {
		t.Helper()
		rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
		if err != nil {
			t.Fatal(err)
		}
		return rt
	}
	var requests [][]model.Message
	ctx := model.WithInvocationStart(t.Context(), func(_ context.Context, _ model.Invocation, request *model.Request) error {
		requests = append(requests, model.CloneMessages(request.Messages))
		return nil
	})
	runTurn := func(rt *Runtime, input string) {
		t.Helper()
		stream := true
		run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: input,
			AgentSpec: agent.AgentSpec{Model: llm, Request: agent.ModelRequestOptions{Stream: &stream}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := run.Handle.WaitCompletion(ctx); err != nil {
			t.Fatal(err)
		}
	}
	rt := makeRuntime(store)
	runTurn(rt, "produce citations")
	runTurn(rt, "continue before reopen")
	runTurn(makeRuntime(sessionfile.NewStore(sessionfile.Config{RootDir: root})), "continue after reopen")
	first := model.CitationSource{RefID: "grounding-0", URL: "https://first.test", Title: "第一"}
	second := model.CitationSource{RefID: "grounding-1", URL: "https://second.test", Title: "Café"}
	third := model.CitationSource{RefID: "grounding-2", URL: "https://third.test", Title: "世界"}
	want := model.NewTextPartWithCitations("你好 café 世界", []model.Citation{
		{StartIndex: 0, EndIndex: 6, Sources: []model.CitationSource{first}},
		{StartIndex: 7, EndIndex: 12, Sources: []model.CitationSource{first, second}},
		{StartIndex: 13, EndIndex: 19, Sources: []model.CitationSource{third}},
	})
	if len(requests) != 3 || len(requests[1]) < 2 || len(requests[2]) < len(requests[1]) {
		t.Fatalf("expected three actual requests with live and reopened context: %+v", requests)
	}
	if !reflect.DeepEqual(requests[1][1].Parts, []model.Part{want}) {
		got, _ := json.Marshal(requests[1][1])
		t.Fatalf("live assistant message lost text or citations: %s", got)
	}
	if !reflect.DeepEqual(requests[1], requests[2][:len(requests[1])]) || len(providerPrefixes) != 2 || string(providerPrefixes[0]) != string(providerPrefixes[1]) {
		t.Fatal("model context or provider wire prefix changed after file Store reopen")
	}
}

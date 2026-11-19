package gatewayapp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// Live acceptance consumes only an explicitly provided private profile. It never
// reads or edits production configuration. Its policy allows only these exact
// read operations, including when a model proposes a different tool.
type liveMCPWire struct {
	base   http.RoundTripper
	mu     sync.Mutex
	hashes []string
	models map[string]int
}

func (w *liveMCPWire) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		var fields map[string]json.RawMessage
		if json.Unmarshal(body, &fields) == nil {
			raw := fields["tools"]
			if bytes.Contains(raw, []byte(`"mcp_call"`)) {
				sum := sha256.Sum256(raw)
				w.mu.Lock()
				w.hashes = append(w.hashes, hex.EncodeToString(sum[:]))
				var requestedModel string
				_ = json.Unmarshal(fields["model"], &requestedModel)
				if w.models == nil {
					w.models = map[string]int{}
				}
				w.models[requestedModel]++
				w.mu.Unlock()
			}
		}
	}
	return w.base.RoundTrip(r)
}

type liveMCPTrace struct {
	model.LLM
	root  string
	mu    sync.Mutex
	calls int
}

func (m *liveMCPTrace) Capabilities() model.Capabilities {
	caps, _ := model.CapabilitiesOf(m.LLM)
	return caps
}
func (m *liveMCPTrace) Generate(ctx context.Context, r *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.mu.Lock()
	m.calls++
	id := m.calls
	m.mu.Unlock()
	raw, _ := json.Marshal(r)
	_ = os.WriteFile(filepath.Join(m.root, fmt.Sprintf("model-request-%03d.json", id)), raw, 0600)
	return func(yield func(*model.StreamEvent, error) bool) {
		f, err := os.OpenFile(filepath.Join(m.root, fmt.Sprintf("model-stream-%03d.jsonl", id)), os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			yield(nil, err)
			return
		}
		defer f.Close()
		enc := json.NewEncoder(f)
		for e, err := range m.LLM.Generate(ctx, r) {
			if e != nil {
				_ = enc.Encode(e)
			}
			if err != nil {
				_ = enc.Encode(map[string]any{"error": err.Error()})
			}
			if !yield(e, err) {
				return
			}
		}
	}
}
func writeLiveJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal("private evidence write failed")
	}
}

type liveApprovalFunc func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error)

func (f liveApprovalFunc) RequestApproval(ctx context.Context, r agent.ApprovalRequest) (agent.ApprovalResponse, error) {
	return f(ctx, r)
}

func (m *liveMCPTrace) ContextWindowTokens() int {
	if source, ok := m.LLM.(interface{ ContextWindowTokens() int }); ok {
		return source.ContextWindowTokens()
	}
	return 0
}

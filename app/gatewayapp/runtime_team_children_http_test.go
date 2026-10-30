package gatewayapp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
)

type teamChildProvider struct {
	mu          sync.Mutex
	role, label string
	models      []string
}

func (p *teamChildProvider) RoundTrip(req *http.Request) (*http.Response, error) {
	defer req.Body.Close()
	var body struct {
		Model    string
		Messages []struct{ Role string }
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.models = append(p.models, body.Model)
	role, label := p.role, p.label
	p.mu.Unlock()
	if body.Model == "team-luna" || body.Model == "team-sol" {
		return reviewerHTTPStream(req, map[string]any{"role": "assistant", "content": "TEAM_CHILD_DONE"}, "stop")
	}
	if body.Model != "gpt-4.1" {
		return nil, fmt.Errorf("unexpected model %s", body.Model)
	}
	if len(body.Messages) > 0 && body.Messages[len(body.Messages)-1].Role == "tool" {
		return reviewerHTTPStream(req, map[string]any{"role": "assistant", "content": "TEAM_PARENT_DONE"}, "stop")
	}
	args, _ := json.Marshal(map[string]string{"agent": role, "handle": label, "prompt": "CHILD_TEAM_INPUT"})
	return reviewerHTTPStream(req, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "team-child-call-" + label, "index": 0, "type": "function", "function": map[string]any{"name": "StartThread", "arguments": string(args)}}}}, "tool_calls")
}

func TestRuntimeTeamApplicationChildrenUseNextBinding(t *testing.T) {
	for _, role := range []agentbinding.Handle{agentbinding.HandleBreeze, agentbinding.HandleOrbit, agentbinding.HandleZenith} {
		t.Run(string(role), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			root := t.TempDir()
			provider := &teamChildProvider{role: string(role)}
			host := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, provider)
			defer host.close(t)
			for _, name := range []string{"gpt-4.1", "team-luna", "team-sol"} {
				connectApplicationChildModel(t, ctx, host, name)
			}
			client, _ := connectSessionOwnerHTTP(t, ctx, host, "owner", filepath.Join(root, "owner.token"))
			sid := createApplicationHTTPSession(t, ctx, client, "team-parent")
			for round, name := range []string{"team-luna", "team-sol"} {
				label := fmt.Sprintf("leaf-%d", round)
				provider.mu.Lock()
				provider.label = label
				provider.mu.Unlock()
				bindRuntimeTeamHTTP(t, ctx, host, role, name, "bind-"+name)
				backgroundPrompt(t, ctx, client, sid, "spawn-"+name, "SPAWN_TEAM_CHILD", "user", "")
				child, taskID := applicationAttachedChild(t, ctx, client, sid, label)
				tasks, err := httpclient.NewTaskClient(client)
				if err != nil {
					t.Fatal(err)
				}
				for {
					result, err := tasks.List(ctx, taskstream.ListRequest{SessionID: sid})
					if err != nil {
						t.Fatal(err)
					}
					completed := false
					for _, entry := range result.Tasks {
						if entry.TaskID == taskID && entry.State == "completed" && !entry.Running {
							completed = true
						}
					}
					if completed {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatalf("child Task did not complete: %+v", result)
					case <-time.After(10 * time.Millisecond):
					}
				}
				waitApplicationHTTPIdle(t, ctx, client, child)
				waitApplicationHTTPIdle(t, ctx, client, sid)
			}
			provider.mu.Lock()
			defer provider.mu.Unlock()
			var children []string
			for _, name := range provider.models {
				if name != "gpt-4.1" {
					children = append(children, name)
				}
			}
			if len(children) != 2 || children[0] != "team-luna" || children[1] != "team-sol" {
				t.Fatalf("native child requests: %v", provider.models)
			}
		})
	}
}

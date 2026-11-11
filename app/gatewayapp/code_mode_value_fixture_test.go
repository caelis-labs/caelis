package gatewayapp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const valueDataDir = "testdata/code-mode/h1-value"

type valueRecord struct {
	RecordID string `json:"record_id"`
	IssueID  string `json:"issue_id"`
	Status   string `json:"status"`
	Severity string `json:"severity"`
	Owner    string `json:"owner"`
	Updated  string `json:"updated_at"`
	Evidence string `json:"evidence"`
}
type valueDataset struct {
	Classification string                   `json:"classification"`
	Batches        map[string][]valueRecord `json:"batches"`
}
type valueIssue struct {
	IssueID  string `json:"issue_id"`
	Severity string `json:"severity"`
	Updated  string `json:"updated_at"`
	BatchID  string `json:"batch_id"`
	RecordID string `json:"record_id"`
}
type valueOwner struct {
	Owner  string       `json:"owner"`
	Count  int          `json:"count"`
	Issues []valueIssue `json:"issues"`
}
type valueAnswer struct {
	Total  int          `json:"total_count"`
	Owners []valueOwner `json:"owners"`
}

func valueLoad(turn int) (valueDataset, error) {
	var d valueDataset
	raw, err := os.ReadFile(filepath.Join(valueDataDir, fmt.Sprintf("records-turn-%d.json", turn)))
	if err != nil {
		return d, err
	}
	err = json.Unmarshal(raw, &d)
	return d, err
}
func valueOracle(d valueDataset) valueAnswer {
	type latest struct {
		batch  string
		record valueRecord
	}
	chosen := map[string]latest{}
	for batch, records := range d.Batches {
		for _, r := range records {
			old, exists := chosen[r.IssueID]
			if !exists || r.Updated > old.record.Updated || (r.Updated == old.record.Updated && (batch > old.batch || (batch == old.batch && r.RecordID > old.record.RecordID))) {
				chosen[r.IssueID] = latest{batch, r}
			}
		}
	}
	groups := map[string][]valueIssue{}
	for _, item := range chosen {
		r := item.record
		if r.Status != "open" || (r.Severity != "critical" && r.Severity != "high") {
			continue
		}
		groups[r.Owner] = append(groups[r.Owner], valueIssue{r.IssueID, r.Severity, r.Updated, item.batch, r.RecordID})
	}
	out := valueAnswer{Owners: []valueOwner{}}
	var names []string
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		issues := groups[name]
		sort.Slice(issues, func(i, j int) bool { return issues[i].IssueID < issues[j].IssueID })
		out.Owners = append(out.Owners, valueOwner{name, len(issues), issues})
		out.Total += len(issues)
	}
	return out
}
func valueToolSchemas() map[string]map[string]any {
	return map[string]map[string]any{
		"list_batches": {"type": "object", "additionalProperties": false},
		"read_batch":   {"type": "object", "additionalProperties": false, "required": []string{"batch_id"}, "properties": map[string]any{"batch_id": map[string]any{"type": "string", "enum": []string{"batch-a", "batch-b", "batch-c"}}}},
	}
}

type valueFixture struct {
	mu         sync.Mutex
	data       valueDataset
	turn       int
	root       string
	calls      []map[string]any
	captureErr error
}

func (f *valueFixture) set(turn int) error {
	d, err := valueLoad(turn)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data = d
	f.turn = turn
	return nil
}
func (f *valueFixture) result(name string, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var data any
	batch := ""
	if name == "list_batches" {
		data = map[string]any{"batch_ids": []string{"batch-a", "batch-b", "batch-c"}}
	} else {
		var args struct {
			Batch string `json:"batch_id"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return nil, err
		}
		batch = args.Batch
		records, ok := f.data.Batches[batch]
		if !ok {
			return nil, fmt.Errorf("unknown batch")
		}
		data = map[string]any{"batch_id": batch, "records": records}
	}
	result := &mcpsdk.CallToolResult{StructuredContent: data, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "Synthetic read-only fixture; inspect structuredContent. Not production data."}}}
	f.calls = append(f.calls, map[string]any{"turn": f.turn, "tool": name, "batch_id": batch, "arguments": req.Params.Arguments, "original_result": result})
	if f.root != "" {
		raw, _ := json.MarshalIndent(f.calls, "", "  ")
		if err := os.WriteFile(filepath.Join(f.root, "fixture-calls.json"), raw, 0600); err != nil {
			f.captureErr = err
			return nil, err
		}
	}
	return result, nil
}
func valueFixtureSpec(ctx context.Context, root, workspace string) (mcp.ServerSpec, *valueFixture, func(), error) {
	f := &valueFixture{root: root}
	if err := f.set(1); err != nil {
		return mcp.ServerSpec{}, nil, nil, err
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "code-mode-inspections", Version: "1"}, nil)
	descriptions := map[string]string{"list_batches": "List the three batch IDs for the current synthetic inspection snapshot. Read-only.", "read_batch": "Read all inspection records in one batch. Records contain issue_id, record_id, status, severity, owner, updated_at and evidence. Read-only."}
	for name, schema := range valueToolSchemas() {
		name := name
		server.AddTool(&mcpsdk.Tool{Name: name, Description: descriptions[name], InputSchema: schema}, func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			return f.result(name, req)
		})
	}
	endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
	return mcp.ServerSpec{PluginID: "code-mode-value-fixture", Name: "workitems", Transport: mcp.TransportStreamableHTTP, URL: endpoint.URL, SourceTrusted: true, WorkDir: workspace}, f, endpoint.Close, nil
}
func TestCodeModeValueFixturesOffline(t *testing.T) {
	for turn := 1; turn <= 2; turn++ {
		d, err := valueLoad(turn)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		ids := map[string]bool{}
		for _, rows := range d.Batches {
			for _, r := range rows {
				if ids[r.RecordID] {
					t.Fatal("duplicate record ID")
				}
				ids[r.RecordID] = true
				n++
			}
		}
		if n != 35+turn {
			t.Fatalf("turn%d records=%d", turn, n)
		}
		raw, err := os.ReadFile(filepath.Join(valueDataDir, fmt.Sprintf("oracle-turn-%d.json", turn)))
		if err != nil {
			t.Fatal(err)
		}
		var want valueAnswer
		if json.Unmarshal(raw, &want) != nil || !reflect.DeepEqual(valueOracle(d), want) {
			t.Fatalf("independent oracle mismatch Turn %d", turn)
		}
		for _, g := range want.Owners {
			for _, i := range g.Issues {
				if turn == 2 && i.IssueID == "ISS-003" {
					t.Fatal("old open state survived latest closed record")
				}
				if i.IssueID == "ISS-004" && (i.RecordID != "R016" || i.BatchID != "batch-b") {
					t.Fatal("cross-batch tie-break lost")
				}
				if i.IssueID == "ISS-007" && i.RecordID != "R036" {
					t.Fatal("same-batch tie-break lost")
				}
			}
		}
	}
	root := t.TempDir()
	spec, f, closeFixture, err := valueFixtureSpec(t.Context(), root, root)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFixture()
	manager, err := mcp.NewManager(t.Context(), []mcp.ServerSpec{spec}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	<-manager.Initialized()
	definitions := map[string]tool.Definition{}
	for _, one := range manager.Tools() {
		def := one.Definition()
		definitions[def.Name] = def
	}
	if err := valueSelected(valueManifestFor("offline", nil, nil, nil, nil).Tasks[1], definitions); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"workitems__list_batches", "workitems__read_batch"} {
		one, found, err := manager.Lookup(t.Context(), name)
		if err != nil || !found {
			t.Fatalf("fixture tool missing %s %v", name, err)
		}
		input := []byte(`{}`)
		if name == "workitems__read_batch" {
			input = []byte(`{"batch_id":"batch-a"}`)
		}
		result, err := one.Call(t.Context(), tool.Call{ID: name, Name: name, Input: input})
		if err != nil || result.IsError || result.ID != name {
			t.Fatalf("fixture MCP failed: %+v %v", result, err)
		}
	}
	if len(f.calls) != 2 || f.captureErr != nil {
		t.Fatal("fixture counters/capture failed")
	}
}

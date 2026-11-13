package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
)

type nativeServiceErrorSource struct {
	*h1Source
	lookupErr error
}

func (s nativeServiceErrorSource) Lookup(context.Context, string) (tool.Tool, bool, error) {
	return nil, false, s.lookupErr
}

func TestNativeServiceDiscoveryRedactsSourceLookupErrors(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		t.Run(fmt.Sprint(wrapped), func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			const secret = "credential=secret"
			var lookupErr error = tool.NewError(tool.ErrorCodePermissionDenied, secret)
			if wrapped {
				lookupErr = fmt.Errorf("private source %s: %w", secret, lookupErr)
			}
			source := nativeServiceErrorSource{h1Source: h1Directory(1), lookupErr: lookupErr}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "native-service", Name: tool.ToolSearchToolName, Args: `{"server":"docs","limit":16}`}}
				}
				return nil
			}}
			runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{toolsearch.NewWithRanker(source.items, nil)}, DeferredTools: source}, nil, nil)
			if len(driver.requests) != 2 {
				t.Fatalf("model requests=%d, want 2", len(driver.requests))
			}
			result := nativeServiceResult(driver.requests[1], "native-service")
			if result == nil || !result.IsError {
				t.Fatalf("lookup failure produced a successful service page: %+v", result)
			}
			if code := errorDeliveryErrorCode(t, result.Content); code != string(tool.ErrorCodePermissionDenied) {
				t.Fatalf("lookup error=%s, want permission_denied", code)
			}
			raw, err := json.Marshal(driver.requests[1])
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), secret) || !strings.Contains(string(raw), "tool source lookup failed") {
				t.Fatalf("model received private or missing lookup diagnostic: %s", raw)
			}
		})
	}
}

func nativeServiceResult(request *model.Request, id string) *model.ToolResultPart {
	for _, message := range request.Messages {
		for _, part := range message.Parts {
			if part.ToolResult != nil && part.ToolResult.ToolUseID == id {
				return part.ToolResult
			}
		}
	}
	return nil
}

func nativeServicePage(result *model.ToolResultPart) (tool.ToolSearchResult, error) {
	if result == nil || result.IsError {
		return tool.ToolSearchResult{}, fmt.Errorf("missing or failed native service result: %+v", result)
	}
	for _, part := range result.Content {
		if part.JSON != nil {
			var page tool.ToolSearchResult
			err := json.Unmarshal(part.JSON.Value, &page)
			return page, err
		}
	}
	return tool.ToolSearchResult{}, fmt.Errorf("native service result has no JSON page")
}

func TestNativeServiceDiscoveryFiltersCurrentSourceBeforeLimit(t *testing.T) {
	for _, changed := range []string{"removed", "refreshed"} {
		t.Run(changed, func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			source := h1Directory(20)
			// These tools remain callable natively even though JavaScript cannot
			// execute tools requiring the command sandbox.
			for i := 16; i < len(source.items); i++ {
				one := source.items[i].(h1Tool)
				one.def.ExecutionRequirements = &tool.ExecutionRequirements{Sandbox: sandbox.CapabilitySet{CommandExec: true}}
				source.items[i] = one
			}
			catalog := toolsearch.NewWithRanker(append([]tool.Tool(nil), source.items...), nil)
			if changed == "removed" {
				source.items = source.items[16:]
			} else {
				for i := range 16 {
					one := source.items[i].(h1Tool)
					one.def = tool.CloneDefinition(one.def)
					one.def.Metadata[tool.MetadataMCPSourceFingerprint] = "refreshed"
					source.items[i] = one
				}
			}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "native-service", Name: tool.ToolSearchToolName, Args: `{"server":"docs","limit":16}`}}
				}
				return nil
			}}
			runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{catalog}, DeferredTools: source}, nil, nil)
			if len(driver.requests) != 2 {
				t.Fatalf("model requests=%d, want 2", len(driver.requests))
			}
			page, err := nativeServicePage(nativeServiceResult(driver.requests[1], "native-service"))
			if err != nil {
				t.Fatal(err)
			}
			if page.Count != 4 || page.Truncated || page.Next != "" || page.OmittedCount != 0 {
				t.Fatalf("current source eligibility consumed the page budget: %+v", page)
			}
		})
	}
}

func TestNativeServiceDiscoveryPaginatesEveryCurrentTool(t *testing.T) {
	store, active, _ := codeModeStore(t)
	source := h1Directory(37)
	seen := map[string]bool{}
	pages := 0
	var pageErr error
	driver := &mcpScriptModel{steps: func(i int, request *model.Request) []model.ToolCall {
		after := ""
		if i > 0 {
			page, err := nativeServicePage(nativeServiceResult(request, fmt.Sprintf("page-%d", i-1)))
			if err != nil {
				pageErr = err
				return nil
			}
			pages++
			if page.Count == 0 || page.Count > 16 || page.Count != len(page.Tools) {
				pageErr = fmt.Errorf("non-progressing service page: %+v", page)
				return nil
			}
			for _, entry := range page.Tools {
				if seen[entry.Name] || entry.Binding == nil {
					pageErr = fmt.Errorf("repeated or unbound tool: %+v", entry)
					return nil
				}
				seen[entry.Name] = true
			}
			if page.Next == "" {
				if page.Truncated {
					pageErr = fmt.Errorf("truncated service page lost its continuation")
				}
				return nil
			}
			if pages > 37 || !page.Truncated || page.Next != page.Tools[len(page.Tools)-1].Name {
				pageErr = fmt.Errorf("invalid service continuation: %+v", page)
				return nil
			}
			after = page.Next
		}
		args := map[string]any{"server": "docs", "plugin": "fixture", "limit": 16}
		if after != "" {
			args["after"] = after
		}
		raw, _ := json.Marshal(args)
		return []model.ToolCall{{ID: fmt.Sprintf("page-%d", i), Name: tool.ToolSearchToolName, Args: string(raw)}}
	}}
	runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{toolsearch.NewSource(source)}, DeferredTools: source}, nil, nil)
	if pageErr != nil {
		t.Fatal(pageErr)
	}
	if len(seen) != 37 || pages != 3 {
		t.Fatalf("discovered=%d pages=%d, want 37 tools across 3 pages", len(seen), pages)
	}
}

func TestNativeServiceDiscoveryRejectsRacedCurrentSource(t *testing.T) {
	for _, change := range []string{"removed", "refreshed"} {
		t.Run(change, func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			source := h1Directory(20)
			search := toolsearch.NewWithRanker(append([]tool.Tool(nil), source.items...), nil)
			// Change execution scope after ToolSearch's prefilter and selection,
			// before Chat's authoritative post-admission.
			raced := tool.NamedTool{Def: search.Definition(), Invoke: func(ctx context.Context, call tool.Call) (tool.Result, error) {
				result, err := search.Call(ctx, call)
				source.mu.Lock()
				defer source.mu.Unlock()
				if change == "removed" {
					source.items = source.items[16:]
				} else {
					one := source.items[0].(h1Tool)
					one.def = tool.CloneDefinition(one.def)
					one.def.Metadata[tool.MetadataMCPSourceFingerprint] = "refreshed"
					source.items[0] = one
				}
				return result, err
			}}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "native-service", Name: tool.ToolSearchToolName, Args: `{"server":"docs","limit":16}`}}
				}
				return nil
			}}
			runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{raced}, DeferredTools: source}, nil, nil)
			if len(driver.requests) != 2 {
				t.Fatalf("model requests=%d, want 2", len(driver.requests))
			}
			result := nativeServiceResult(driver.requests[1], "native-service")
			if result == nil || !result.IsError {
				t.Fatalf("raced scope produced a successful service page: %+v", result)
			}
			if code := errorDeliveryErrorCode(t, result.Content); code != string(tool.ErrorCodeStaleRevision) {
				t.Fatalf("raced scope error=%s, want stale_revision", code)
			}
		})
	}
}

func TestNativeServiceContinuationRejectsRevokedAnchor(t *testing.T) {
	store, active, _ := codeModeStore(t)
	source := h1Directory(20)
	catalog := toolsearch.NewWithRanker(append([]tool.Tool(nil), source.items...), nil)
	var pageErr error
	driver := &mcpScriptModel{steps: func(i int, request *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "first-page", Name: tool.ToolSearchToolName, Args: `{"server":"docs","limit":16}`}}
		}
		if i == 1 {
			page, err := nativeServicePage(nativeServiceResult(request, "first-page"))
			if err != nil {
				pageErr = fmt.Errorf("read first service page: %w", err)
				return nil
			}
			if page.Next == "" {
				pageErr = fmt.Errorf("missing first-page continuation: page=%+v", page)
				return nil
			}
			source.mu.Lock()
			source.items = append(source.items[:15], source.items[16:]...)
			source.mu.Unlock()
			raw, _ := json.Marshal(map[string]any{"server": "docs", "after": page.Next, "limit": 16})
			return []model.ToolCall{{ID: "revoked-anchor", Name: tool.ToolSearchToolName, Args: string(raw)}}
		}
		return nil
	}}
	runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{catalog}, DeferredTools: source}, nil, nil)
	if pageErr != nil {
		t.Fatal(pageErr)
	}
	if len(driver.requests) != 3 {
		t.Fatalf("model requests=%d, want 3", len(driver.requests))
	}
	result := nativeServiceResult(driver.requests[2], "revoked-anchor")
	if result == nil || !result.IsError || errorDeliveryErrorCode(t, result.Content) != string(tool.ErrorCodeStaleRevision) {
		t.Fatalf("revoked anchor produced a successful continuation: %+v", result)
	}
}

package mcp

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func asyncTestClient(ctx context.Context, spec ServerSpec, listGate <-chan struct{}) (*Client, error) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: spec.PluginID, Version: "1"}, nil)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "echo", Description: spec.PluginID}, func(context.Context, *mcpsdk.CallToolRequest, map[string]any) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{}, nil, nil
	})
	if listGate != nil {
		server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
			return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
				if method == "tools/list" {
					select {
					case <-listGate:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return next(ctx, method, req)
			}
		})
	}
	lifetime, cancel := context.WithCancel(context.Background())
	st, ct := mcpsdk.NewInMemoryTransports()
	if _, err := server.Connect(lifetime, st, nil); err != nil {
		cancel()
		return nil, err
	}
	sdk := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil)
	session, err := connectWithTimeout(ctx, sdk, lifetime, cancel, ct, DefaultStartupTimeout)
	if err != nil {
		cancel()
		return nil, err
	}
	return &Client{spec: spec, session: session, cancel: cancel, closed: make(chan struct{})}, nil
}

func TestManagerInitializesServersIndependentlyAndPublishesWholeLists(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		failures := make(chan ServerFailure, 3)
		specs := []ServerSpec{{PluginID: "p", Name: "slow"}, {PluginID: "p", Name: "fast"}, {PluginID: "p", Name: "broken"}}
		mgr, err := newManager(t.Context(), specs, func(ctx context.Context, spec ServerSpec) (*Client, error) {
			if spec.Name == "broken" {
				return nil, errors.New("private startup detail")
			}
			if spec.Name == "slow" {
				return asyncTestClient(ctx, spec, gate)
			}
			return asyncTestClient(ctx, spec, nil)
		}, func(f ServerFailure) { failures <- f })
		if err != nil {
			t.Fatal(err)
		}
		defer mgr.Close()
		synctest.Wait()
		if tools := mgr.Tools(); len(tools) != 1 || tools[0].Definition().Name != "fast__echo" {
			t.Fatalf("ready tools = %#v", tools)
		}
		if len(failures) != 1 {
			t.Fatalf("failures = %d", len(failures))
		}
		close(gate)
		<-mgr.Initialized()
		if tools := mgr.Tools(); len(tools) != 2 {
			t.Fatalf("tools after full listing = %d", len(tools))
		}
	})
}

func TestManagerServerInitializationTimesOutAtThirtySeconds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		failures := make(chan ServerFailure, 1)
		start := time.Now()
		mgr, err := newManager(t.Context(), []ServerSpec{{PluginID: "p", Name: "slow"}}, func(ctx context.Context, _ ServerSpec) (*Client, error) { <-ctx.Done(); return nil, ctx.Err() }, func(f ServerFailure) { failures <- f })
		if err != nil {
			t.Fatal(err)
		}
		defer mgr.Close()
		synctest.Wait()
		if len(failures) != 0 {
			t.Fatal("server failed before timeout")
		}
		<-mgr.Initialized()
		if got := time.Since(start); got != 30*time.Second {
			t.Fatalf("timeout = %s", got)
		}
		if f := <-failures; !errors.Is(f.Err, context.DeadlineExceeded) {
			t.Fatalf("failure = %v", f.Err)
		}
		if len(mgr.Tools()) != 0 {
			t.Fatal("timed out server published tools")
		}
	})
}

func TestManagerCloseDrainsLateSuccessfulConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan *Client, 1)
		failures := make(chan ServerFailure, 1)
		mgr, err := newManager(t.Context(), []ServerSpec{{PluginID: "p", Name: "late"}}, func(ctx context.Context, spec ServerSpec) (*Client, error) {
			client, err := asyncTestClient(ctx, spec, nil)
			if err != nil {
				return nil, err
			}
			started <- client
			<-ctx.Done()
			return client, nil
		}, func(f ServerFailure) { failures <- f })
		if err != nil {
			t.Fatal(err)
		}
		client := <-started
		if err := mgr.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-client.closed:
		default:
			t.Fatal("late connection was not closed")
		}
		if len(mgr.Tools()) != 0 || len(failures) != 0 {
			t.Fatal("close published tools or cancellation notice")
		}
		if err := mgr.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestManagerConnectionAndListingShareOneStartupBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		failures := make(chan ServerFailure, 1)
		began := time.Now()
		mgr, err := newManager(t.Context(), []ServerSpec{{PluginID: "p", Name: "slow-list"}}, func(ctx context.Context, spec ServerSpec) (*Client, error) {
			time.Sleep(20 * time.Second)
			return asyncTestClient(ctx, spec, make(chan struct{}))
		}, func(failure ServerFailure) { failures <- failure })
		if err != nil {
			t.Fatal(err)
		}
		defer mgr.Close()
		<-mgr.Initialized()
		if elapsed := time.Since(began); elapsed != 30*time.Second {
			t.Fatalf("connection plus listing took %s", elapsed)
		}
		if failure := <-failures; !errors.Is(failure.Err, context.DeadlineExceeded) {
			t.Fatalf("failure=%v", failure.Err)
		}
		if len(mgr.Tools()) != 0 {
			t.Fatal("incomplete tool list was published")
		}
	})
}

func TestManagerCollisionPriorityDoesNotFollowConnectionOrder(t *testing.T) {
	for _, fails := range []bool{false, true} {
		t.Run(map[bool]string{false: "preferred_ready", true: "preferred_failed"}[fails], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				gate := make(chan struct{})
				specs := []ServerSpec{{PluginID: "preferred", Name: "docs"}, {PluginID: "fallback", Name: "docs"}}
				mgr, err := newManager(t.Context(), specs, func(ctx context.Context, spec ServerSpec) (*Client, error) {
					if spec.PluginID == "preferred" {
						<-gate
						if fails {
							return nil, errors.New("failed")
						}
					}
					return asyncTestClient(ctx, spec, nil)
				}, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer mgr.Close()
				synctest.Wait()
				if len(mgr.Tools()) != 0 {
					t.Fatal("lower-priority tools published before collision ownership was known")
				}
				close(gate)
				<-mgr.Initialized()
				want := "preferred"
				if fails {
					want = "fallback"
				}
				ready := mgr.Tools()
				if len(ready) != 1 || ready[0].Definition().Metadata[tool.MetadataPluginID] != want {
					t.Fatalf("winner=%#v want %s", ready, want)
				}
			})
		})
	}
}

func TestManagerPublishedDefinitionsRemainImmutableAfterAliasMerge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		specs := []ServerSpec{{PluginID: "preferred", Name: "docs"}, {PluginID: "fallback", Name: "docs"}}
		mgr, err := newManager(t.Context(), specs, func(ctx context.Context, spec ServerSpec) (*Client, error) {
			if spec.PluginID == "fallback" {
				return asyncTestClient(ctx, spec, gate)
			}
			return asyncTestClient(ctx, spec, nil)
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer mgr.Close()
		synctest.Wait()
		first := mgr.Tools()[0]
		firstAliases := first.Definition().Metadata[tool.MetadataReplayAliases]
		close(gate)
		<-mgr.Initialized()
		ready := mgr.Tools()
		if len(ready) != 1 || ready[0].Definition().Metadata[tool.MetadataPluginID] != "preferred" {
			t.Fatalf("winner=%#v", ready)
		}
		if first.Definition().Metadata[tool.MetadataPluginID] != "preferred" {
			t.Fatal("published callable mutated")
		}
		// Keep a reader's prior definition independent of later alias merges.
		if a, ok := firstAliases.([]string); ok {
			if !slices.Equal(a, first.Definition().Metadata[tool.MetadataReplayAliases].([]string)) {
				t.Fatal("published aliases mutated")
			}
		}
	})
}

package client

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/internal/acpagentbridge/endpoint"
)

type ownedStreamResolver func(context.Context, endpoint.Request) (endpoint.Process, error)

func (f ownedStreamResolver) ResolveACPProcess(ctx context.Context, r endpoint.Request) (endpoint.Process, error) {
	return f(ctx, r)
}

func TestHostedOwnedStreamClosesJoinsAndReleases(t *testing.T) {
	for _, test := range []struct {
		draft     string
		cancelled bool
	}{
		{},
		{draft: "unsupported-draft"},
		{draft: "unsupported-draft", cancelled: true},
	} {
		t.Run(test.draft, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if test.cancelled {
				cancel()
			}
			var closed, released atomic.Int32
			var joined atomic.Bool
			done := make(chan struct{})
			resolver := ownedStreamResolver(func(context.Context, endpoint.Request) (endpoint.Process, error) {
				return endpoint.Process{Open: func(context.Context) (endpoint.Stream, error) {
					inReader, inWriter := io.Pipe()
					outReader, outWriter := io.Pipe()
					var once sync.Once
					go func() {
						defer close(done)
						defer inReader.Close()
						defer outWriter.Close()
						scanner := bufio.NewScanner(inReader)
						for scanner.Scan() {
							var frame struct {
								ID json.RawMessage `json:"id"`
							}
							if json.Unmarshal(scanner.Bytes(), &frame) != nil {
								return
							}
							raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "result": InitializeResponse{ProtocolVersion: 1, AuthMethods: []json.RawMessage{}}})
							if _, err := outWriter.Write(append(raw, '\n')); err != nil {
								return
							}
						}
					}()
					return endpoint.Stream{Input: inWriter, Output: outReader, Close: func() error {
						once.Do(func() { closed.Add(1); _ = inWriter.Close(); _ = outReader.Close() })
						return nil
					}, Wait: func(ctx context.Context) error {
						if err := ctx.Err(); err != nil {
							return err
						}
						select {
						case <-done:
							joined.Store(true)
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					}}, nil
				}, Release: func() {
					if !joined.Load() {
						t.Error("stream released before owned server joined")
					}
					released.Add(1)
				}}, nil
			})
			client, err := Start(ctx, Config{HostedAdapterID: "local", WorkDir: t.TempDir(), EndpointResolver: resolver, ACPDraftSchema: test.draft})
			if test.draft != "" {
				if err == nil {
					t.Fatal("unsupported draft stream accepted")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, err = client.Initialize(ctx); err != nil {
					t.Fatal(err)
				}
				if err = client.Close(ctx); err != nil {
					t.Fatal(err)
				}
				if err = client.Close(ctx); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-done:
			default:
				t.Fatal("server goroutine outlived stream release")
			}
			if closed.Load() != 1 || released.Load() != 1 {
				t.Fatalf("owned cleanup counts close=%d release=%d", closed.Load(), released.Load())
			}
		})
	}
}

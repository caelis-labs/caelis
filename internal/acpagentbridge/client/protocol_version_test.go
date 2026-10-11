package client

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
)

func TestInitializeRejectsNonV1NegotiatedVersion(t *testing.T) {
	t.Parallel()
	for _, version := range []int{0, 2, 3} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			clientSide, peerSide := net.Pipe()
			defer peerSide.Close()
			peer, err := acpsdk.NewConnectionWithOptions(func(_ context.Context, method string, _ json.RawMessage) (any, *acpsdk.RequestError) {
				if method != MethodInitialize {
					t.Errorf("unexpected fallback method %q", method)
					return nil, acpsdk.NewMethodNotFound(method)
				}
				return map[string]any{"protocolVersion": version, "info": map[string]any{"name": "v2-peer", "version": "test"}, "capabilities": map[string]any{"session": map[string]any{}}}, nil
			}, peerSide, peerSide, acpsdk.ConnectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer peer.Close()
			client, err := NewStreamClient(clientSide, clientSide, Config{})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close(context.Background())
			response, err := client.Initialize(ctx)
			if err == nil || !strings.Contains(err.Error(), "requires stable v1") || response.ProtocolVersion != 0 {
				t.Fatalf("initialize version %d = %#v, %v", version, response, err)
			}
		})
	}
}

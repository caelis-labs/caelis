//go:build e2e

package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	surfaceacp "github.com/caelis-labs/caelis/surfaces/acp"
)

// Compile the peers in independent module graphs so this exercises actual old
// and current SDK implementations, rather than two imports of the selected pin.
func TestACPSDKReleaseCompatibility(t *testing.T) {
	for _, version := range []string{"v1.5.2", "v1.6.0"} {
		t.Run(version, func(t *testing.T) {
			root := t.TempDir()
			for _, name := range []string{"main.go", "go.mod", "go.sum"} {
				data, err := os.ReadFile(filepath.Join("testdata", "acp-sdk-peer", name))
				if err != nil {
					t.Fatal(err)
				}
				if name == "go.mod" {
					data = bytes.ReplaceAll(data, []byte("v1.5.2"), []byte(version))
				}
				if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			bin := filepath.Join(root, "peer")
			if os.PathSeparator == '\\' {
				bin += ".exe"
			}
			build := exec.CommandContext(t.Context(), "go", "build", "-mod=readonly", "-o", bin, ".")
			build.Dir = root
			build.Env = append(os.Environ(), "GOWORK=off")
			if output, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build independent SDK %s: %v\n%s", version, err, output)
			}
			t.Run("Core-client-to-SDK-agent", func(t *testing.T) { testCoreClientToSDKAgent(t, bin) })
			t.Run("SDK-client-to-Core-surface", func(t *testing.T) { testSDKClientToCoreSurface(t, bin) })
		})
	}
}

func testCoreClientToSDKAgent(t *testing.T, bin string) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	var text strings.Builder
	var mu sync.Mutex
	before := make(chan struct{})
	permissions := 0
	c, err := client.Start(ctx, client.Config{Command: bin, Args: []string{"agent"}, WorkDir: t.TempDir(),
		OnUpdate: func(envelope client.UpdateEnvelope) {
			if chunk, ok := envelope.Update.(client.ContentChunk); ok {
				var content struct {
					Text string `json:"text"`
				}
				if err := json.Unmarshal(chunk.Content, &content); err != nil {
					t.Error(err)
				}
				mu.Lock()
				text.WriteString(content.Text)
				mu.Unlock()
				if content.Text == "before" {
					close(before)
				}
			}
		},
		OnPermissionRequest: func(ctx context.Context, request client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
			select {
			case <-before:
			case <-ctx.Done():
				return client.RequestPermissionResponse{}, ctx.Err()
			}
			mu.Lock()
			defer mu.Unlock()
			if text.String() != "before" || len(request.Options) != 1 || request.Options[0].OptionId != "original" {
				return client.RequestPermissionResponse{}, fmt.Errorf("permission ordering/identity: %q %+v", text.String(), request)
			}
			permissions++
			return client.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected("original")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	if _, err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	s, err := c.NewSession(ctx, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := c.Prompt(ctx, s.SessionID, "compat", nil)
	mu.Lock()
	if err != nil || response.StopReason != "end_turn" || text.String() != "beforeafter" || permissions != 1 {
		defer mu.Unlock()
		t.Fatalf("conversation: %+v %v text=%q permissions=%d", response, err, text.String(), permissions)
	}
	mu.Unlock()
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

type sdkCompatSurface struct{}

func (sdkCompatSurface) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{ProtocolVersion: 1}, nil
}
func (sdkCompatSurface) NewSession(context.Context, acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	return acp.NewSessionResponse{SessionId: "compat"}, nil
}
func (sdkCompatSurface) Cancel(context.Context, acp.CancelNotification) error { return nil }
func (sdkCompatSurface) Prompt(ctx context.Context, req acp.PromptRequest, cb surfaceacp.PromptCallbacks) (acp.PromptResponse, error) {
	for _, text := range []string{"before", "after"} {
		if err := cb.SessionUpdate(ctx, eventstream.SessionNotification{SessionID: string(req.SessionId), Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, Content: json.RawMessage(`{"type":"text","text":"` + text + `"}`)}}); err != nil {
			return acp.PromptResponse{}, err
		}
		if text == "before" {
			response, err := cb.RequestPermission(ctx, acp.RequestPermissionRequest{SessionId: req.SessionId, ToolCall: acp.ToolCallUpdate{ToolCallId: "tool", Title: acp.Ptr("Read")}, Options: []acp.PermissionOption{{OptionId: "original", Name: "Allow", Kind: acp.PermissionOptionKindAllowOnce}}})
			if err != nil {
				return acp.PromptResponse{}, err
			}
			if response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != "original" {
				return acp.PromptResponse{}, fmt.Errorf("permission identity changed: %+v", response)
			}
		}
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func testSDKClientToCoreSurface(t *testing.T, bin string) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "client")
	in, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- surfaceacp.ServeStdio(ctx, sdkCompatSurface{}, in, out) }()
	serveErr := <-done
	if err := cmd.Wait(); err != nil {
		t.Fatalf("SDK client: %v %s", err, stderr.String())
	}
	if serveErr != nil {
		t.Fatal(serveErr)
	}
	if !strings.Contains(stderr.String(), "stable-v1 conversation and permission ordering passed") {
		t.Fatalf("missing peer assertions: %s", stderr.String())
	}
}

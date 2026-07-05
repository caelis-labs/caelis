package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type Client struct {
	spec      ServerSpec
	session   *mcpsdk.ClientSession
	transport string
	cancel    context.CancelFunc

	closeOnce sync.Once
	closed    chan struct{}
	closeErr  error
}

func resolveExecutable(command string, workDir string) string {
	if filepath.IsAbs(command) {
		return command
	}
	if strings.Contains(command, string(filepath.Separator)) {
		abs := filepath.Join(workDir, command)
		if _, err := os.Stat(abs); err == nil {
			return abs
		}
	}
	return command
}

func StartClient(ctx context.Context, spec ServerSpec) (*Client, error) {
	transport, transportName, err := transportForSpec(spec)
	if err != nil {
		return nil, err
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{
		Name:    "caelis",
		Title:   "Caelis",
		Version: "1.0.0",
	}, &mcpsdk.ClientOptions{
		Capabilities: &mcpsdk.ClientCapabilities{},
	})
	lifetimeCtx, cancel := context.WithCancel(context.Background())
	session, err := connectWithTimeout(ctx, client, lifetimeCtx, cancel, transport, 15*time.Second)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("mcp: connect %s server %s/%s failed: %w", transportName, spec.PluginID, spec.Name, err)
	}
	return &Client{
		spec:      spec,
		session:   session,
		transport: transportName,
		cancel:    cancel,
		closed:    make(chan struct{}),
	}, nil
}

func connectWithTimeout(ctx context.Context, client *mcpsdk.Client, lifetimeCtx context.Context, cancel context.CancelFunc, transport mcpsdk.Transport, timeout time.Duration) (*mcpsdk.ClientSession, error) {
	type connectResult struct {
		session *mcpsdk.ClientSession
		err     error
	}
	done := make(chan connectResult, 1)
	go func() {
		session, err := client.Connect(lifetimeCtx, transport, nil)
		done <- connectResult{session: session, err: err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-done:
		return result.session, result.err
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	case <-timer.C:
		cancel()
		return nil, fmt.Errorf("connection timed out after %s", timeout)
	}
}

func transportForSpec(spec ServerSpec) (mcpsdk.Transport, string, error) {
	transportName := NormalizeTransport(spec.Transport, spec.Command, spec.URL)
	switch transportName {
	case TransportStdio:
		workDir := strings.TrimSpace(spec.WorkDir)
		if workDir == "" {
			return nil, "", fmt.Errorf("mcp: workDir is required for stdio server %s/%s", spec.PluginID, spec.Name)
		}
		command := strings.TrimSpace(spec.Command)
		if command == "" {
			return nil, "", fmt.Errorf("mcp: command is required for stdio server %s/%s", spec.PluginID, spec.Name)
		}
		cmd := exec.Command(resolveExecutable(command, workDir), spec.Args...)
		cmd.Dir = workDir
		cmd.Env = os.Environ()
		for k, v := range spec.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
		return &mcpsdk.CommandTransport{Command: cmd}, transportName, nil
	case TransportStreamableHTTP:
		endpoint := strings.TrimSpace(spec.URL)
		if endpoint == "" {
			return nil, "", fmt.Errorf("mcp: url is required for streamable HTTP server %s/%s", spec.PluginID, spec.Name)
		}
		return &mcpsdk.StreamableClientTransport{
			Endpoint:             endpoint,
			HTTPClient:           httpClientWithHeaders(spec.Headers),
			DisableStandaloneSSE: true,
		}, transportName, nil
	case TransportSSE:
		endpoint := strings.TrimSpace(spec.URL)
		if endpoint == "" {
			return nil, "", fmt.Errorf("mcp: url is required for SSE server %s/%s", spec.PluginID, spec.Name)
		}
		return &mcpsdk.SSEClientTransport{
			Endpoint:   endpoint,
			HTTPClient: httpClientWithHeaders(spec.Headers),
		}, transportName, nil
	default:
		return nil, "", fmt.Errorf("mcp: unsupported transport %q for server %s/%s", spec.Transport, spec.PluginID, spec.Name)
	}
}

func httpClientWithHeaders(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return nil
	}
	return &http.Client{Transport: headerRoundTripper{
		base:    http.DefaultTransport,
		headers: headers,
	}}
}

type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (rt headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	cloned := req.Clone(req.Context())
	for k, v := range rt.headers {
		if strings.TrimSpace(k) == "" {
			continue
		}
		cloned.Header.Set(k, v)
	}
	return base.RoundTrip(cloned)
}

func (c *Client) ListTools(ctx context.Context) ([]*mcpsdk.Tool, error) {
	if err := c.closedError(); err != nil {
		return nil, err
	}
	var out []*mcpsdk.Tool
	var cursor string
	for {
		var params *mcpsdk.ListToolsParams
		if cursor != "" {
			params = &mcpsdk.ListToolsParams{Cursor: cursor}
		}
		res, err := c.session.ListTools(ctx, params)
		if err != nil {
			c.markFailed(err)
			return nil, err
		}
		out = append(out, res.Tools...)
		cursor = strings.TrimSpace(res.NextCursor)
		if cursor == "" {
			break
		}
	}
	return out, nil
}

func (c *Client) CallTool(ctx context.Context, params *mcpsdk.CallToolParams) (*mcpsdk.CallToolResult, error) {
	if err := c.closedError(); err != nil {
		return nil, err
	}
	res, err := c.session.CallTool(ctx, params)
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	var closeErr error
	c.closeOnce.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
		if c.session != nil {
			closeErr = c.session.Close()
		}
		if closeErr != nil {
			c.closeErr = closeErr
		} else {
			c.closeErr = errors.New("closed")
		}
		close(c.closed)
	})
	return closeErr
}

func (c *Client) closedError() error {
	if c == nil {
		return errors.New("mcp client is nil")
	}
	select {
	case <-c.closed:
		return fmt.Errorf("mcp client closed: %w", c.closeErr)
	default:
		return nil
	}
}

func (c *Client) markFailed(err error) {
	if c == nil || err == nil {
		return
	}
	c.closeOnce.Do(func() {
		c.closeErr = err
		if c.cancel != nil {
			c.cancel()
		}
		if c.session != nil {
			_ = c.session.Close()
		}
		close(c.closed)
	})
}

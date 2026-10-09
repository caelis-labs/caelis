package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// DefaultStartupTimeout bounds one server initialization, including tool listing.
const DefaultStartupTimeout = 30 * time.Second

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
	return startClientWithHTTPClient(ctx, spec, nil)
}

func startClientWithHTTPClient(ctx context.Context, spec ServerSpec, httpClient *http.Client) (*Client, error) {
	transport, transportName, err := transportForSpecWithHTTPClient(spec, httpClient)
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
	session, err := connectWithTimeout(ctx, client, lifetimeCtx, cancel, transport, DefaultStartupTimeout)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("connect %s MCP server %s/%s: %w", transportName, spec.PluginID, spec.Name, err)
	}
	connected := &Client{
		spec:      spec,
		session:   session,
		transport: transportName,
		cancel:    cancel,
		closed:    make(chan struct{}),
	}
	// Observe process/connection exit independently of tool calls. A cancelled
	// DTW script can leave its supervisor alive; only the connection closing
	// marks the client failed, and no operation is retried here.
	go func() {
		if err := session.Wait(); err != nil {
			connected.markFailed(err)
		} else {
			connected.markFailed(errors.New("MCP server connection closed"))
		}
	}()
	return connected, nil
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
		result := <-done
		if result.session != nil {
			_ = result.session.Close()
		}
		return nil, ctx.Err()
	case <-timer.C:
		cancel()
		result := <-done
		if result.session != nil {
			_ = result.session.Close()
		}
		return nil, fmt.Errorf("connection timed out after %s: %w", timeout, context.DeadlineExceeded)
	}
}

func transportForSpecWithHTTPClient(spec ServerSpec, httpClient *http.Client) (mcpsdk.Transport, string, error) {
	transportName := NormalizeTransport(spec.Transport, spec.Command, spec.URL)
	switch transportName {
	case TransportStdio:
		workDir := strings.TrimSpace(spec.WorkDir)
		if workDir == "" {
			return nil, "", fmt.Errorf("workDir is required for stdio MCP server %s/%s", spec.PluginID, spec.Name)
		}
		command := strings.TrimSpace(spec.Command)
		if command == "" {
			return nil, "", fmt.Errorf("command is required for stdio MCP server %s/%s", spec.PluginID, spec.Name)
		}
		if spec.DataDir != "" {
			if err := ensurePluginDataDir(spec.DataDir, workDir); err != nil {
				return nil, "", fmt.Errorf("plugin data directory: %w", err)
			}
		}
		cmd := exec.Command(resolveExecutable(command, workDir), spec.Args...)
		cmd.Dir = workDir
		if spec.CleanEnvironment {
			cmd.Env = pluginBaseEnvironment()
		} else {
			cmd.Env = os.Environ()
		}
		for k, v := range spec.Env {
			cmd.Env = setProcessEnvironment(cmd.Env, k, v)
		}
		return &mcpsdk.CommandTransport{Command: cmd}, transportName, nil
	case TransportStreamableHTTP:
		endpoint := strings.TrimSpace(spec.URL)
		if endpoint == "" {
			return nil, "", fmt.Errorf("URL is required for streamable HTTP MCP server %s/%s", spec.PluginID, spec.Name)
		}
		return &mcpsdk.StreamableClientTransport{
			Endpoint:             endpoint,
			HTTPClient:           httpClientWithHeaders(httpClient, endpoint, spec.Headers),
			DisableStandaloneSSE: true,
		}, transportName, nil
	case TransportSSE:
		endpoint := strings.TrimSpace(spec.URL)
		if endpoint == "" {
			return nil, "", fmt.Errorf("URL is required for SSE MCP server %s/%s", spec.PluginID, spec.Name)
		}
		return &mcpsdk.SSEClientTransport{
			Endpoint:   endpoint,
			HTTPClient: httpClientWithHeaders(httpClient, endpoint, spec.Headers),
		}, transportName, nil
	default:
		return nil, "", fmt.Errorf("unsupported transport %q for MCP server %s/%s", spec.Transport, spec.PluginID, spec.Name)
	}
}

func ensurePluginDataDir(dataDir, workDir string) error {
	if !filepath.IsAbs(dataDir) {
		return fmt.Errorf("data directory must be absolute")
	}
	storeDir := filepath.Dir(filepath.Dir(filepath.Dir(dataDir)))
	if err := makePrivateChildDirs(storeDir, filepath.Join("plugins", "data", filepath.Base(dataDir))); err != nil {
		return err
	}
	info, err := os.Lstat(dataDir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("data directory is not a real directory")
	}
	realData, err := filepath.EvalSymlinks(dataDir)
	if err != nil {
		return err
	}
	if !pathInside(dataDir, workDir) {
		return nil // the plugin root is a valid immutable working directory
	}
	rel, _ := filepath.Rel(dataDir, workDir)
	if err := makePrivateChildDirs(dataDir, rel); err != nil {
		return err
	}
	realWorkDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return err
	}
	if !pathInside(realData, realWorkDir) {
		return fmt.Errorf("working directory escapes plugin data directory")
	}
	return nil
}

func makePrivateChildDirs(root, relative string) error {
	if !filepath.IsAbs(root) || !pathInside(root, filepath.Join(root, relative)) {
		return fmt.Errorf("private directory escapes host root")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	current := root
	if relative == "." {
		return nil
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("invalid private directory component")
		}
		current = filepath.Join(current, part)
		if err := os.Mkdir(current, 0o700); err != nil && !os.IsExist(err) {
			return err
		}
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("private directory is not a real directory: %s", current)
		}
	}
	return nil
}

func pathInside(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)))
}

func pluginBaseEnvironment() []string {
	allowed := []string{"PATH", "HOME", "USER", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL"}
	if runtime.GOOS == "windows" {
		allowed = append(allowed, "SYSTEMROOT", "WINDIR", "APPDATA", "LOCALAPPDATA", "USERPROFILE")
	}
	var out []string
	for _, key := range allowed {
		if value, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+value)
		}
	}
	return out
}

func setProcessEnvironment(base []string, key, value string) []string {
	if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, '\x00') {
		return base
	}
	for i, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		if name == key || (runtime.GOOS == "windows" && strings.EqualFold(name, key)) {
			base[i] = key + "=" + value
			return base
		}
	}
	return append(base, key+"="+value)
}

func httpClientWithHeaders(client *http.Client, endpoint string, headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return client
	}
	if client == nil {
		client = &http.Client{}
	}
	cloned := *client
	origin, err := url.Parse(endpoint)
	if err != nil {
		return &cloned
	}
	cloned.Transport = headerRoundTripper{
		base:    client.Transport,
		headers: headers,
		origin:  origin.Scheme + "://" + origin.Host,
	}
	return &cloned
}

type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
	origin  string
}

func (rt headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	cloned := req.Clone(req.Context())
	if !strings.EqualFold(cloned.URL.Scheme+"://"+cloned.URL.Host, rt.origin) {
		return base.RoundTrip(cloned)
	}
	for k, v := range rt.headers {
		if strings.TrimSpace(k) == "" {
			continue
		}
		if !hasHeaderFold(cloned.Header, k) {
			cloned.Header.Set(k, v)
		}
	}
	return base.RoundTrip(cloned)
}

func hasHeaderFold(headers http.Header, name string) bool {
	for existing := range headers {
		if strings.EqualFold(existing, name) {
			return true
		}
	}
	return false
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

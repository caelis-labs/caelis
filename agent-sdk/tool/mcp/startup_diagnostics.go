package mcp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const startupStderrLimit = 4096

// Only fixed categories leave this observer. Raw stderr, URLs, headers and
// command arguments are never included in status or notices.
type startupDiagnostics struct {
	mu       sync.Mutex
	tail     [startupStderrLimit]byte
	used     int
	stopped  bool
	observed string
}

func observeStartup(transport mcpsdk.Transport, spec ServerSpec) *startupDiagnostics {
	d := new(startupDiagnostics)
	wrap := func(client *http.Client) *http.Client {
		if client == nil {
			client = http.DefaultClient
		}
		cloned := *client
		base := client.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		cloned.Transport = startupRoundTripper{base: base, diagnostics: d}
		return &cloned
	}
	switch t := transport.(type) {
	case *mcpsdk.CommandTransport:
		if isNPMLauncher(spec.Command) {
			t.Command.Stderr = d
		}
	case *mcpsdk.StreamableClientTransport:
		t.HTTPClient = wrap(t.HTTPClient)
	case *mcpsdk.SSEClientTransport:
		t.HTTPClient = wrap(t.HTTPClient)
	}
	return d
}

func isNPMLauncher(command string) bool {
	switch strings.ToLower(filepath.Base(command)) {
	case "npx", "npx.cmd", "npx.exe", "npm", "npm.cmd", "npm.exe":
		return true
	}
	return false
}

func (d *startupDiagnostics) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.stopped {
		if len(p) >= len(d.tail) {
			copy(d.tail[:], p[len(p)-len(d.tail):])
			d.used = len(d.tail)
		} else {
			keep := min(d.used, len(d.tail)-len(p))
			copy(d.tail[:], d.tail[d.used-keep:d.used])
			copy(d.tail[keep:], p)
			d.used = keep + len(p)
		}
	}
	return len(p), nil
}

func (d *startupDiagnostics) stopStderr() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = true
	clear(d.tail[:])
	d.used = 0
}

func (d *startupDiagnostics) resetHTTP() {
	if d != nil {
		d.mu.Lock()
		d.observed = ""
		d.mu.Unlock()
	}
}

func (d *startupDiagnostics) reason(err error) string {
	if d != nil {
		d.mu.Lock()
		defer d.mu.Unlock()
		if reason := npmStderrReason(string(d.tail[:d.used])); reason != "" {
			return reason
		}
		if d.observed != "" {
			return d.observed
		}
	}
	return networkFailureReason(err)
}

type startupRoundTripper struct {
	base        http.RoundTripper
	diagnostics *startupDiagnostics
}

func (rt startupRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := rt.base.RoundTrip(req)
	reason := networkFailureReason(err)
	if response != nil {
		switch response.StatusCode {
		case http.StatusUnauthorized:
			reason = "HTTP 401 authentication rejected"
		case http.StatusForbidden:
			reason = "HTTP 403 access denied"
		}
	}
	if reason != "" {
		rt.diagnostics.mu.Lock()
		rt.diagnostics.observed = reason
		rt.diagnostics.mu.Unlock()
	}
	// Preserve the original response/error for SDK authentication and teardown.
	return response, err
}

func networkFailureReason(err error) string {
	var dns *net.DNSError
	var network net.Error
	switch {
	case errors.As(err, &dns):
		if dns.IsTimeout {
			return "DNS lookup timed out"
		}
		return "DNS lookup failed"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, os.ErrNotExist):
		return "executable not found"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return ""
	case errors.As(err, &network) && network.Timeout():
		return "network request timed out"
	}
	return ""
}

// An npm code alone does not establish a registry network failure. Require
// npm-prefixed fetch evidence too; never copy its endpoint or credentials.
func npmStderrReason(tail string) string {
	code := ""
	fetch := false
	for _, line := range strings.Split(tail, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if !strings.HasPrefix(lower, "npm error ") && !strings.HasPrefix(lower, "npm err! ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 4 && strings.EqualFold(fields[2], "code") {
			code = fields[3]
		}
		if strings.Contains(lower, "request to ") || strings.Contains(lower, "fetch") || strings.Contains(lower, " - get https://") || strings.Contains(lower, " - get http://") {
			fetch = true
		}
	}
	switch code {
	case "E401":
		return "npm authentication rejected (E401)"
	case "E403":
		return "npm access denied (E403)"
	}
	if !fetch {
		return ""
	}
	switch code {
	case "ENOTFOUND":
		return "npm registry DNS failure (ENOTFOUND)"
	case "EAI_AGAIN":
		return "npm registry DNS failure (EAI_AGAIN)"
	case "ECONNREFUSED":
		return "npm registry connection refused (ECONNREFUSED)"
	case "ETIMEDOUT", "ESOCKETTIMEDOUT":
		return "npm registry request timed out"
	case "E404":
		return "npm package resolution/download failed (E404)"
	}
	return ""
}

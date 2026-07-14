package appserver

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type requestPolicy struct {
	allowedHosts map[string]struct{}
}

func newRequestPolicy(allowedHosts []string) (*requestPolicy, error) {
	policy := &requestPolicy{allowedHosts: make(map[string]struct{}, len(allowedHosts))}
	for _, allowed := range allowedHosts {
		host, _, err := splitAuthority(allowed)
		if err != nil {
			return nil, fmt.Errorf("appserver: invalid allowed host %q: %w", allowed, err)
		}
		policy.allowedHosts[host] = struct{}{}
	}
	if len(policy.allowedHosts) == 0 {
		return nil, fmt.Errorf("appserver: at least one allowed Host is required")
	}
	return policy, nil
}

func (p *requestPolicy) authorize(request *http.Request) error {
	if p == nil || request == nil {
		return fmt.Errorf("request trust policy is unavailable")
	}
	host, _, err := splitAuthority(request.Host)
	if err != nil {
		return fmt.Errorf("invalid Host")
	}
	if _, ok := p.allowedHosts[host]; !ok {
		return fmt.Errorf("host is not allowed")
	}

	// Fetch Metadata is browser-controlled. Native clients normally omit it.
	// same-site is still cross-origin and is rejected together with cross-site.
	fetchValues := request.Header.Values("Sec-Fetch-Site")
	if len(fetchValues) > 1 || len(fetchValues) == 1 && strings.Contains(fetchValues[0], ",") {
		return fmt.Errorf("invalid Sec-Fetch-Site")
	}
	fetchSite := ""
	if len(fetchValues) == 1 {
		fetchSite = strings.ToLower(strings.TrimSpace(fetchValues[0]))
	}
	switch fetchSite {
	case "", "same-origin", "none":
	case "same-site", "cross-site":
		return fmt.Errorf("cross-origin browser request is not allowed")
	default:
		return fmt.Errorf("invalid Sec-Fetch-Site")
	}

	origins := request.Header.Values("Origin")
	if len(origins) == 0 {
		return nil
	}
	if len(origins) != 1 || strings.Contains(origins[0], ",") {
		return fmt.Errorf("invalid Origin")
	}
	origin, err := url.Parse(strings.TrimSpace(origins[0]))
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil ||
		origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return fmt.Errorf("invalid Origin")
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	if !strings.EqualFold(origin.Scheme, scheme) {
		return fmt.Errorf("origin is not same-origin")
	}
	requestAuthority, err := canonicalAuthority(request.Host, scheme)
	if err != nil {
		return fmt.Errorf("invalid Host")
	}
	originAuthority, err := canonicalAuthority(origin.Host, scheme)
	if err != nil || originAuthority != requestAuthority {
		return fmt.Errorf("origin is not same-origin")
	}
	return nil
}

func canonicalAuthority(authority, scheme string) (string, error) {
	host, port, err := splitAuthority(authority)
	if err != nil {
		return "", err
	}
	if port == "" {
		switch strings.ToLower(scheme) {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return "", fmt.Errorf("unsupported scheme")
		}
	}
	return net.JoinHostPort(host, port), nil
}

func splitAuthority(authority string) (string, string, error) {
	authority = strings.TrimSpace(authority)
	if authority == "" || strings.ContainsAny(authority, "@/\\, \t\r\n") {
		return "", "", fmt.Errorf("malformed authority")
	}
	host := authority
	port := ""
	if strings.HasPrefix(authority, "[") {
		closing := strings.IndexByte(authority, ']')
		if closing < 0 {
			return "", "", fmt.Errorf("malformed IPv6 authority")
		}
		host = authority[1:closing]
		remainder := authority[closing+1:]
		if remainder != "" {
			if !strings.HasPrefix(remainder, ":") {
				return "", "", fmt.Errorf("malformed IPv6 authority")
			}
			port = strings.TrimPrefix(remainder, ":")
		}
	} else if parsedIP := net.ParseIP(authority); parsedIP == nil && strings.Contains(authority, ":") {
		var err error
		host, port, err = net.SplitHostPort(authority)
		if err != nil {
			return "", "", fmt.Errorf("malformed host and port")
		}
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return "", "", fmt.Errorf("host is empty")
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	} else {
		for _, r := range host {
			letter := r >= 'a' && r <= 'z'
			digit := r >= '0' && r <= '9'
			if r > 127 || !letter && !digit && r != '-' && r != '.' {
				return "", "", fmt.Errorf("host contains invalid characters")
			}
		}
	}
	if port != "" {
		parsed, err := strconv.ParseUint(port, 10, 16)
		if err != nil || parsed == 0 {
			return "", "", fmt.Errorf("port is invalid")
		}
		port = strconv.FormatUint(parsed, 10)
	}
	return host, port, nil
}

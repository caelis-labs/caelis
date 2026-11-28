package cli

import (
	"net"
	"strings"
)

// Only a listener bound exclusively to loopback can grant local Shell.
func localShellListen(address string) bool {
	address = strings.TrimSpace(address)
	if address == "" {
		return true
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback() || strings.EqualFold(host, "localhost")
}

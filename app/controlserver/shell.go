package controlserver

import (
	"net"
	"net/http"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver"
)

func (s *Server) shellRoutes() {
	service := s.config.Services.Shell
	if service == nil {
		return
	}
	s.mux.HandleFunc("POST "+apiPrefix+"/shell/execute", func(w http.ResponseWriter, r *http.Request) {
		principal, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		if !localShellRequest(r) {
			writeMappedError(w, appserver.ErrUnauthorized)
			return
		}
		var req appserver.ShellRequest
		if !decodeBodyLimit(w, r, &req, 256<<10) {
			return
		}
		result, err := service.ExecuteShell(r.Context(), principal, req)
		switch errorcode.CodeOf(err) {
		case errorcode.ResourceExhausted:
			writeErrorIdentity(w, http.StatusRequestEntityTooLarge, "Shell output limit exceeded", errorcode.ResourceExhausted, "")
		case errorcode.Overloaded:
			writeErrorIdentity(w, http.StatusTooManyRequests, "Shell execution limit reached", errorcode.Overloaded, "")
		case errorcode.Timeout:
			writeErrorIdentity(w, http.StatusGatewayTimeout, "Shell execution deadline exceeded", errorcode.Timeout, "")
		default:
			writeJSONResult(w, result, err)
		}
	})
}

// Check actual socket endpoints. Proxy headers only disqualify a request.
func localShellRequest(r *http.Request) bool {
	local, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr)
	remote, _, err := net.SplitHostPort(r.RemoteAddr)
	if !ok || !local.IP.IsLoopback() || err != nil || !net.ParseIP(remote).IsLoopback() {
		return false
	}
	for _, header := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-IP", "Via"} {
		if len(r.Header.Values(header)) != 0 {
			return false
		}
	}
	return true
}

package providers

import (
	"net/http"
	"strings"

	"github.com/caelis-labs/caelis/internal/version"
)

const (
	caelisOpenRouterReferer = "https://github.com/caelis-labs/caelis"
	caelisOpenRouterTitle   = "Caelis"
)

func applyDefaultAttributionHeaders(req *http.Request, api APIType) {
	if req == nil {
		return
	}
	setHeaderDefault(req.Header, "User-Agent", caelisUserAgent())
	if api == APIOpenRouter {
		setHeaderDefault(req.Header, "HTTP-Referer", caelisOpenRouterReferer)
		setHeaderDefault(req.Header, "X-Title", caelisOpenRouterTitle)
	}
}

func caelisUserAgent() string {
	value := strings.TrimSpace(version.String())
	value = strings.TrimPrefix(value, "v")
	if value == "" {
		value = "dev"
	}
	return "caelis/" + value
}

func setHeaderDefault(headers http.Header, key string, value string) {
	if headers == nil || strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
		return
	}
	if strings.TrimSpace(headers.Get(key)) != "" {
		return
	}
	headers.Set(key, value)
}

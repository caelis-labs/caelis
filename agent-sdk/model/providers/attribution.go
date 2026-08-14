package providers

import (
	"net/http"
	"runtime/debug"
	"strings"
	"sync/atomic"
)

const (
	caelisOpenRouterReferer = "https://github.com/caelis-labs/caelis"
	caelisOpenRouterTitle   = "Caelis"
)

var attributionBuildVersion atomic.Value

// SetAttributionBuildVersion configures the build version used when provider
// clients apply default HTTP attribution headers such as User-Agent. Host
// products should usually call this once during process startup before issuing
// provider requests. It is safe to call multiple times. Published versions such
// as v1.2.3 are normalized to caelis/1.2.3; dev, (devel), and empty values fall
// back to caelis/dev.
func SetAttributionBuildVersion(version string) {
	attributionBuildVersion.Store(version)
}

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
	value := normalizedCaelisBuildVersion(configuredAttributionBuildVersion())
	if value == "" {
		if info, ok := debug.ReadBuildInfo(); ok {
			value = normalizedCaelisBuildVersion(info.Main.Version)
		}
	}
	value = strings.TrimPrefix(value, "v")
	if value == "" {
		value = "dev"
	}
	return "caelis/" + value
}

func configuredAttributionBuildVersion() string {
	value := attributionBuildVersion.Load()
	if value == nil {
		return ""
	}
	return value.(string)
}

func normalizedCaelisBuildVersion(value string) string {
	value = strings.TrimSpace(value)
	switch value {
	case "", "(devel)", "dev":
		return ""
	default:
		return value
	}
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

package providers

import (
	"net/http"
	"testing"
)

func withAttributionBuildVersion(t *testing.T, version string) {
	t.Helper()
	oldVersion := configuredAttributionBuildVersion()
	SetAttributionBuildVersion(version)
	t.Cleanup(func() { SetAttributionBuildVersion(oldVersion) })
}

func TestSetAttributionBuildVersionNormalizesPublishedVersion(t *testing.T) {
	withAttributionBuildVersion(t, "v1.2.3")

	req, err := http.NewRequest(http.MethodPost, "https://example.com/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}

	applyDefaultAttributionHeaders(req, APIOpenRouter)

	if got := req.Header.Get("User-Agent"); got != "caelis/1.2.3" {
		t.Fatalf("User-Agent = %q, want caelis/1.2.3", got)
	}
	if got := req.Header.Get("HTTP-Referer"); got != caelisOpenRouterReferer {
		t.Fatalf("HTTP-Referer = %q, want %q", got, caelisOpenRouterReferer)
	}
	if got := req.Header.Get("X-Title"); got != caelisOpenRouterTitle {
		t.Fatalf("X-Title = %q, want %q", got, caelisOpenRouterTitle)
	}
}

func TestSetAttributionBuildVersionFallsBackToDev(t *testing.T) {
	cases := []struct {
		name    string
		version string
	}{
		{name: "dev", version: "dev"},
		{name: "devel", version: "(devel)"},
		{name: "empty", version: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withAttributionBuildVersion(t, tc.version)

			req, err := http.NewRequest(http.MethodPost, "https://example.com/chat/completions", nil)
			if err != nil {
				t.Fatal(err)
			}

			applyDefaultAttributionHeaders(req, APIOpenRouter)

			if got := req.Header.Get("User-Agent"); got != "caelis/dev" {
				t.Fatalf("User-Agent = %q, want caelis/dev", got)
			}
		})
	}
}

func TestApplyDefaultAttributionHeadersDefaultFallbackUsesDev(t *testing.T) {
	withAttributionBuildVersion(t, "")

	req, err := http.NewRequest(http.MethodPost, "https://example.com/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}

	applyDefaultAttributionHeaders(req, APIOpenAICompatible)

	if got := req.Header.Get("User-Agent"); got != "caelis/dev" {
		t.Fatalf("User-Agent = %q, want caelis/dev", got)
	}
}

func TestApplyDefaultAttributionHeadersPreservesExistingIdentity(t *testing.T) {
	withAttributionBuildVersion(t, "v9.9.9")

	req, err := http.NewRequest(http.MethodPost, "https://example.com/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", "custom-client/9.9.9")
	req.Header.Set("HTTP-Referer", "https://example.com/app")
	req.Header.Set("X-Title", "Example App")

	applyDefaultAttributionHeaders(req, APIOpenRouter)

	if got := req.Header.Get("User-Agent"); got != "custom-client/9.9.9" {
		t.Fatalf("User-Agent = %q, want configured value", got)
	}
	if got := req.Header.Get("HTTP-Referer"); got != "https://example.com/app" {
		t.Fatalf("HTTP-Referer = %q, want configured value", got)
	}
	if got := req.Header.Get("X-Title"); got != "Example App" {
		t.Fatalf("X-Title = %q, want configured value", got)
	}
}

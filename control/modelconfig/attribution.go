package modelconfig

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func providerUserAgent(build string) string {
	build = strings.TrimPrefix(strings.TrimSpace(build), "v")
	if build == "" || build == "(devel)" {
		build = "dev"
	}
	return "caelis/" + build
}

func providerAttributionHeaders(api model.APIType) map[string]string {
	switch api {
	case model.APIOpenRouter:
		return map[string]string{
			"HTTP-Referer": "https://github.com/caelis-labs/caelis",
			"X-Title":      "Caelis",
		}
	case model.APIOpenAICodex:
		return map[string]string{"originator": "caelis"}
	default:
		return nil
	}
}

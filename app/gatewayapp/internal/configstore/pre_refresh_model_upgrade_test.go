package configstore

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

// Frozen from f3019f24: AssembleConnect(codex, gpt-5.6-sol) with no-op OAuth,
// builder.FromProvider, then Store.Save with DefaultFastMode=true. Keep these
// bytes independent of the current catalog: AssembleConnect omitted image_input
// because the directory owns that capability; loading must still derive it.
// Removing default_fast_mode produces the same writer's non-Fast document and
// tests image capability independently of default Fast validation.
const preRefreshCodexFastConfig = `{
  "schema_version": 2,
  "configuration_revision": 1,
  "models": {
    "provider_endpoints": [
      {
        "id": "openai-codex@default",
        "provider": "openai-codex",
        "endpoint_id": "default",
        "base_url": "https://chatgpt.com/backend-api/codex",
        "credential_ref": "codex:default"
      }
    ],
    "configs": [
      {
        "id": "openai-codex@default/openai-codex/gpt-5.6-sol",
        "alias": "openai-codex/gpt-5.6-sol",
        "provider_endpoint_id": "openai-codex@default",
        "model": "gpt-5.6-sol",
        "context_window_tokens": 258400,
        "reasoning_effort": "low",
        "reasoning_levels": [
          "low",
          "medium",
          "high",
          "xhigh",
          "max",
          "ultra"
        ],
        "reasoning_mode": "effort",
        "max_output_tokens": 32768
      }
    ]
  },
  "external_agents": {},
  "model_profiles": {
    "default_profile_id": "provider:openai-codex@default/openai-codex/gpt-5.6-sol",
    "default_effort": "low",
    "default_fast_mode": true,
    "profiles": [
      {
        "id": "provider:openai-codex@default/openai-codex/gpt-5.6-sol",
        "display_name": "openai-codex/gpt-5.6-sol",
        "backend": {
          "provider": {
            "model_config_id": "openai-codex@default/openai-codex/gpt-5.6-sol"
          }
        },
        "effort": {
          "default_effort": "low",
          "choices": [
            {
              "canonical": "low",
              "wire_value": "low"
            },
            {
              "canonical": "medium",
              "wire_value": "medium"
            },
            {
              "canonical": "high",
              "wire_value": "high"
            },
            {
              "canonical": "xhigh",
              "wire_value": "xhigh"
            },
            {
              "canonical": "max",
              "wire_value": "max"
            },
            {
              "canonical": "ultra",
              "wire_value": "ultra"
            }
          ]
        },
        "speed": {
          "default_speed": "standard",
          "choices": [
            {
              "canonical": "standard",
              "wire_value": "default"
            },
            {
              "canonical": "fast",
              "wire_value": "priority"
            }
          ]
        }
      }
    ]
  },
  "agent_bindings": {},
  "sandbox": {},
  "runtime": {
    "approval_mode": "auto-review"
  }
}`

func TestStoreLoadsPreRefreshCodexConfigWithFastDefault(t *testing.T) {
	t.Parallel()

	checkPreRefreshCodexUpgrade(t, preRefreshCodexFastConfig, true)
}

// The without-Fast variant loads even when the maintained runtime metadata is
// missing, so it catches a lost image capability independently of the Fast
// validation gate.
func TestStoreLoadsPreRefreshCodexConfigWithoutFastDefault(t *testing.T) {
	t.Parallel()

	wire := strings.Replace(preRefreshCodexFastConfig, "    \"default_fast_mode\": true,\n", "", 1)
	checkPreRefreshCodexUpgrade(t, wire, false)
}

func checkPreRefreshCodexUpgrade(t *testing.T, wire string, wantFast bool) {
	t.Helper()

	if strings.Contains(wire, `"image_input"`) {
		t.Fatal("frozen fixture must not persist the catalogue-owned image_input override")
	}
	if got := strings.Contains(wire, `"default_fast_mode"`); got != wantFast {
		t.Fatalf("frozen fixture default_fast_mode presence = %t, want %t", got, wantFast)
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(wire), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	store := New(root)

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	checkLoaded := func(doc AppConfig) {
		t.Helper()
		if doc.SchemaVersion != SchemaVersionV2 {
			t.Fatalf("loaded schema version = %d, want %d", doc.SchemaVersion, SchemaVersionV2)
		}
		if doc.ModelProfiles.DefaultFastMode != wantFast {
			t.Fatalf("loaded default fast mode = %t, want %t", doc.ModelProfiles.DefaultFastMode, wantFast)
		}
		if wantFast {
			profile, ok := modelprofile.Lookup(doc.ModelProfiles, doc.ModelProfiles.DefaultProfileID)
			if !ok {
				t.Fatalf("loaded default profile %q not found", doc.ModelProfiles.DefaultProfileID)
			}
			if profile.Speed.DefaultSpeed != "standard" || !profile.SupportsFast() {
				t.Fatalf("loaded default profile speed = %#v, want derivable standard/fast capability", profile.Speed)
			}
		}
		assertCodexSolSDKImageCapability(t, preRefreshSolModelConfig(t, doc))
	}
	checkLoaded(loaded)

	if err := store.Save(loaded); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	reloaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() after Save error = %v", err)
	}
	checkLoaded(reloaded)
}

func preRefreshSolModelConfig(t *testing.T, doc AppConfig) modelconfig.Config {
	t.Helper()
	if len(doc.Models.Configs) != 1 {
		t.Fatalf("loaded provider models = %d, want 1", len(doc.Models.Configs))
	}
	cfg := modelconfig.NormalizeConfig(doc.Models.Configs[0])
	if cfg.Model != "gpt-5.6-sol" {
		t.Fatalf("loaded model = %q, want gpt-5.6-sol", cfg.Model)
	}
	if cfg.ImageInput != nil {
		t.Fatalf("loaded image_input override = %v, want catalogue-derived nil", *cfg.ImageInput)
	}
	for _, endpoint := range doc.Models.ProviderEndpoints {
		if endpoint.ID == cfg.ProviderEndpointID {
			cfg = modelconfig.MergeConfigProviderEndpoint(cfg, endpoint)
			break
		}
	}
	if cfg.Provider != "openai-codex" {
		t.Fatalf("loaded provider = %q, want endpoint-merged openai-codex", cfg.Provider)
	}
	return cfg
}

func assertCodexSolSDKImageCapability(t *testing.T, cfg modelconfig.Config) {
	t.Helper()
	cfg.HTTPClient = &http.Client{Transport: rejectNetworkRoundTripper{t}}
	resolution, err := modelconfig.BuildModel(cfg, 0, 0)
	if err != nil {
		t.Fatalf("BuildModel() error = %v", err)
	}
	capabilities, declared := model.CapabilitiesOf(resolution.Model)
	if !declared {
		t.Fatalf("SDK capabilities for %s = undeclared", cfg.Model)
	}
	if !capabilities.ImageInput {
		t.Fatalf("SDK image capability = false, want restored catalogue-derived image input for %s", cfg.Model)
	}
}

type rejectNetworkRoundTripper struct{ t *testing.T }

func (r rejectNetworkRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.t.Errorf("unexpected network request: %s %s", req.Method, req.URL)
	return nil, errors.New("configstore upgrade regression test disables network access")
}

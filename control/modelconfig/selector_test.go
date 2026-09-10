package modelconfig

import (
	"errors"
	"testing"
)

func TestPublicSelector(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"default", Config{Provider: "deepseek", Model: "deepseek-flash"}, "deepseek/deepseek-flash"},
		{"vision", Config{Provider: "deepseek", Model: "deepseek-v4-flash-vision-exp"}, "deepseek/deepseek-v4-flash-vision-exp"},
		{"api", Config{Provider: "xiaomi", EndpointID: "api-cn", Model: "mimo-v2.5-pro"}, "xiaomi@api-cn/mimo-v2.5-pro"},
		{"plan", Config{Provider: "xiaomi", EndpointID: "token-plan-cn", Model: "mimo-v2.5-pro"}, "xiaomi@token-plan-cn/mimo-v2.5-pro"},
		{"nested model", Config{Provider: "openai-compatible", EndpointID: "office", Model: "Org/Model:latest"}, "openai-compatible@office/org/model:latest"},
		{"custom alias", Config{Provider: "deepseek", Model: "deepseek-flash", Alias: "fast"}, "deepseek@default/fast"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := NormalizeConfig(tt.cfg)
			if got := PublicSelector(cfg); got != tt.want {
				t.Fatalf("PublicSelector = %q, want %q", got, tt.want)
			}
			for _, ref := range []string{tt.want, cfg.ID, cfg.Alias} {
				got, ok, err := ResolveSelector([]Config{cfg}, ref)
				if err != nil || !ok || got.ID != cfg.ID || got.Model != tt.cfg.Model {
					t.Fatalf("ResolveSelector(%q) = %#v, %v, %v", ref, got, ok, err)
				}
			}
		})
	}
}

func TestPublicSelectorsKeepEndpointIdentity(t *testing.T) {
	api := NormalizeConfig(Config{Provider: "xiaomi", EndpointID: "api-cn", Model: "mimo-v2.5-pro"})
	plan := NormalizeConfig(Config{Provider: "xiaomi", EndpointID: "token-plan-cn", Model: "mimo-v2.5-pro"})
	for _, configs := range [][]Config{{api}, {plan}, {api, plan}, {plan, api}} {
		if err := ValidatePublicSelectors(configs); err != nil {
			t.Fatal(err)
		}
		for _, cfg := range configs {
			got, ok, err := ResolveSelector(configs, PublicSelector(cfg))
			if err != nil || !ok || got.ID != cfg.ID {
				t.Fatalf("ResolveSelector = %#v, %v, %v", got, ok, err)
			}
		}
	}
	if _, _, err := ResolveSelector([]Config{api, plan}, api.Alias); !errors.Is(err, ErrAmbiguousSelector) {
		t.Fatalf("duplicate alias error = %v", err)
	}
}

func TestPublicSelectorsTakePrecedenceOverCompatibilityAliases(t *testing.T) {
	standard := NormalizeConfig(Config{Provider: "deepseek", Model: "deepseek-flash"})
	custom := NormalizeConfig(Config{Provider: "deepseek", Model: "deepseek-flash", EndpointID: "office"})
	for _, configs := range [][]Config{{standard}, {custom, standard}, {standard, custom}} {
		if err := ValidatePublicSelectors(configs); err != nil {
			t.Fatalf("valid multi-endpoint catalog rejected: %v", err)
		}
		for _, cfg := range configs {
			for _, ref := range []string{cfg.ID, PublicSelector(cfg)} {
				got, ok, err := ResolveSelector(configs, ref)
				if err != nil || !ok || got.ID != cfg.ID {
					t.Fatalf("resolve(%q) = %#v, %v, %v; want %q", ref, got, ok, err, cfg.ID)
				}
			}
		}
	}
	// A user alias cannot override the canonical default route either.
	variant := NormalizeConfig(Config{Provider: "deepseek", Model: "other-model", Alias: standard.Alias, EndpointID: "office"})
	got, ok, err := ResolveSelector([]Config{variant, standard}, standard.Alias)
	if err != nil || !ok || got.ID != standard.ID {
		t.Fatalf("alias shadowed public selector: %#v, %v, %v", got, ok, err)
	}
}

func TestPublicSelectorRejectsCollisionWithDurableID(t *testing.T) {
	plain := NormalizeConfig(Config{Provider: "xiaomi", EndpointID: "api-cn", Model: "m"})
	nested := NormalizeConfig(Config{Provider: "xiaomi", EndpointID: "api-cn", Model: "xiaomi/m"})
	configs := []Config{plain, nested}
	if PublicSelector(nested) != plain.ID {
		t.Fatal("fixture must collide with a durable ID")
	}
	if err := ValidatePublicSelectors(configs); !errors.Is(err, ErrAmbiguousSelector) {
		t.Fatalf("catalog collision error = %v", err)
	}
	got, ok, err := ResolveSelector(configs, plain.ID)
	if err != nil || !ok || got.ID != plain.ID {
		t.Fatalf("exact durable ID lost precedence: %#v, %v, %v", got, ok, err)
	}
}

func TestPublicSelectorCustomVariantsAndUnknownInput(t *testing.T) {
	standard := NormalizeConfig(Config{Provider: "deepseek", Model: "deepseek-flash"})
	variant := NormalizeConfig(Config{Provider: "deepseek", Model: "deepseek-flash", Alias: "fast"})
	configs := []Config{standard, variant}
	if err := ValidatePublicSelectors(configs); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"", "unknown", "other/deepseek-flash"} {
		if _, ok, err := ResolveSelector(configs, ref); ok || err != nil {
			t.Fatalf("unknown selector %q = %v, %v", ref, ok, err)
		}
	}
	custom := NormalizeConfig(Config{Provider: "deepseek", BaseURL: "https://example.invalid/v1", Model: "deepseek-flash"})
	if got := PublicSelector(custom); got != custom.ProviderEndpointID+"/deepseek-flash" {
		t.Fatalf("custom URL selector = %q", got)
	}
}

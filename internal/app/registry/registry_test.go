package registry

import (
	"context"
	"errors"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/plugin"
	"github.com/OnslaughtSnail/caelis/core/session"
	storememory "github.com/OnslaughtSnail/caelis/internal/adapters/store/memory"
	appresources "github.com/OnslaughtSnail/caelis/internal/app/resources"
)

func TestRegistryRegistersFactoriesAndRejectsDuplicates(t *testing.T) {
	reg := New()
	factory := func(context.Context, plugin.StoreConfig) (session.Store, error) {
		return storememory.New(), nil
	}
	if err := reg.RegisterStore("custom", factory); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterStore("custom", factory); err == nil {
		t.Fatal("duplicate store registration error = nil, want error")
	}
	got, ok := reg.Store(" CUSTOM ")
	if !ok {
		t.Fatal("Store custom not found")
	}
	store, err := got(context.Background(), plugin.StoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.(*storememory.Store); !ok {
		t.Fatalf("store = %T, want memory store", store)
	}
}

func TestRegistryAppliesContribution(t *testing.T) {
	reg := New()
	err := reg.Apply(context.Background(), contributionFunc(func(_ context.Context, target plugin.Registry) error {
		if err := target.RegisterPrompt(plugin.PromptFragment{ID: "prompt", Text: "hello"}); err != nil {
			return err
		}
		return target.RegisterSkill(plugin.SkillDescriptor{Name: "skill"})
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Prompts()) != 1 || reg.Prompts()[0].ID != "prompt" {
		t.Fatalf("prompts = %#v, want prompt", reg.Prompts())
	}
	if len(reg.Skills()) != 1 || reg.Skills()[0].Name != "skill" {
		t.Fatalf("skills = %#v, want skill", reg.Skills())
	}
}

func TestRegistryAppliesCatalogAliases(t *testing.T) {
	reg, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	err = reg.ApplyCatalog(appresources.Catalog{
		Stores: []plugin.FactoryAlias{{Name: "project-memory", Uses: "memory"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	factory, ok := reg.Store("project-memory")
	if !ok {
		t.Fatal("store alias project-memory not found")
	}
	store, err := factory(context.Background(), plugin.StoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.(*storememory.Store); !ok {
		t.Fatalf("store = %T, want memory store", store)
	}
}

func TestRegistryRejectsCatalogAliasWithUnknownFactory(t *testing.T) {
	reg := New()
	err := reg.ApplyCatalog(appresources.Catalog{
		Stores: []plugin.FactoryAlias{{Name: "missing", Uses: "nope"}},
	})
	if err == nil {
		t.Fatal("ApplyCatalog unknown alias error = nil, want error")
	}
}

func TestRegistryApplyReturnsContributionError(t *testing.T) {
	reg := New()
	want := errors.New("boom")
	err := reg.Apply(context.Background(), contributionFunc(func(context.Context, plugin.Registry) error {
		return want
	}))
	if !errors.Is(err, want) {
		t.Fatalf("Apply error = %v, want %v", err, want)
	}
}

type contributionFunc func(context.Context, plugin.Registry) error

func (f contributionFunc) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: "test"}
}

func (f contributionFunc) Register(ctx context.Context, registry plugin.Registry) error {
	return f(ctx, registry)
}

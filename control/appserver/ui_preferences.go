package appserver

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/uipreferences"
)

// UIPreferencesStore stores presentation preferences independently of Sessions.
// SaveUIPreferences merges nonzero fields from the argument into the persisted
// UI document. It must preserve omitted UI fields and concurrent product settings.
type UIPreferencesStore interface {
	LoadUIPreferences(context.Context) (uipreferences.Preferences, error)
	SaveUIPreferences(context.Context, uipreferences.Preferences) error
}

// UIPreferencesService exposes Host UI preferences to authenticated surfaces.
type UIPreferencesService struct{ Store UIPreferencesStore }

// Load returns the persisted preferences with presentation defaults applied.
func (s *UIPreferencesService) Load(ctx context.Context, p Principal) (uipreferences.Preferences, error) {
	if strings.TrimSpace(p.ID) == "" {
		return uipreferences.Preferences{}, ErrUnauthorized
	}
	if s == nil || s.Store == nil {
		return uipreferences.Preferences{}, errorcode.New(errorcode.Unavailable, "UI preferences unavailable")
	}
	return s.Store.LoadUIPreferences(ctx)
}

// Save records a sparse presentation update without modifying a Session.
// Omitted and zero fields leave stored values unchanged. Validation runs on the
// update itself; defaults are not written unless the caller sends them.
func (s *UIPreferencesService) Save(ctx context.Context, p Principal, value uipreferences.Preferences) error {
	if strings.TrimSpace(p.ID) == "" {
		return ErrUnauthorized
	}
	if s == nil || s.Store == nil {
		return errorcode.New(errorcode.Unavailable, "UI preferences unavailable")
	}
	if err := value.Validate(); err != nil {
		return errorcode.New(errorcode.InvalidArgument, err.Error())
	}
	return s.Store.SaveUIPreferences(ctx, value)
}

// UIPreferencesClient is the principal-bound presentation preference contract.
type UIPreferencesClient interface {
	LoadUIPreferences(context.Context) (uipreferences.Preferences, error)
	SaveUIPreferences(context.Context, uipreferences.Preferences) error
}

type boundUIPreferencesClient struct {
	service   *UIPreferencesService
	principal Principal
}

func (c *boundUIPreferencesClient) LoadUIPreferences(ctx context.Context) (uipreferences.Preferences, error) {
	return c.service.Load(ctx, c.principal)
}
func (c *boundUIPreferencesClient) SaveUIPreferences(ctx context.Context, p uipreferences.Preferences) error {
	return c.service.Save(ctx, c.principal, p)
}

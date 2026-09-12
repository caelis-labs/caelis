package gatewayapp

import (
	"context"
	"errors"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/uipreferences"
)

func (s *appConfigStore) LoadUIPreferences(ctx context.Context) (uipreferences.Preferences, error) {
	doc, err := s.LoadContext(ctx)
	return doc.UI.WithDefaults(), err
}

func (s *appConfigStore) SaveUIPreferences(ctx context.Context, p uipreferences.Preferences) error {
	if err := p.Validate(); err != nil {
		return err
	}
	for {
		doc, err := s.LoadContext(ctx)
		if err != nil {
			return err
		}
		merged := doc.UI.Merge(p)
		if doc.UI == merged {
			return nil
		}
		doc.UI = merged
		_, err = s.CompareAndSave(ctx, doc.ConfigurationRevision, doc)
		// A proven pre-write revision conflict can be retried: reload product
		// configuration and unrelated UI fields, then merge the same sparse update.
		if !errors.Is(err, configstore.ErrConfigurationRevisionConflict) {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

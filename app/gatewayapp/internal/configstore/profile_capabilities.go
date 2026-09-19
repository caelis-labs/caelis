package configstore

import (
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/control/modelprofile/builder"
)

// refreshProviderSpeeds derives provider capabilities from the current endpoint
// and catalog. ACP capabilities remain peer-discovered snapshots and are checked
// again by session configuration before execution.
func refreshProviderSpeeds(doc *AppConfig) {
	for index, profile := range doc.ModelProfiles.Profiles {
		if profile.Kind() != modelprofile.BackendProvider {
			continue
		}
		for _, configured := range doc.Models.Configs {
			if configured.ID != profile.Backend.Provider.ModelConfigID {
				continue
			}
			for _, endpoint := range doc.Models.ProviderEndpoints {
				if endpoint.ID == configured.ProviderEndpointID {
					configured = modelconfig.MergeConfigProviderEndpoint(configured, endpoint)
					break
				}
			}
			fresh, err := builder.FromProvider(configured)
			if err == nil {
				doc.ModelProfiles.Profiles[index].Speed = fresh.Speed
			}
			break
		}
	}
}

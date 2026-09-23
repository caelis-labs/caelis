package controlserver

import (
	"github.com/caelis-labs/caelis/control/appserver"
	"slices"
)

// Discovery and initialize use the same assembled capability projection.
func botServerInfo(info appserver.ServerInfo, services appserver.AppServerServices) appserver.ServerInfo {
	info = normalizeServerInfo(info)
	if services.BotWork == nil || services.BotWork.Store == nil {
		return info
	}
	for _, capability := range []string{appserver.CapabilityBotManagedWork, appserver.CapabilityBotFiles, appserver.CapabilityBotDesktop, appserver.CapabilityBotReminders, appserver.CapabilityBotImages, appserver.CapabilityBotTextResults} {
		if !slices.Contains(info.Capabilities, capability) {
			info.Capabilities = append(info.Capabilities, capability)
		}
	}
	info.StoreID = services.BotWork.Store.StoreID
	info.InstanceID = services.BotWork.Store.InstanceID
	return info
}

package application

// Focused capabilities let an application negotiate functionality without
// destructive Session creation probes. They do not grant execution authority.
const (
	CapabilityModelCapabilities    = "application-model-capabilities-v1"
	CapabilityHotConfiguration     = "application-hot-configuration-v1"
	CapabilityNativeExecution      = "application-native-execution-v1"
	CapabilityWorkspaceBinding     = "application-workspace-binding-v1"
	CapabilityBackgroundActivation = "application-background-activation-v1"
	CapabilityResourceTransfer     = "application-resource-transfer-v1"
	CapabilityToolResultContent    = "application-tool-result-content-v1"
	CapabilityMediaResources       = "application-media-resources-v1"
	CapabilityAtomicCapabilities   = "application-atomic-capabilities-v1"
)

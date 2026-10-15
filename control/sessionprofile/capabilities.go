package sessionprofile

// Focused capabilities let an application negotiate functionality without
// destructive Session creation probes. They do not grant execution authority.
const (
	CapabilityModelCapabilities    = "session-model-capabilities-v1"
	CapabilityHotConfiguration     = "session-hot-configuration-v1"
	CapabilityNativeExecution      = "session-native-execution-v1"
	CapabilityWorkspaceBinding     = "session-workspace-binding-v1"
	CapabilityBackgroundActivation = "session-background-activation-v1"
	CapabilityResourceTransfer     = "session-resource-transfer-v1"
	CapabilityToolResultContent    = "session-tool-result-content-v1"
	CapabilityMediaResources       = "session-media-resources-v1"
	CapabilityAtomicCapabilities   = "session-atomic-capabilities-v1"
)

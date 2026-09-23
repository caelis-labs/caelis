package application

// Focused capabilities let an application negotiate functionality without
// destructive Session creation probes. They do not grant execution authority.
const (
	CapabilityHotConfiguration     = "application-hot-configuration-v1"
	CapabilityNativeExecution      = "application-native-execution-v1"
	CapabilityWorkspaceBinding     = "application-workspace-binding-v1"
	CapabilityBackgroundActivation = "application-background-activation-v1"
	CapabilityResourceTransfer     = "application-resource-transfer-v1"
)

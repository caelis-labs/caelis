package client

import "github.com/caelis-labs/caelis/protocol/acp/schema"

// SupportsSessionSteering validates the interoperable steering advertisement
// from one initialize response without mutating or caching peer metadata.
func SupportsSessionSteering(response InitializeResponse) (bool, error) {
	capability, err := schema.DecodeSessionSteeringCapability(response.Meta)
	return capability.Supported, err
}

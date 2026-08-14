package controlclient

import "testing"

func TestCompatibilityPolicyIsIndependentFromDistributionIdentity(t *testing.T) {
	policy := CurrentCompatibility(CapabilityAppServerClients)
	base := ServerInfo{
		ProtocolVersion: policy.ProtocolVersions[0], EnvelopeVersion: policy.EnvelopeVersions[0], APIVersion: policy.APIVersions[0],
		Capabilities: []string{CapabilityAppServerClients},
	}
	for _, distribution := range []string{"v1.0.0", "v99.0.0"} {
		info := base
		info.DistributionVersion = distribution
		info.BuildID = distribution + "-build"
		if err := policy.Accept(info); err != nil {
			t.Fatalf("Accept(%q) = %v", distribution, err)
		}
	}
}

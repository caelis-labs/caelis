package appserver

import (
	"fmt"
	"slices"
	"strings"

	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

// CompatibilityPolicy is owned by a Surface and declares the server contract
// versions and capabilities that Surface can consume. Distribution version and
// BuildID intentionally do not participate in protocol compatibility.
type CompatibilityPolicy struct {
	ProtocolVersions []int
	EnvelopeVersions []string
	APIVersions      []string
	RequiredCaps     []string
}

// CurrentCompatibility returns the policy implemented by this source tree.
func CurrentCompatibility(requiredCapabilities ...string) CompatibilityPolicy {
	return CompatibilityPolicy{
		ProtocolVersions: []int{schema.CurrentProtocolVersion},
		EnvelopeVersions: []string{EnvelopeVersion},
		APIVersions:      []string{HTTPAPIVersion},
		RequiredCaps:     append([]string(nil), requiredCapabilities...),
	}
}

// Accept validates one server handshake against this Surface policy.
func (p CompatibilityPolicy) Accept(info ServerInfo) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if !slices.Contains(p.ProtocolVersions, info.ProtocolVersion) {
		return fmt.Errorf("controlclient: unsupported ACP protocol version %d", info.ProtocolVersion)
	}
	if !slices.Contains(p.EnvelopeVersions, strings.TrimSpace(info.EnvelopeVersion)) {
		return fmt.Errorf("controlclient: unsupported Envelope version %q", info.EnvelopeVersion)
	}
	if !slices.Contains(p.APIVersions, strings.TrimSpace(info.APIVersion)) {
		return fmt.Errorf("controlclient: unsupported Control API version %q", info.APIVersion)
	}
	for _, capability := range p.RequiredCaps {
		capability = strings.TrimSpace(capability)
		if capability != "" && !slices.Contains(info.Capabilities, capability) {
			return fmt.Errorf("controlclient: missing required capability %q", capability)
		}
	}
	return nil
}

// Validate rejects implicit or partial Surface compatibility policy.
func (p CompatibilityPolicy) Validate() error {
	if len(p.ProtocolVersions) == 0 || len(p.EnvelopeVersions) == 0 || len(p.APIVersions) == 0 {
		return fmt.Errorf("controlclient: Surface compatibility policy is incomplete")
	}
	return nil
}

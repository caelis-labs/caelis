package agents

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// PreparationState is the durable progress of one external ACP onboarding
// preparation. Preparation never represents the final roster commit.
type PreparationState string

const (
	PreparationStatePlanned   PreparationState = "planned"
	PreparationStateNeedsAuth PreparationState = "needs_auth"
	PreparationStateReady     PreparationState = "ready"
)

// Valid reports whether state is one maintained preparation state.
func (s PreparationState) Valid() bool {
	switch s {
	case PreparationStatePlanned, PreparationStateNeedsAuth, PreparationStateReady:
		return true
	default:
		return false
	}
}

// ACPPrepareRequest is the public, secret-free request used to start external
// ACP onboarding. Config values, discovery snapshots, and authentication
// selections enter only through later revision-bound preparation steps.
type ACPPrepareRequest struct {
	AdapterID   string         `json:"adapter_id,omitempty"`
	Launcher    LauncherChoice `json:"launcher,omitempty"`
	CommandLine string         `json:"command_line,omitempty"`
	ModelID     string         `json:"model_id,omitempty"`
	CWD         string         `json:"cwd,omitempty"`
	ParentRef   string         `json:"parent_ref,omitempty"`
}

// NormalizeACPPrepareRequest returns a detached canonical preparation request.
func NormalizeACPPrepareRequest(in ACPPrepareRequest) ACPPrepareRequest {
	request := NormalizeConnectRequest(ConnectRequest{
		AdapterID:   in.AdapterID,
		Launcher:    in.Launcher,
		CommandLine: in.CommandLine,
		ModelID:     in.ModelID,
		CWD:         in.CWD,
	})
	return ACPPrepareRequest{
		AdapterID:   request.AdapterID,
		Launcher:    request.Launcher,
		CommandLine: request.CommandLine,
		ModelID:     request.ModelID,
		CWD:         request.CWD,
		ParentRef:   strings.TrimSpace(in.ParentRef),
	}
}

// AuthenticationChallengeMethod is the wire-safe identity of one ACP
// authentication method. Terminal arguments and environment values stay in
// the live discovery process and are deliberately not durable challenge data.
type AuthenticationChallengeMethod struct {
	ID          string             `json:"id"`
	Name        string             `json:"name,omitempty"`
	Description string             `json:"description,omitempty"`
	Type        AuthenticationType `json:"type"`
}

// ACPAuthenticationChallenge is the exact preparation revision for which a
// client may choose one declared authentication method.
type ACPAuthenticationChallenge struct {
	PreparationRef string                          `json:"preparation_ref"`
	ContentDigest  string                          `json:"content_digest"`
	Methods        []AuthenticationChallengeMethod `json:"methods"`
	ExpiresAt      time.Time                       `json:"expires_at"`
}

// ACPPreparation is the durable, secret-free observation produced before one
// external ACP roster mutation. Trusted ownership fields are persisted by the
// Host but never exposed on the product wire.
type ACPPreparation struct {
	Ref                    string                          `json:"ref"`
	State                  PreparationState                `json:"state"`
	PrincipalID            string                          `json:"-"`
	OperationID            string                          `json:"-"`
	IntentDigest           string                          `json:"-"`
	ParentRef              string                          `json:"parent_ref,omitempty"`
	Request                ACPPrepareRequest               `json:"request"`
	Connection             Connection                      `json:"connection,omitzero"`
	ObservedRevision       uint64                          `json:"observed_revision"`
	Discovery              DiscoverySnapshot               `json:"discovery,omitzero"`
	AuthenticationMethods  []AuthenticationChallengeMethod `json:"authentication_methods,omitempty"`
	SelectedAuthentication Authentication                  `json:"selected_authentication,omitzero"`
	CreatedAt              time.Time                       `json:"created_at"`
	ExpiresAt              time.Time                       `json:"expires_at"`
	ContentDigest          string                          `json:"content_digest"`
	CleanupWarning         string                          `json:"cleanup_warning,omitempty"`
}

// NormalizeACPPreparation returns detached canonical preparation data without
// changing its content digest.
func NormalizeACPPreparation(in ACPPreparation) ACPPreparation {
	request := NormalizeACPPrepareRequest(in.Request)
	connection := Connection{}
	if acpPreparationConnectionPresent(in.Connection) {
		connection = NormalizeConnection(in.Connection)
	}
	parentRef := strings.TrimSpace(in.ParentRef)
	if parentRef == "" {
		parentRef = request.ParentRef
	}
	request.ParentRef = parentRef
	out := ACPPreparation{
		Ref:                    strings.TrimSpace(in.Ref),
		State:                  PreparationState(strings.ToLower(strings.TrimSpace(string(in.State)))),
		PrincipalID:            strings.TrimSpace(in.PrincipalID),
		OperationID:            strings.TrimSpace(in.OperationID),
		IntentDigest:           strings.ToLower(strings.TrimSpace(in.IntentDigest)),
		ParentRef:              parentRef,
		Request:                request,
		Connection:             connection,
		ObservedRevision:       in.ObservedRevision,
		Discovery:              NormalizeDiscoverySnapshot(in.Discovery),
		SelectedAuthentication: NormalizeAuthentication(in.SelectedAuthentication),
		CreatedAt:              normalizePreparationTime(in.CreatedAt),
		ExpiresAt:              normalizePreparationTime(in.ExpiresAt),
		ContentDigest:          strings.ToLower(strings.TrimSpace(in.ContentDigest)),
		CleanupWarning:         strings.TrimSpace(in.CleanupWarning),
	}
	if !out.Discovery.DiscoveredAt.IsZero() {
		out.Discovery.DiscoveredAt = normalizePreparationTime(out.Discovery.DiscoveredAt)
	}
	seenMethods := map[string]struct{}{}
	for _, raw := range in.AuthenticationMethods {
		method := normalizeAuthenticationChallengeMethod(raw)
		if method.ID == "" {
			continue
		}
		key := strings.ToLower(method.ID)
		if _, ok := seenMethods[key]; ok {
			continue
		}
		seenMethods[key] = struct{}{}
		out.AuthenticationMethods = append(out.AuthenticationMethods, method)
	}
	return out
}

// SealACPPreparation normalizes preparation, computes its content digest, and
// validates the resulting durable record.
func SealACPPreparation(in ACPPreparation) (ACPPreparation, error) {
	out := NormalizeACPPreparation(in)
	digest, err := ACPPreparationContentDigest(out)
	if err != nil {
		return ACPPreparation{}, err
	}
	out.ContentDigest = digest
	if err := ValidateACPPreparation(out); err != nil {
		return ACPPreparation{}, err
	}
	return out, nil
}

// ACPPreparationContentDigest returns the canonical digest used for exact
// preparation updates. Trusted ownership is included even though it is hidden
// from the wire representation.
func ACPPreparationContentDigest(in ACPPreparation) (string, error) {
	in = NormalizeACPPreparation(in)
	in.ContentDigest = ""
	material := struct {
		Ref                    string                          `json:"ref"`
		State                  PreparationState                `json:"state"`
		PrincipalID            string                          `json:"principal_id"`
		OperationID            string                          `json:"operation_id"`
		IntentDigest           string                          `json:"intent_digest"`
		ParentRef              string                          `json:"parent_ref,omitempty"`
		Request                ACPPrepareRequest               `json:"request"`
		Connection             Connection                      `json:"connection,omitzero"`
		ObservedRevision       uint64                          `json:"observed_revision"`
		Discovery              DiscoverySnapshot               `json:"discovery,omitzero"`
		AuthenticationMethods  []AuthenticationChallengeMethod `json:"authentication_methods,omitempty"`
		SelectedAuthentication Authentication                  `json:"selected_authentication,omitzero"`
		CreatedAt              time.Time                       `json:"created_at"`
		ExpiresAt              time.Time                       `json:"expires_at"`
		CleanupWarning         string                          `json:"cleanup_warning,omitempty"`
	}{
		Ref: in.Ref, State: in.State, PrincipalID: in.PrincipalID,
		OperationID: in.OperationID, IntentDigest: in.IntentDigest,
		ParentRef: in.ParentRef, Request: in.Request, Connection: in.Connection,
		ObservedRevision: in.ObservedRevision, Discovery: in.Discovery,
		AuthenticationMethods:  in.AuthenticationMethods,
		SelectedAuthentication: in.SelectedAuthentication,
		CreatedAt:              in.CreatedAt, ExpiresAt: in.ExpiresAt,
		CleanupWarning: in.CleanupWarning,
	}
	payload, err := json.Marshal(material)
	if err != nil {
		return "", fmt.Errorf("control/agents: encode ACP preparation digest: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

// ValidateACPPreparation validates durable state, ownership, observation, and
// digest invariants. Expiration relative to wall clock is enforced by the Host
// store rather than this deterministic DTO validator.
func ValidateACPPreparation(in ACPPreparation) error {
	in = NormalizeACPPreparation(in)
	if err := validateACPPreparationRef(in.Ref); err != nil {
		return err
	}
	if in.ParentRef != "" {
		if err := validateACPPreparationRef(in.ParentRef); err != nil {
			return fmt.Errorf("control/agents: invalid parent preparation: %w", err)
		}
		if in.ParentRef == in.Ref {
			return errors.New("control/agents: ACP preparation must not parent itself")
		}
	}
	if err := ValidateACPPrepareRequest(in.Request); err != nil {
		return err
	}
	if in.Request.ParentRef != in.ParentRef {
		return errors.New("control/agents: ACP preparation request parent does not match preparation parent")
	}
	if in.PrincipalID == "" {
		return errors.New("control/agents: ACP preparation principal is required")
	}
	if in.OperationID == "" {
		return errors.New("control/agents: ACP preparation operation is required")
	}
	if !validSHA256Hex(in.IntentDigest) {
		return errors.New("control/agents: ACP preparation intent digest must be SHA-256 hex")
	}
	if !in.State.Valid() {
		return fmt.Errorf("control/agents: unsupported ACP preparation state %q", in.State)
	}
	if in.CreatedAt.IsZero() || in.ExpiresAt.IsZero() || !in.ExpiresAt.After(in.CreatedAt) {
		return errors.New("control/agents: ACP preparation requires an increasing creation and expiration window")
	}
	if err := validateAuthenticationChallengeMethods(in.AuthenticationMethods); err != nil {
		return err
	}
	if err := ValidateAuthentication(in.SelectedAuthentication); err != nil {
		return fmt.Errorf("control/agents: invalid selected ACP authentication: %w", err)
	}
	discoveryPresent := acpPreparationDiscoveryPresent(in.Discovery)
	switch in.State {
	case PreparationStatePlanned:
		if acpPreparationConnectionPresent(in.Connection) {
			if err := ValidateConnection(in.Connection); err != nil {
				return fmt.Errorf("control/agents: invalid ACP preparation connection: %w", err)
			}
		}
		if discoveryPresent || len(in.AuthenticationMethods) != 0 || in.SelectedAuthentication.MethodID != "" {
			return errors.New("control/agents: planned ACP preparation must not contain discovery or authentication results")
		}
	case PreparationStateNeedsAuth:
		if err := ValidateConnection(in.Connection); err != nil {
			return fmt.Errorf("control/agents: invalid ACP preparation connection: %w", err)
		}
		if discoveryPresent || len(in.AuthenticationMethods) == 0 || in.SelectedAuthentication.MethodID != "" || in.Connection.Authentication.MethodID != "" {
			return errors.New("control/agents: needs_auth ACP preparation requires only an unresolved authentication challenge")
		}
	case PreparationStateReady:
		if err := ValidateConnection(in.Connection); err != nil {
			return fmt.Errorf("control/agents: invalid ACP preparation connection: %w", err)
		}
		if !discoveryPresent {
			return errors.New("control/agents: ready ACP preparation requires normalized discovery")
		}
		if err := validateReadyACPPreparation(in); err != nil {
			return err
		}
	}
	if !validSHA256Hex(in.ContentDigest) {
		return errors.New("control/agents: ACP preparation content digest must be SHA-256 hex")
	}
	want, err := ACPPreparationContentDigest(in)
	if err != nil {
		return err
	}
	if in.ContentDigest != want {
		return errors.New("control/agents: ACP preparation content digest does not match its content")
	}
	return nil
}

// ValidateACPPrepareRequest validates a secret-free request before any
// launcher resolution or installation effect is attempted.
func ValidateACPPrepareRequest(in ACPPrepareRequest) error {
	in = NormalizeACPPrepareRequest(in)
	if in.AdapterID == "" {
		return errors.New("control/agents: ACP preparation adapter is required")
	}
	if !IsName(in.AdapterID) {
		return errors.New("control/agents: ACP preparation adapter must use a canonical name")
	}
	switch in.Launcher {
	case LauncherChoiceNPX, LauncherChoiceGlobal, LauncherChoiceManaged, LauncherChoiceInstalled, LauncherChoiceHosted:
		if in.CommandLine != "" {
			return errors.New("control/agents: ACP preparation command line requires the command launcher")
		}
	case LauncherChoiceCommand:
		if in.CommandLine == "" {
			return errors.New("control/agents: ACP preparation command launcher requires a command line")
		}
	default:
		return fmt.Errorf("control/agents: unsupported ACP preparation launcher %q", in.Launcher)
	}
	if in.ParentRef != "" {
		if err := validateACPPreparationRef(in.ParentRef); err != nil {
			return fmt.Errorf("control/agents: invalid ACP preparation request parent: %w", err)
		}
	}
	return nil
}

// AuthenticationChallenge returns the public authentication choice bound to
// a needs_auth preparation revision.
func (p ACPPreparation) AuthenticationChallenge() (ACPAuthenticationChallenge, error) {
	p = NormalizeACPPreparation(p)
	if err := ValidateACPPreparation(p); err != nil {
		return ACPAuthenticationChallenge{}, err
	}
	if p.State != PreparationStateNeedsAuth {
		return ACPAuthenticationChallenge{}, errors.New("control/agents: ACP preparation does not need authentication")
	}
	return ACPAuthenticationChallenge{
		PreparationRef: p.Ref,
		ContentDigest:  p.ContentDigest,
		Methods:        append([]AuthenticationChallengeMethod(nil), p.AuthenticationMethods...),
		ExpiresAt:      p.ExpiresAt,
	}, nil
}

func normalizeAuthenticationChallengeMethod(in AuthenticationChallengeMethod) AuthenticationChallengeMethod {
	out := AuthenticationChallengeMethod{
		ID:          strings.TrimSpace(in.ID),
		Name:        strings.TrimSpace(in.Name),
		Description: strings.TrimSpace(in.Description),
		Type:        AuthenticationType(strings.ToLower(strings.TrimSpace(string(in.Type)))),
	}
	if out.Type == "" {
		out.Type = AuthenticationAgent
	}
	return out
}

func validateAuthenticationChallengeMethods(methods []AuthenticationChallengeMethod) error {
	seen := map[string]struct{}{}
	for _, raw := range methods {
		method := normalizeAuthenticationChallengeMethod(raw)
		if method.ID == "" {
			return errors.New("control/agents: ACP authentication challenge method id is required")
		}
		switch method.Type {
		case AuthenticationAgent, AuthenticationTerminal:
		default:
			return fmt.Errorf("control/agents: unsupported ACP authentication challenge type %q", method.Type)
		}
		key := strings.ToLower(method.ID)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("control/agents: duplicate ACP authentication challenge method %q", method.ID)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateReadyACPPreparation(in ACPPreparation) error {
	discovery := NormalizeDiscoverySnapshot(in.Discovery)
	if discovery.ConnectionID != in.Connection.ID {
		return errors.New("control/agents: ready ACP preparation discovery belongs to another connection")
	}
	if discovery.LaunchFingerprint != LaunchFingerprint(in.Connection.Launcher) {
		return errors.New("control/agents: ready ACP preparation discovery has a stale launcher fingerprint")
	}
	if discovery.DiscoveredAt.IsZero() {
		return errors.New("control/agents: ready ACP preparation discovery time is required")
	}
	selected := NormalizeAuthentication(in.SelectedAuthentication)
	if len(in.AuthenticationMethods) > 0 && selected.MethodID == "" {
		return errors.New("control/agents: ready ACP preparation must resolve its authentication challenge")
	}
	if selected.MethodID != "" && len(in.AuthenticationMethods) > 0 {
		matched := false
		for _, method := range in.AuthenticationMethods {
			method = normalizeAuthenticationChallengeMethod(method)
			if method.ID == selected.MethodID && method.Type == selected.Type {
				matched = true
				break
			}
		}
		if !matched {
			return errors.New("control/agents: selected ACP authentication was not declared by the challenge")
		}
	}
	if NormalizeAuthentication(in.Connection.Authentication) != selected || NormalizeAuthentication(discovery.Authentication) != selected {
		return errors.New("control/agents: ready ACP preparation authentication is inconsistent")
	}
	return nil
}

func acpPreparationDiscoveryPresent(in DiscoverySnapshot) bool {
	in = NormalizeDiscoverySnapshot(in)
	return in.ConnectionID != "" || in.LaunchFingerprint != "" || in.CWD != "" || in.ProtocolVersion != 0 ||
		in.SelectedModelID != "" || in.CurrentModelID != "" || len(in.Models) != 0 || len(in.ConfigOptions) != 0 ||
		in.ModelControl.Kind != "" || in.ModelControl.ConfigID != "" || in.Authentication.MethodID != "" || !in.DiscoveredAt.IsZero()
}

func acpPreparationConnectionPresent(in Connection) bool {
	return strings.TrimSpace(in.ID) != "" || strings.TrimSpace(in.Name) != "" ||
		strings.TrimSpace(in.Launcher.Command) != "" ||
		len(in.Launcher.Args) != 0 || len(in.Launcher.Env) != 0 || strings.TrimSpace(in.Launcher.WorkDir) != "" ||
		strings.TrimSpace(in.Authentication.MethodID) != "" || strings.TrimSpace(string(in.Authentication.Type)) != ""
}

func validateACPPreparationRef(ref string) error {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, "acpp_") {
		return errors.New("control/agents: invalid ACP preparation reference")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(ref, "acpp_"))
	if err != nil || len(raw) != 32 {
		return errors.New("control/agents: invalid ACP preparation reference")
	}
	return nil
}

func validSHA256Hex(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func normalizePreparationTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC().Round(0)
}

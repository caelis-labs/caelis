package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

// AuthorizationSchemaVersion identifies typed consent and revocation facts.
const AuthorizationSchemaVersion = 1

// AuthorizationScope identifies reusable consent. Tool is present for an exact
// tool scope; a resource scope instead names one policy-owned capability set.
// A scope is evidence consumed by Policy, never an independent allow decision.
type AuthorizationScope struct {
	Authority          string `json:"authority"`
	Workspace          string `json:"workspace"`
	SessionID          string `json:"session_id,omitempty"`
	SessionIncarnation string `json:"session_incarnation,omitempty"`
	SourceID           string `json:"source_id"`
	SourceRevision     string `json:"source_revision"`
	PolicyRevision     string `json:"policy_revision"`
	Tool               string `json:"tool,omitempty"`
	CapabilitySet      string `json:"capability_set,omitempty"`
	CapabilityRevision string `json:"capability_revision,omitempty"`
	ResourceKind       string `json:"resource_kind,omitempty"`
	Resource           string `json:"resource,omitempty"`
}

// AuthorizationRevocation is an immutable approval-domain fact. Sequence order
// establishes invalidation; it does not claim to undo an admitted effect.
type AuthorizationRevocation struct {
	Schema int                `json:"schema"`
	Scope  AuthorizationScope `json:"scope"`
}

// AuthorizationEvidence is a disposable projection of Session approval facts.
// Legacy tokens without typed scope never become reusable consent.
type AuthorizationEvidence struct {
	Consents    []AuthorizationScope
	Revocations map[string]uint64
}

// ProjectAuthorizations reads the full durable history independently of model
// compaction. A fork or another Session incarnation cannot inherit consent.
func ProjectAuthorizations(active Session, events []*Event) AuthorizationEvidence {
	out := AuthorizationEvidence{Revocations: map[string]uint64{}}
	latest := map[string]*Event{}
	for _, event := range events {
		if event == nil {
			continue
		}
		if revoked := event.AuthorizationRevocation; revoked != nil && revoked.Scope.MatchesSession(active) {
			key := revoked.Scope.Key()
			if event.Seq > out.Revocations[key] {
				out.Revocations[key] = event.Seq
			}
		}
		if event.Journal != nil && event.Journal.PauseToken != nil {
			token := event.Journal.PauseToken
			if prior := latest[token.TokenID]; prior == nil || prior.Journal.PauseToken.Revision < token.Revision {
				latest[token.TokenID] = event
			}
		}
	}
	consents := map[string]AuthorizationScope{}
	for _, event := range latest {
		token := event.Journal.PauseToken
		a := token.Authorization
		if a == nil || token.Status != PauseTokenResolved || !token.Approved || token.Outcome != "selected" || !a.Scope.MatchesSession(active) || a.Validate(token.Approval) != nil {
			continue
		}
		choice, ok := a.Choice(token.OptionID)
		key := a.Scope.Key()
		if ok && choice == AuthorizationSession && a.Revocation == out.Revocations[key] {
			consents[key] = a.Scope
		}
	}
	keys := make([]string, 0, len(consents))
	for key := range consents {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out.Consents = append(out.Consents, consents[key])
	}
	return out
}

// MatchesSession checks the canonical incarnation and workspace without
// normalizing identities or consulting any permission cache.
func (s AuthorizationScope) MatchesSession(active Session) bool {
	return !active.CreatedAt.IsZero() && s.SessionID == active.SessionID && s.SessionIncarnation == active.CreatedAt.UTC().Format(time.RFC3339Nano) && s.Workspace == active.CWD
}

// Validate rejects incomplete or ambiguous scopes without normalizing identities.
func (s AuthorizationScope) Validate() error {
	if s.Authority == "" || s.Workspace == "" || s.SourceID == "" || s.SourceRevision == "" || s.PolicyRevision == "" {
		return errors.New("authorization scope requires authority, workspace, source and policy identities")
	}
	if (s.SessionID == "") != (s.SessionIncarnation == "") {
		return errors.New("authorization scope requires both Session identity and incarnation")
	}
	if s.Tool != "" {
		if s.CapabilitySet != "" || s.CapabilityRevision != "" || s.ResourceKind != "" || s.Resource != "" {
			return errors.New("tool authorization scope cannot contain a resource or capability set")
		}
		return nil
	}
	if s.SessionID == "" || s.CapabilitySet == "" || s.CapabilityRevision == "" || s.ResourceKind == "" || s.Resource == "" {
		return errors.New("resource authorization requires a Session, capability set and exact resource")
	}
	return nil
}

// Key is a stable content identity. Call IDs and refresh generations deliberately
// do not participate; each caller retains its independent invocation identity.
func (s AuthorizationScope) Key() string {
	raw, _ := json.Marshal(s)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// AuthorizationPersistence is the exact meaning of an offered approval choice.
type AuthorizationPersistence string

const (
	AuthorizationOnce    AuthorizationPersistence = "once"
	AuthorizationSession AuthorizationPersistence = "session"
	AuthorizationPolicy  AuthorizationPersistence = "policy"
)

// AuthorizationChoice binds one offered option to an explicit reusable scope.
// Reject options have no choice. Option names and ACP kinds never widen scope.
type AuthorizationChoice struct {
	OptionID    string                   `json:"option_id"`
	Persistence AuthorizationPersistence `json:"persistence"`
}

// ApprovalAuthorization describes the reusable part of one Policy Ask. PerCall
// prevents coalescing approvals which also waive a restriction on this call.
// CallDigest binds the immutable effective target, arguments and constraints.
type ApprovalAuthorization struct {
	Schema     int                   `json:"schema"`
	Scope      AuthorizationScope    `json:"scope"`
	CallDigest string                `json:"call_digest"`
	PerCall    bool                  `json:"per_call,omitempty"`
	Revocation uint64                `json:"revocation,omitempty"`
	Choices    []AuthorizationChoice `json:"choices"`
}

// CloneApprovalAuthorization detaches the only mutable container.
func CloneApprovalAuthorization(in *ApprovalAuthorization) *ApprovalAuthorization {
	if in == nil {
		return nil
	}
	out := *in
	out.Choices = append([]AuthorizationChoice(nil), in.Choices...)
	return &out
}

// Choice resolves only an explicitly offered typed meaning.
func (a *ApprovalAuthorization) Choice(id string) (AuthorizationPersistence, bool) {
	if a != nil {
		for _, choice := range a.Choices {
			if choice.OptionID == id {
				return choice.Persistence, true
			}
		}
	}
	return "", false
}

// Validate checks typed choices against the exact offered protocol options.
func (a *ApprovalAuthorization) Validate(offered *ProtocolApproval) error {
	if a != nil && a.Schema != AuthorizationSchemaVersion {
		return errors.New("unsupported authorization schema")
	}
	if a == nil {
		return nil
	}
	if err := a.Scope.Validate(); err != nil {
		return err
	}
	if a.Scope.SessionID == "" || a.CallDigest == "" || offered == nil || len(a.Choices) == 0 {
		return errors.New("approval authorization requires a Session scope, call digest and offered choices")
	}
	seen := map[string]bool{}
	for _, choice := range a.Choices {
		if seen[choice.OptionID] {
			return errors.New("duplicate authorization option")
		}
		seen[choice.OptionID] = true
		found := false
		for _, option := range offered.Options {
			if option.ID == choice.OptionID && (option.Kind == "allow_once" || option.Kind == "allow_always") {
				found = true
			}
		}
		if !found {
			return errors.New("authorization choice must name an offered allow option")
		}
		switch choice.Persistence {
		case AuthorizationOnce, AuthorizationSession:
		case AuthorizationPolicy:
			if a.Scope.Tool == "" {
				return errors.New("resource authorization cannot become a persistent Policy rule")
			}
		default:
			return errors.New("unsupported authorization persistence")
		}
	}
	return nil
}

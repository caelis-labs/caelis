package bot

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

// Client is a registered authenticated desktop connection. Credential hashes
// are stored separately from its public lifecycle projection.
type Client struct {
	ID           string    `json:"id"`
	PrincipalID  string    `json:"principal_id"`
	BotID        string    `json:"bot_id"`
	InstanceID   string    `json:"instance_id"`
	ActivationID string    `json:"activation_id"`
	Active       bool      `json:"active"`
	ExpiresAt    time.Time `json:"expires_at"`
	ActivatedAt  time.Time `json:"activated_at"`
	Actions      []string  `json:"actions"`
}

type clientCredential struct {
	Client      Client `json:"client"`
	Hash        string `json:"hash"`
	OperationID string `json:"operation_id"`
}

// ClientRegistration is returned once to the authenticated enrolling client.
// Token must stay in native credential storage and never enter model context.
type ClientRegistration struct {
	Client Client `json:"client"`
	Token  string `json:"token"`
}

// RegisterClient enrolls a desktop connection. Only fixed routine action names
// are accepted; commands, environment variables and plugin configuration are absent.
func (s *WorkStore) RegisterClient(ctx context.Context, principalID, botID, operationID string, actions []string) (ClientRegistration, error) {
	if principalID == "" || operationID == "" {
		return ClientRegistration{}, errorcode.New(errorcode.InvalidArgument, "bot: client identity and operation are required")
	}
	if _, err := ConversationID(botID); err != nil {
		return ClientRegistration{}, err
	}
	seen := map[string]bool{}
	for _, action := range actions {
		switch action {
		case "clock", "reminders", "gesture":
		default:
			return ClientRegistration{}, errorcode.New(errorcode.Unsupported, "bot: unsupported desktop action")
		}
		if seen[action] {
			return ClientRegistration{}, errorcode.New(errorcode.InvalidArgument, "bot: duplicate desktop action")
		}
		seen[action] = true
	}
	id := opaqueID("bot-client-", principalID, botID, operationID)
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ClientRegistration{}, err
	}
	token := id + "." + hex.EncodeToString(nonce[:])
	hash := sha256.Sum256([]byte(token))
	client := Client{ID: id, PrincipalID: principalID, BotID: botID, Actions: append([]string{}, actions...)}
	fresh, err := s.db.Put(ctx, "client", id, clientCredential{Client: client, Hash: hex.EncodeToString(hash[:]), OperationID: operationID})
	if err != nil {
		return ClientRegistration{}, err
	}
	if !fresh {
		var previous clientCredential
		if err := s.db.Get(ctx, "client", id, &previous); err != nil {
			return ClientRegistration{}, err
		}
		if !slices.Equal(previous.Client.Actions, actions) {
			return ClientRegistration{}, errorcode.New(errorcode.Conflict, "bot: registration operation conflicts")
		}
		return ClientRegistration{}, errorcode.New(errorcode.UnknownOutcome, "bot: client registration already exists; its credential is never reissued or replaced")
	}
	return ClientRegistration{Client: client, Token: token}, nil
}

// AuthenticateClient verifies a registered credential independently of whether
// its process-bound activation remains live. Inactive credentials may only activate.
func (s *WorkStore) AuthenticateClient(ctx context.Context, token string) (Client, error) {
	id, _, ok := strings.Cut(token, ".")
	if !ok || !strings.HasPrefix(id, "bot-client-") {
		return Client{}, errorcode.New(errorcode.Unauthenticated, "bot: invalid client credential")
	}
	var value clientCredential
	if err := s.db.Get(ctx, "client", id, &value); err != nil {
		return Client{}, errorcode.New(errorcode.Unauthenticated, "bot: invalid client credential")
	}
	sum := sha256.Sum256([]byte(token))
	expected, err := hex.DecodeString(value.Hash)
	if err != nil || subtle.ConstantTimeCompare(sum[:], expected) != 1 {
		return Client{}, errorcode.New(errorcode.Unauthenticated, "bot: invalid client credential")
	}
	return value.Client, nil
}

// ActivateClient starts or renews a bounded lease in this Host instance. A
// restarted Host never silently revives desktop tools from a persisted lease.
func (s *WorkStore) ActivateClient(ctx context.Context, client Client) (Client, error) {
	var record clientCredential
	if err := s.db.Get(ctx, "client", client.ID, &record); err != nil {
		return Client{}, err
	}
	if record.Client.PrincipalID != client.PrincipalID || record.Client.BotID != client.BotID {
		return Client{}, errorcode.New(errorcode.PermissionDenied, "bot: client binding mismatch")
	}
	next := record
	now := time.Now().UTC()
	if !record.Client.Active || record.Client.InstanceID != s.InstanceID {
		if !record.Client.Active {
			next.Client.ActivatedAt = now
		}
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return Client{}, err
		}
		next.Client.ActivationID = hex.EncodeToString(nonce[:])
	}
	next.Client.Active = true
	next.Client.InstanceID = s.InstanceID
	next.Client.ExpiresAt = now.Add(10 * time.Minute)
	changed, err := s.db.compare(ctx, "client", client.ID, record, next)
	if err != nil {
		return Client{}, err
	}
	if !changed {
		return Client{}, errorcode.New(errorcode.Conflict, "bot: client activation changed")
	}
	return next.Client, nil
}

// ActiveClient resolves current authority for every desktop call; tool discovery
// does not retain a revoked or expired lease.
func (s *WorkStore) ActiveClient(ctx context.Context, principalID, botID, id string) (Client, error) {
	client, err := s.ClientState(ctx, principalID, botID, id)
	if err != nil {
		return Client{}, err
	}
	if !client.Active || client.InstanceID != s.InstanceID || !client.ExpiresAt.After(time.Now()) {
		return Client{}, errorcode.New(errorcode.PermissionDenied, "bot: client activation is inactive")
	}
	return client, nil
}

// ClientState returns the scoped lifecycle receipt, including after explicit exit.
func (s *WorkStore) ClientState(ctx context.Context, principalID, botID, id string) (Client, error) {
	var record clientCredential
	if err := s.db.Get(ctx, "client", id, &record); err != nil {
		return Client{}, err
	}
	client := record.Client
	if client.PrincipalID != principalID || client.BotID != botID {
		return Client{}, errorcode.New(errorcode.PermissionDenied, "bot: client binding mismatch")
	}
	return client, nil
}

// ExitClient revokes the active desktop connection. It does not close a Host,
// unregister other clients, or decide whether any work should be interrupted.
func (s *WorkStore) ExitClient(ctx context.Context, client Client) error {
	for range 8 {
		var record clientCredential
		if err := s.db.Get(ctx, "client", client.ID, &record); err != nil {
			return err
		}
		if record.Client.PrincipalID != client.PrincipalID || record.Client.BotID != client.BotID {
			return errorcode.New(errorcode.PermissionDenied, "bot: client binding mismatch")
		}
		if record.Client.ActivationID != client.ActivationID || record.Client.InstanceID != client.InstanceID {
			return errorcode.New(errorcode.Conflict, "bot: client activation changed before exit")
		}
		if !record.Client.Active {
			return nil
		}
		next := record
		next.Client.Active = false
		changed, err := s.db.compare(ctx, "client", client.ID, record, next)
		if err != nil || changed {
			if changed {
				s.desktop.wake()
			}
			return err
		}
	}
	return errors.New("bot: client exit contention")
}

package sessionprofile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/caelis-labs/caelis/control/internal/operationreceipt"
	"time"
)

// MaxResourceBytes bounds one immutable resource. Bytes stay in SQLite; resource
// names and IDs never become filesystem paths inside this package.
const MaxResourceBytes = 8 << 20

// CreateResource stores a permanent immutable byte snapshot under exact
// connection/Session ownership. Retrying with changed metadata or bytes conflicts.
func (s *Store) CreateResource(ctx context.Context, scope Scope, session, operation, name, mediaType string, data []byte) (Resource, error) {
	return s.CreateResourceWithExpiry(ctx, scope, session, operation, name, mediaType, data, nil)
}

// CreateResourceWithExpiry stores immutable owned bytes, optionally bounded by
// expiresAt. Expiry is fixed by the upload operation and never renewed by retry.
// The Host owns any later materialization into its controlled workspace.
func (s *Store) CreateResourceWithExpiry(ctx context.Context, scope Scope, session, operation, name, mediaType string, data []byte, expiresAt *time.Time) (Resource, error) {
	if !validID(operation) || !validID(name) || !validID(mediaType) || len(data) > MaxResourceBytes {
		return Resource{}, ErrInvalid
	}
	// Copy once so neither persistence nor digest depends on caller-owned storage.
	data = append([]byte{}, data...)
	r := Resource{ID: stableID("app-resource-", scope.PrincipalID, session, operation), SessionID: session, Name: name, MediaType: mediaType, Size: int64(len(data)), SHA256: digestBytes(data)}
	if expiresAt != nil {
		deadline := expiresAt.UTC()
		r.ExpiresAt = &deadline
	}
	body, err := encode(r)
	if err != nil {
		return Resource{}, err
	}
	digest := digestBytes(body)
	s.mu.Lock()
	defer s.mu.Unlock()
	binding, err := s.binding(ctx, scope, session)
	if err != nil {
		return Resource{}, err
	}
	var oldDigest, oldID string
	err = s.db.QueryRowContext(ctx, `SELECT digest,id FROM session_resources WHERE principal=? AND session=? AND operation=?`, scope.PrincipalID, session, operation).Scan(&oldDigest, &oldID)
	if err == nil {
		r.ID = oldID
		body, err = encode(r)
		if err != nil {
			return Resource{}, err
		}
		digest = digestBytes(body)
		if oldDigest != digest {
			return Resource{}, ErrConflict
		}
		stored, _, err := s.resourceStored(ctx, scope, session, r.ID)
		if err == nil && operationreceipt.HasTransaction(ctx) {
			err = s.commitResourceReceipt(ctx, scope, operation, stored, data, false)
		}
		return stored, err
	}
	if !errors.Is(notFound(err), ErrNotFound) {
		return Resource{}, err
	}
	if binding.Archived {
		return Resource{}, ErrRevoked
	}
	if err = s.checkScope(ctx, scope); err != nil {
		return Resource{}, err
	}
	if r.ExpiresAt != nil && !s.now().Before(*r.ExpiresAt) {
		return Resource{}, ErrInvalid
	}
	if operationreceipt.HasTransaction(ctx) {
		err = s.commitResourceReceipt(ctx, scope, operation, r, data, true)
	} else {
		_, err = s.db.ExecContext(ctx, `INSERT INTO session_resources(id,principal,session,operation,digest,body,data) VALUES(?,?,?,?,?,?,?)`, r.ID, scope.PrincipalID, session, operation, digest, body, data)
	}
	return r, err
}

// resource is the internal byte authority, including for callback content.
func (s *Store) resource(ctx context.Context, scope Scope, session, id string) (Resource, []byte, error) {
	r, data, err := s.resourceStored(ctx, scope, session, id)
	if err != nil {
		return Resource{}, nil, err
	}
	if r.ExpiresAt != nil && !s.now().Before(*r.ExpiresAt) {
		return Resource{}, nil, ErrResourceExpired
	}
	return r, data, nil
}

// resourceStored verifies ownership and integrity without granting byte use.
// This path serves descriptors and exact upload receipts after expiry.
func (s *Store) resourceStored(ctx context.Context, scope Scope, session, id string) (Resource, []byte, error) {
	if _, err := s.binding(ctx, scope, session); err != nil {
		return Resource{}, nil, err
	}
	var body, data []byte
	err := s.db.QueryRowContext(ctx, `SELECT body,data FROM session_resources WHERE id=? AND principal=?  AND session=?`, id, scope.PrincipalID, session).Scan(&body, &data)
	if err != nil {
		return Resource{}, nil, notFound(err)
	}
	var r Resource
	if err = json.Unmarshal(body, &r); err != nil {
		return Resource{}, nil, err
	}
	if r.ID != id || r.SessionID != session || r.Size != int64(len(data)) || len(data) > MaxResourceBytes || r.SHA256 != digestBytes(data) {
		return Resource{}, nil, fmt.Errorf("%w: resource integrity mismatch", ErrInvalid)
	}
	return r, data, nil
}

// GetResource returns verified immutable metadata without exposing a path.
// Expired descriptors remain available to their owner, but their bytes do not.
func (s *Store) GetResource(ctx context.Context, scope Scope, session, id string) (Resource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, _, err := s.resourceStored(ctx, scope, session, id)
	return r, err
}

// ReadResource verifies its digest and expiration before returning owned bytes.
// Expired and revoked connection leases retain access to unexpired resources in
// their exact original scope; expired resources cannot be read.
func (s *Store) ReadResource(ctx context.Context, scope Scope, session, id string) (Resource, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resource(ctx, scope, session, id)
}

// commitResourceReceipt commits bytes and the common command descriptor together.
// Native producer resources without a Control command keep their existing owner.
func (s *Store) commitResourceReceipt(ctx context.Context, scope Scope, operation string, resource Resource, data []byte, insert bool) error {
	body, err := encode(resource)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if insert {
		_, err = tx.ExecContext(ctx, `INSERT INTO session_resources(id,principal,session,operation,digest,body,data) VALUES(?,?,?,?,?,?,?)`, resource.ID, scope.PrincipalID, resource.SessionID, operation, digestBytes(body), body, data)
		if err != nil {
			return err
		}
	}
	if err = operationreceipt.CompleteTransaction(ctx, tx, body); err != nil {
		return err
	}
	return tx.Commit()
}

package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// MaxResourceBytes bounds one immutable resource. Bytes stay in SQLite; resource
// names and IDs never become filesystem paths inside this package.
const MaxResourceBytes = 8 << 20

// CreateResource stores immutable bytes under exact connection/Session ownership.
// Retrying an operation with changed metadata or bytes conflicts. The Host owns
// any later materialization into its controlled workspace.
func (s *Store) CreateResource(ctx context.Context, scope Scope, session, operation, name, mediaType string, data []byte) (Resource, error) {
	if !validID(operation) || !validID(name) || !validID(mediaType) || len(data) > MaxResourceBytes {
		return Resource{}, ErrInvalid
	}
	// Copy once so neither persistence nor digest depends on caller-owned storage.
	data = append([]byte{}, data...)
	r := Resource{ID: stableID("app-resource-", scope.PrincipalID, scope.ApplicationID, scope.ConnectionID, session, operation), SessionID: session, Name: name, MediaType: mediaType, Size: int64(len(data)), SHA256: digestBytes(data)}
	body, err := encode(r)
	if err != nil {
		return Resource{}, err
	}
	digest := digestBytes(body)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err = s.active(ctx, scope); err != nil {
		return Resource{}, err
	}
	binding, err := s.binding(ctx, scope, session)
	if err != nil {
		return Resource{}, err
	}
	if binding.Archived {
		return Resource{}, ErrRevoked
	}
	var oldDigest string
	err = s.db.QueryRowContext(ctx, `SELECT digest FROM app_resources WHERE connection=? AND session=? AND operation=?`, scope.ConnectionID, session, operation).Scan(&oldDigest)
	if err == nil {
		if oldDigest != digest {
			return Resource{}, ErrConflict
		}
		_, _, err = s.resource(ctx, scope, session, r.ID)
		return r, err
	}
	if !errors.Is(notFound(err), ErrNotFound) {
		return Resource{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO app_resources(id,principal,application,connection,session,operation,digest,body,data) VALUES(?,?,?,?,?,?,?,?,?)`, r.ID, scope.PrincipalID, scope.ApplicationID, scope.ConnectionID, session, operation, digest, body, data)
	return r, err
}
func (s *Store) resource(ctx context.Context, scope Scope, session, id string) (Resource, []byte, error) {
	if _, err := s.binding(ctx, scope, session); err != nil {
		return Resource{}, nil, err
	}
	var body, data []byte
	err := s.db.QueryRowContext(ctx, `SELECT body,data FROM app_resources WHERE id=? AND principal=? AND application=? AND connection=? AND session=?`, id, scope.PrincipalID, scope.ApplicationID, scope.ConnectionID, session).Scan(&body, &data)
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
func (s *Store) GetResource(ctx context.Context, scope Scope, session, id string) (Resource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, _, err := s.resource(ctx, scope, session, id)
	return r, err
}

// ReadResource verifies its digest before returning owned bytes. Expired and
// revoked credentials retain read access to their exact original scope only.
func (s *Store) ReadResource(ctx context.Context, scope Scope, session, id string) (Resource, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resource(ctx, scope, session, id)
}

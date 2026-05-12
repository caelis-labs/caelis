// Package config defines public configuration and secret resolution ports.
package config

import "context"

type Store interface {
	Load(context.Context, string, any) error
	Save(context.Context, string, any) error
}

type SecretResolver interface {
	ResolveSecret(context.Context, SecretRef) (string, error)
}

type SecretRef struct {
	Env  string
	Name string
}

type RuntimePreferenceStore interface {
	GetPreference(context.Context, string) (string, bool, error)
	SetPreference(context.Context, string, string) error
}

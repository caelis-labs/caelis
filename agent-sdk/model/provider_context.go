package model

import (
	"context"
	"strings"
)

type providerRequestMetadataKey struct{}

// ProviderRequestMetadata carries provider-private request hints that must not
// become model-visible message content or canonical replay state.
type ProviderRequestMetadata struct {
	SessionAffinity string
}

func WithProviderRequestMetadata(ctx context.Context, metadata ProviderRequestMetadata) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	metadata.SessionAffinity = strings.TrimSpace(metadata.SessionAffinity)
	if metadata.SessionAffinity == "" {
		return ctx
	}
	return context.WithValue(ctx, providerRequestMetadataKey{}, metadata)
}

func ProviderRequestMetadataFromContext(ctx context.Context) (ProviderRequestMetadata, bool) {
	if ctx == nil {
		return ProviderRequestMetadata{}, false
	}
	metadata, ok := ctx.Value(providerRequestMetadataKey{}).(ProviderRequestMetadata)
	if !ok {
		return ProviderRequestMetadata{}, false
	}
	metadata.SessionAffinity = strings.TrimSpace(metadata.SessionAffinity)
	return metadata, metadata.SessionAffinity != ""
}

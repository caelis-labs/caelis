package providers

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"testing"
	"time"
)

func TestCodeFreeCredentialResolverLoadsExplicitStoreWithoutFilesystem(t *testing.T) {
	store := &memoryCodeFreeCredentialStore{
		record: CodeFreeCredentialRecord{
			UserID:          "272182",
			EncryptedAPIKey: encryptCodeFreeAPIKeyForTest(t, "api-key"),
			BaseURL:         "https://provider.example",
			ExpiresAt:       time.Now().Add(time.Hour),
		},
	}

	resolved, err := NewCodeFreeCredentialResolver(CodeFreeCredentialResolverConfig{
		Store: store,
	}).Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Credentials.UserID != "272182" || resolved.Credentials.APIKey != "api-key" {
		t.Fatalf("credentials = %#v, want decrypted store credentials", resolved.Credentials)
	}
	if resolved.BaseURL != "https://provider.example" {
		t.Fatalf("baseURL = %q, want store base URL", resolved.BaseURL)
	}
}

func TestCodeFreeCredentialResolverRefreshesExpiredStoreRecord(t *testing.T) {
	stale := CodeFreeCredentialRecord{
		UserID:          "272182",
		EncryptedAPIKey: encryptCodeFreeAPIKeyForTest(t, "stale-key"),
		RefreshToken:    "refresh-1",
		ExpiresAt:       time.Now().Add(-time.Minute),
	}
	fresh := CodeFreeCredentialRecord{
		UserID:          "272182",
		EncryptedAPIKey: encryptCodeFreeAPIKeyForTest(t, "fresh-key"),
		RefreshToken:    "refresh-2",
		ExpiresAt:       time.Now().Add(time.Hour),
	}
	store := &memoryCodeFreeCredentialStore{record: stale}
	refresher := &recordingCodeFreeRefresher{record: fresh}

	resolved, err := NewCodeFreeCredentialResolver(CodeFreeCredentialResolverConfig{
		Store:     store,
		Refresher: refresher,
	}).Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if refresher.refreshToken != "refresh-1" {
		t.Fatalf("refresh token = %q, want refresh-1", refresher.refreshToken)
	}
	if !store.saved || store.record.RefreshToken != "refresh-2" {
		t.Fatalf("saved=%t record=%#v, want refreshed record saved", store.saved, store.record)
	}
	if resolved.Credentials.APIKey != "fresh-key" {
		t.Fatalf("api key = %q, want fresh-key", resolved.Credentials.APIKey)
	}
}

func TestCodeFreeCredentialResolverRejectsUnusableExpiredRecordWhenRefreshFails(t *testing.T) {
	store := &memoryCodeFreeCredentialStore{
		record: CodeFreeCredentialRecord{
			UserID:       "272182",
			RefreshToken: "refresh-1",
			ExpiresAt:    time.Now().Add(-time.Minute),
		},
	}
	refresher := &recordingCodeFreeRefresher{err: errCodeFreeAuthTestRefresh}

	_, err := NewCodeFreeCredentialResolver(CodeFreeCredentialResolverConfig{
		Store:     store,
		Refresher: refresher,
	}).Resolve(context.Background())
	if err == nil {
		t.Fatal("Resolve() error = nil, want refresh failure")
	}
}

type memoryCodeFreeCredentialStore struct {
	record CodeFreeCredentialRecord
	saved  bool
}

func (s *memoryCodeFreeCredentialStore) LoadCodeFreeCredentials(context.Context) (CodeFreeCredentialRecord, error) {
	return s.record, nil
}

func (s *memoryCodeFreeCredentialStore) SaveCodeFreeCredentials(_ context.Context, record CodeFreeCredentialRecord) error {
	s.saved = true
	s.record = record
	return nil
}

type recordingCodeFreeRefresher struct {
	record       CodeFreeCredentialRecord
	err          error
	refreshToken string
}

func (r *recordingCodeFreeRefresher) RefreshCodeFreeCredentials(_ context.Context, refreshToken string, _ CodeFreeCredentialRecord) (CodeFreeCredentialRecord, error) {
	r.refreshToken = refreshToken
	if r.err != nil {
		return CodeFreeCredentialRecord{}, r.err
	}
	return r.record, nil
}

type codeFreeAuthTestError string

func (e codeFreeAuthTestError) Error() string { return string(e) }

const errCodeFreeAuthTestRefresh = codeFreeAuthTestError("refresh failed")

func encryptCodeFreeAPIKeyForTest(t *testing.T, value string) string {
	t.Helper()
	block, err := aes.NewCipher([]byte(codeFreeAPIKeyDecryptKey))
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	plain := []byte(value)
	padding := block.BlockSize() - len(plain)%block.BlockSize()
	for i := 0; i < padding; i++ {
		plain = append(plain, byte(padding))
	}
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, []byte(codeFreeAPIKeyDecryptIV)).CryptBlocks(out, plain)
	return base64.StdEncoding.EncodeToString(out)
}

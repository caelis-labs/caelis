package providers

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

const (
	codeFreeAPIKeyDecryptKey = "Xtpa6sS&+D.NAo%CP8LA:7pk"
	codeFreeAPIKeyDecryptIV  = "%1KJIrl3!XUxr04V"
)

// CodeFreeCredentialRecord is the provider-neutral credential document shape
// used by CodeFree auth helpers. Callers own where and how this document is
// stored; Layer 4 does not read local CLI credential files directly.
type CodeFreeCredentialRecord struct {
	AccessToken      string
	RefreshToken     string
	UserID           string
	EncryptedAPIKey  string
	BaseURL          string
	TokenType        string
	ExpiresAt        time.Time
	RefreshExpiresAt time.Time
}

// CodeFreeResolvedCredentials are ready to feed into CodeFreeProvider config.
type CodeFreeResolvedCredentials struct {
	Credentials      CodeFreeCredentials
	AccessToken      string
	RefreshToken     string
	BaseURL          string
	TokenType        string
	ExpiresAt        time.Time
	RefreshExpiresAt time.Time
}

// CodeFreeCredentialStore loads and persists CodeFree credential records.
type CodeFreeCredentialStore interface {
	LoadCodeFreeCredentials(context.Context) (CodeFreeCredentialRecord, error)
	SaveCodeFreeCredentials(context.Context, CodeFreeCredentialRecord) error
}

// CodeFreeCredentialRefresher refreshes an expired CodeFree credential record.
type CodeFreeCredentialRefresher interface {
	RefreshCodeFreeCredentials(context.Context, string, CodeFreeCredentialRecord) (CodeFreeCredentialRecord, error)
}

type CodeFreeCredentialResolverConfig struct {
	Store     CodeFreeCredentialStore
	Refresher CodeFreeCredentialRefresher
	Now       func() time.Time
}

type CodeFreeCredentialResolver struct {
	store     CodeFreeCredentialStore
	refresher CodeFreeCredentialRefresher
	now       func() time.Time
}

func NewCodeFreeCredentialResolver(cfg CodeFreeCredentialResolverConfig) *CodeFreeCredentialResolver {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &CodeFreeCredentialResolver{
		store:     cfg.Store,
		refresher: cfg.Refresher,
		now:       now,
	}
}

func (r *CodeFreeCredentialResolver) Resolve(ctx context.Context) (CodeFreeResolvedCredentials, error) {
	if r == nil || r.store == nil {
		return CodeFreeResolvedCredentials{}, fmt.Errorf("providers: codefree credential store is required")
	}
	record, err := r.store.LoadCodeFreeCredentials(ctx)
	if err != nil {
		return CodeFreeResolvedCredentials{}, err
	}
	if r.shouldRefresh(record) {
		refreshed, err := r.refresh(ctx, record)
		if err != nil {
			if !codeFreeRecordHasUsableCredentials(record) {
				return CodeFreeResolvedCredentials{}, err
			}
		} else {
			record = refreshed
		}
	}
	return resolveCodeFreeCredentialRecord(record)
}

func (r *CodeFreeCredentialResolver) shouldRefresh(record CodeFreeCredentialRecord) bool {
	if strings.TrimSpace(record.RefreshToken) == "" {
		return false
	}
	if !codeFreeRecordHasUsableCredentials(record) {
		return true
	}
	expiresAt := record.ExpiresAt
	return !expiresAt.IsZero() && !r.now().Before(expiresAt)
}

func (r *CodeFreeCredentialResolver) refresh(ctx context.Context, record CodeFreeCredentialRecord) (CodeFreeCredentialRecord, error) {
	if r.refresher == nil {
		return record, fmt.Errorf("providers: codefree credentials expired and no refresher is configured")
	}
	refreshed, err := r.refresher.RefreshCodeFreeCredentials(ctx, strings.TrimSpace(record.RefreshToken), record)
	if err != nil {
		return record, err
	}
	if strings.TrimSpace(refreshed.RefreshToken) == "" {
		refreshed.RefreshToken = record.RefreshToken
	}
	if strings.TrimSpace(refreshed.BaseURL) == "" {
		refreshed.BaseURL = record.BaseURL
	}
	if err := r.store.SaveCodeFreeCredentials(ctx, refreshed); err != nil {
		return record, err
	}
	return refreshed, nil
}

func resolveCodeFreeCredentialRecord(record CodeFreeCredentialRecord) (CodeFreeResolvedCredentials, error) {
	userID := strings.TrimSpace(record.UserID)
	if userID == "" {
		return CodeFreeResolvedCredentials{}, fmt.Errorf("providers: codefree credentials missing id_token/userId")
	}
	apiKey, err := decryptCodeFreeAPIKey(record.EncryptedAPIKey)
	if err != nil {
		return CodeFreeResolvedCredentials{}, err
	}
	return CodeFreeResolvedCredentials{
		Credentials: CodeFreeCredentials{
			UserID: userID,
			APIKey: apiKey,
		},
		AccessToken:      strings.TrimSpace(record.AccessToken),
		RefreshToken:     strings.TrimSpace(record.RefreshToken),
		BaseURL:          strings.TrimSpace(record.BaseURL),
		TokenType:        strings.TrimSpace(record.TokenType),
		ExpiresAt:        record.ExpiresAt,
		RefreshExpiresAt: record.RefreshExpiresAt,
	}, nil
}

func codeFreeRecordHasUsableCredentials(record CodeFreeCredentialRecord) bool {
	return strings.TrimSpace(record.UserID) != "" && strings.TrimSpace(record.EncryptedAPIKey) != ""
}

func decryptCodeFreeAPIKey(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("providers: codefree credentials missing encrypted api key")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		return "", fmt.Errorf("providers: decode codefree encrypted api key: %w", err)
	}
	block, err := aes.NewCipher([]byte(codeFreeAPIKeyDecryptKey))
	if err != nil {
		return "", fmt.Errorf("providers: init codefree api key cipher: %w", err)
	}
	if len(ciphertext) == 0 || len(ciphertext)%block.BlockSize() != 0 {
		return "", fmt.Errorf("providers: invalid codefree encrypted api key length")
	}
	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, []byte(codeFreeAPIKeyDecryptIV)).CryptBlocks(plain, ciphertext)
	plain, err = trimPKCS7Padding(plain, block.BlockSize())
	if err != nil {
		return "", fmt.Errorf("providers: unpad codefree api key: %w", err)
	}
	apiKey := strings.TrimSpace(string(plain))
	if apiKey == "" {
		return "", fmt.Errorf("providers: decrypted codefree api key is empty")
	}
	return apiKey, nil
}

func trimPKCS7Padding(buf []byte, blockSize int) ([]byte, error) {
	if len(buf) == 0 || blockSize <= 0 || len(buf)%blockSize != 0 {
		return nil, fmt.Errorf("invalid pkcs7 buffer")
	}
	pad := int(buf[len(buf)-1])
	if pad == 0 || pad > blockSize || pad > len(buf) {
		return nil, fmt.Errorf("invalid pkcs7 padding")
	}
	for _, b := range buf[len(buf)-pad:] {
		if int(b) != pad {
			return nil, fmt.Errorf("invalid pkcs7 padding")
		}
	}
	return buf[:len(buf)-pad], nil
}

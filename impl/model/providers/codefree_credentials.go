package providers

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type codeFreeCachedCredentials struct {
	AccessToken               string `json:"access_token,omitempty"`
	RefreshToken              string `json:"refresh_token,omitempty"`
	UserID                    string `json:"id_token"`
	APIKey                    string `json:"apikey"`
	BaseURL                   string `json:"baseUrl,omitempty"`
	TokenType                 string `json:"token_type,omitempty"`
	ExpiresIn                 int64  `json:"expires_in,omitempty"`
	RefreshTokenExpiresIn     int64  `json:"refresh_token_expires_in,omitempty"`
	ObtainedAtUnixMilli       int64  `json:"obtained_at_unix_ms,omitempty"`
	ExpiresAtUnixMilli        int64  `json:"expires_at_unix_ms,omitempty"`
	RefreshExpiresAtUnixMilli int64  `json:"refresh_expires_at_unix_ms,omitempty"`
}

type codeFreeCredentials struct {
	UserID           string
	APIKey           string
	AccessToken      string
	RefreshToken     string
	BaseURL          string
	TokenType        string
	ExpiresAt        time.Time
	RefreshExpiresAt time.Time
	CredentialPath   string
}

type codeFreeStoredCredentials struct {
	Cached  codeFreeCachedCredentials
	Path    string
	ModTime time.Time
}

var codeFreeCredentialMu sync.Mutex

func loadCodeFreeCredentials(ctx context.Context) (codeFreeCredentials, error) {
	codeFreeCredentialMu.Lock()
	defer codeFreeCredentialMu.Unlock()

	stored, err := readCodeFreeStoredCredentials()
	if err != nil {
		return codeFreeCredentials{}, err
	}
	if needsCodeFreeRefresh(stored.Cached, stored.ModTime) && strings.TrimSpace(stored.Cached.RefreshToken) != "" {
		refreshed, err := refreshCodeFreeStoredCredentials(ctx, stored)
		if err == nil {
			stored = refreshed
		} else if !canUseCodeFreeStoredCredentials(stored.Cached) {
			return codeFreeCredentials{}, err
		}
	}
	return finalizeCodeFreeCredentials(stored)
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

func resolveCodeFreeCredentialPath() (string, error) {
	path := strings.TrimSpace(os.Getenv(codeFreeCredsPathEnv))
	if path != "" {
		return path, nil
	}
	primary, _, err := resolveCodeFreeDefaultCredentialPaths()
	if err != nil {
		return "", err
	}
	return primary, nil
}

func resolveCodeFreeDefaultCredentialPaths() (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("providers: resolve codefree home dir: %w", err)
	}
	primary := filepath.Join(home, ".caelis", filepath.FromSlash(codeFreeCredentialDir), codeFreeDefaultCredentialFile)
	legacy := filepath.Join(home, codeFreeLegacyCredentialDir, codeFreeDefaultCredentialFile)
	return primary, legacy, nil
}

func readCodeFreeStoredCredentials() (codeFreeStoredCredentials, error) {
	path, err := resolveCodeFreeCredentialPath()
	if err != nil {
		return codeFreeStoredCredentials{}, err
	}
	stored, err := readCodeFreeStoredCredentialsAtPath(path)
	if err == nil {
		return stored, nil
	}
	if strings.TrimSpace(os.Getenv(codeFreeCredsPathEnv)) != "" || !errors.Is(err, os.ErrNotExist) {
		return codeFreeStoredCredentials{}, err
	}
	primary, legacy, resolveErr := resolveCodeFreeDefaultCredentialPaths()
	if resolveErr != nil {
		return codeFreeStoredCredentials{}, resolveErr
	}
	if filepath.Clean(path) != filepath.Clean(primary) {
		return codeFreeStoredCredentials{}, err
	}
	imported, importErr := importLegacyCodeFreeStoredCredentials(primary, legacy)
	if importErr == nil {
		return imported, nil
	}
	return codeFreeStoredCredentials{}, err
}

func importLegacyCodeFreeStoredCredentials(primary string, legacy string) (codeFreeStoredCredentials, error) {
	if filepath.Clean(primary) == filepath.Clean(legacy) {
		return codeFreeStoredCredentials{}, fmt.Errorf("providers: codefree credential import source and destination are identical")
	}
	raw, err := os.ReadFile(legacy)
	if err != nil {
		return codeFreeStoredCredentials{}, fmt.Errorf("providers: read legacy codefree credentials %q: %w", legacy, err)
	}
	info, err := os.Stat(legacy)
	if err != nil {
		return codeFreeStoredCredentials{}, fmt.Errorf("providers: stat legacy codefree credentials %q: %w", legacy, err)
	}
	if err := os.MkdirAll(filepath.Dir(primary), 0o755); err != nil {
		return codeFreeStoredCredentials{}, fmt.Errorf("providers: create caelis codefree credential dir: %w", err)
	}
	if err := os.WriteFile(primary, raw, 0o600); err != nil {
		return codeFreeStoredCredentials{}, fmt.Errorf("providers: import codefree credentials into %q: %w", primary, err)
	}
	if err := os.Chtimes(primary, info.ModTime(), info.ModTime()); err != nil {
		return codeFreeStoredCredentials{}, fmt.Errorf("providers: preserve imported codefree credential mtime for %q: %w", primary, err)
	}
	return readCodeFreeStoredCredentialsAtPath(primary)
}

func canUseCodeFreeStoredCredentials(cached codeFreeCachedCredentials) bool {
	return strings.TrimSpace(cached.UserID) != "" && strings.TrimSpace(cached.APIKey) != ""
}

func finalizeCodeFreeCredentials(stored codeFreeStoredCredentials) (codeFreeCredentials, error) {
	userID := strings.TrimSpace(stored.Cached.UserID)
	if userID == "" {
		return codeFreeCredentials{}, fmt.Errorf("providers: codefree credentials missing id_token/userId")
	}
	apiKey, err := decryptCodeFreeAPIKey(stored.Cached.APIKey)
	if err != nil {
		return codeFreeCredentials{}, err
	}
	return codeFreeCredentials{
		UserID:           userID,
		APIKey:           apiKey,
		AccessToken:      strings.TrimSpace(stored.Cached.AccessToken),
		RefreshToken:     strings.TrimSpace(stored.Cached.RefreshToken),
		BaseURL:          codeFreeFirstNonEmpty(strings.TrimSpace(stored.Cached.BaseURL), codeFreeDefaultBaseURL),
		TokenType:        strings.TrimSpace(stored.Cached.TokenType),
		ExpiresAt:        codeFreeExpiresAt(stored.Cached, stored.ModTime),
		RefreshExpiresAt: codeFreeRefreshExpiresAt(stored.Cached, stored.ModTime),
		CredentialPath:   stored.Path,
	}, nil
}

func codeFreeFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func codeFreeExpiresAt(cached codeFreeCachedCredentials, modTime time.Time) time.Time {
	if cached.ExpiresAtUnixMilli > 0 {
		return time.UnixMilli(cached.ExpiresAtUnixMilli)
	}
	if cached.ObtainedAtUnixMilli > 0 && cached.ExpiresIn > 0 {
		return time.UnixMilli(cached.ObtainedAtUnixMilli).Add(time.Duration(cached.ExpiresIn) * time.Second)
	}
	if !modTime.IsZero() && cached.ExpiresIn > 0 {
		return modTime.Add(time.Duration(cached.ExpiresIn) * time.Second)
	}
	return time.Time{}
}

func codeFreeRefreshExpiresAt(cached codeFreeCachedCredentials, modTime time.Time) time.Time {
	if cached.RefreshExpiresAtUnixMilli > 0 {
		return time.UnixMilli(cached.RefreshExpiresAtUnixMilli)
	}
	if cached.ObtainedAtUnixMilli > 0 && cached.RefreshTokenExpiresIn > 0 {
		return time.UnixMilli(cached.ObtainedAtUnixMilli).Add(time.Duration(cached.RefreshTokenExpiresIn) * time.Second)
	}
	if !modTime.IsZero() && cached.RefreshTokenExpiresIn > 0 {
		return modTime.Add(time.Duration(cached.RefreshTokenExpiresIn) * time.Second)
	}
	return time.Time{}
}

func needsCodeFreeRefresh(cached codeFreeCachedCredentials, modTime time.Time) bool {
	if strings.TrimSpace(cached.RefreshToken) == "" {
		return false
	}
	if strings.TrimSpace(cached.UserID) == "" || strings.TrimSpace(cached.APIKey) == "" {
		return true
	}
	expiresAt := codeFreeExpiresAt(cached, modTime)
	return !expiresAt.IsZero() && !time.Now().Before(expiresAt)
}

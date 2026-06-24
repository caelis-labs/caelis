package providers

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type codeFreeCachedCredentials struct {
	EncryptedAPIKey string `json:"encryptedApiKey"`
	UserID          string `json:"userId"`
	SessionID       string `json:"sessionId"`
	BaseURLSnapshot string `json:"baseUrlSnapshot,omitempty"`
}

type codeFreeCredentials struct {
	UserID         string
	APIKey         string
	SessionID      string
	BaseURL        string
	CredentialPath string
}

type codeFreeStoredCredentials struct {
	Cached  codeFreeCachedCredentials
	Path    string
	ModTime time.Time
}

var codeFreeCredentialMu sync.Mutex

func loadCodeFreeCredentials(_ context.Context, baseURL string) (codeFreeCredentials, error) {
	codeFreeCredentialMu.Lock()
	defer codeFreeCredentialMu.Unlock()

	stored, err := loadCodeFreeStoredCredentialsLocked(baseURL, "")
	if err != nil {
		return codeFreeCredentials{}, err
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

func resolveCodeFreeDefaultCredentialPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("providers: resolve codefree home dir: %w", err)
	}
	primary := filepath.Join(home, ".caelis", filepath.FromSlash(codeFreeCredentialDir), codeFreeDefaultCredentialFile)
	return primary, nil
}

func loadCodeFreeStoredCredentialsLocked(baseURL string, credentialPath string) (codeFreeStoredCredentials, error) {
	path, explicit, err := resolveCodeFreeCredentialPathForLoad(credentialPath)
	if err != nil {
		return codeFreeStoredCredentials{}, err
	}
	stored, readErr := readCodeFreeStoredCredentialsAtPath(path)
	if readErr == nil {
		if err := validateCodeFreeStoredCredentials(stored, baseURL); err == nil {
			return stored, nil
		} else if explicit {
			return codeFreeStoredCredentials{}, err
		}
	}
	if explicit {
		return codeFreeStoredCredentials{}, readErr
	}
	imported, importErr := importCodeFreeLocalCredentialsLocked(baseURL, path)
	if importErr == nil {
		return imported, nil
	}
	if readErr != nil {
		return codeFreeStoredCredentials{}, readErr
	}
	return codeFreeStoredCredentials{}, importErr
}

func resolveCodeFreeCredentialPathForLoad(credentialPath string) (string, bool, error) {
	if path := strings.TrimSpace(credentialPath); path != "" {
		return path, true, nil
	}
	if path := strings.TrimSpace(os.Getenv(codeFreeCredsPathEnv)); path != "" {
		return path, true, nil
	}
	path, err := resolveCodeFreeDefaultCredentialPath()
	return path, false, err
}

func canUseCodeFreeStoredCredentials(cached codeFreeCachedCredentials) bool {
	return strings.TrimSpace(cached.UserID) != "" &&
		strings.TrimSpace(cached.EncryptedAPIKey) != "" &&
		strings.TrimSpace(cached.SessionID) != ""
}

func validateCodeFreeStoredCredentials(stored codeFreeStoredCredentials, baseURL string) error {
	if !canUseCodeFreeStoredCredentials(stored.Cached) {
		return fmt.Errorf("providers: codefree credentials %q are incomplete", stored.Path)
	}
	if !codeFreeCredentialMatchesBaseURL(stored.Cached, baseURL) {
		return fmt.Errorf("providers: codefree credentials %q were issued for %q, not %q", stored.Path, codeFreeCredentialBaseURL(stored.Cached), normalizeCodeFreeBaseURL(baseURL))
	}
	if _, err := decryptCodeFreeAPIKey(stored.Cached.EncryptedAPIKey); err != nil {
		return err
	}
	return nil
}

func codeFreeCredentialMatchesBaseURL(cached codeFreeCachedCredentials, baseURL string) bool {
	if strings.TrimSpace(cached.BaseURLSnapshot) == "" {
		return true
	}
	requested := normalizeCodeFreeBaseURL(baseURL)
	if requested == "" {
		requested = codeFreeDefaultBaseURL
	}
	return normalizeCodeFreeBaseURL(codeFreeCredentialBaseURL(cached)) == requested
}

func codeFreeCredentialBaseURL(cached codeFreeCachedCredentials) string {
	return codeFreeFirstNonEmpty(cached.BaseURLSnapshot, codeFreeDefaultBaseURL)
}

func importCodeFreeLocalCredentialsLocked(baseURL string, dest string) (codeFreeStoredCredentials, error) {
	var lastErr error
	for _, source := range codeFreeLocalCredentialPaths() {
		if source == "" || source == dest {
			continue
		}
		stored, err := readCodeFreeStoredCredentialsAtPath(source)
		if err != nil {
			lastErr = err
			continue
		}
		if err := validateCodeFreeStoredCredentials(stored, baseURL); err != nil {
			lastErr = err
			continue
		}
		cached := normalizeCodeFreeCachedCredentials(stored.Cached)
		if cached.BaseURLSnapshot == "" {
			cached.BaseURLSnapshot = normalizeCodeFreeBaseURL(baseURL)
		}
		if cached.BaseURLSnapshot == "" {
			cached.BaseURLSnapshot = codeFreeDefaultBaseURL
		}
		if err := saveCodeFreeStoredCredentials(dest, cached); err != nil {
			return codeFreeStoredCredentials{}, err
		}
		return readCodeFreeStoredCredentialsAtPath(dest)
	}
	if lastErr != nil {
		return codeFreeStoredCredentials{}, lastErr
	}
	return codeFreeStoredCredentials{}, fmt.Errorf("providers: no usable codefree-o credentials found")
}

func normalizeCodeFreeCachedCredentials(cached codeFreeCachedCredentials) codeFreeCachedCredentials {
	baseURLSnapshot := strings.TrimSpace(cached.BaseURLSnapshot)
	if baseURLSnapshot != "" {
		baseURLSnapshot = normalizeCodeFreeBaseURL(baseURLSnapshot)
	}
	return codeFreeCachedCredentials{
		EncryptedAPIKey: strings.TrimSpace(cached.EncryptedAPIKey),
		UserID:          strings.TrimSpace(cached.UserID),
		SessionID:       strings.TrimSpace(cached.SessionID),
		BaseURLSnapshot: baseURLSnapshot,
	}
}

func codeFreeLocalCredentialPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".codefree-o", ".local", "share", codeFreeDefaultCredentialFile),
		filepath.Join(home, ".local", "share", "opencode", codeFreeDefaultCredentialFile),
		filepath.Join(home, ".codefree", "common", "agent", "core", ".codefree-o", codeFreeDefaultCredentialFile),
	}
}

func finalizeCodeFreeCredentials(stored codeFreeStoredCredentials) (codeFreeCredentials, error) {
	userID := strings.TrimSpace(stored.Cached.UserID)
	if userID == "" {
		return codeFreeCredentials{}, fmt.Errorf("providers: codefree credentials missing userId")
	}
	sessionID := strings.TrimSpace(stored.Cached.SessionID)
	if sessionID == "" {
		return codeFreeCredentials{}, fmt.Errorf("providers: codefree credentials missing sessionId")
	}
	apiKey, err := decryptCodeFreeAPIKey(stored.Cached.EncryptedAPIKey)
	if err != nil {
		return codeFreeCredentials{}, err
	}
	return codeFreeCredentials{
		UserID:         userID,
		APIKey:         apiKey,
		SessionID:      sessionID,
		BaseURL:        codeFreeCredentialBaseURL(stored.Cached),
		CredentialPath: stored.Path,
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

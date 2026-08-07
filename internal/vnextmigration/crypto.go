package vnextmigration

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type legacyCipher struct {
	aead cipher.AEAD
}

func newLegacyCipher(key []byte) (*legacyCipher, error) {
	if len(key) != 32 {
		return nil, errors.New("legacy master key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &legacyCipher{aead: aead}, nil
}

func (cipher *legacyCipher) open(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, errors.New("legacy encrypted secret is empty")
	}
	nonceSize := cipher.aead.NonceSize()
	if len(ciphertext) < nonceSize+cipher.aead.Overhead() {
		return nil, errors.New("legacy encrypted secret is malformed")
	}
	plaintext, err := cipher.aead.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], nil)
	if err != nil {
		return nil, errors.New("legacy encrypted secret cannot be decrypted with the supplied master key")
	}
	return plaintext, nil
}

type migratedAccountSecret struct {
	Authorization string `json:"authorization,omitempty"`
	AccessToken   string `json:"accessToken,omitempty"`
	RefreshToken  string `json:"refreshToken,omitempty"`
	Cookie        string `json:"cookie,omitempty"`
	ExpiresAt     *int64 `json:"expiresAt,omitempty"`
}

func convertLegacyAccountSecret(plaintext []byte) ([]byte, error) {
	var root map[string]any
	if err := json.Unmarshal(plaintext, &root); err != nil {
		return nil, errors.New("legacy site account secret payload is not valid JSON")
	}
	credentials := root
	if nested, ok := root["credentials"].(map[string]any); ok {
		credentials = nested
	}
	secret := migratedAccountSecret{
		Authorization: firstString(credentials, "authorization"),
		AccessToken:   firstString(credentials, "accessToken", "access_token"),
		RefreshToken:  firstString(credentials, "refreshToken", "refresh_token"),
		Cookie:        firstString(credentials, "cookie"),
	}
	if secret.Authorization == "" && secret.AccessToken != "" {
		secret.Authorization = "Bearer " + secret.AccessToken
	}
	if value, ok := firstValue(credentials, "expiresAt", "expires_at"); ok {
		expiresAt, err := parseUnixMilliseconds(value)
		if err != nil {
			return nil, fmt.Errorf("legacy site account expiry: %w", err)
		}
		if expiresAt > 0 {
			secret.ExpiresAt = &expiresAt
		}
	}
	if secret.Authorization == "" && secret.AccessToken == "" && secret.RefreshToken == "" && secret.Cookie == "" {
		return nil, errors.New("legacy site account secret contains no supported credential")
	}
	return json.Marshal(secret)
}

func firstString(values map[string]any, names ...string) string {
	value, ok := firstValue(values, names...)
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}

func firstValue(values map[string]any, names ...string) (any, bool) {
	for _, name := range names {
		if value, ok := values[name]; ok && value != nil {
			return value, true
		}
	}
	return nil, false
}

func parseUnixMilliseconds(value any) (int64, error) {
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, nil
		}
		parsed, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return 0, errors.New("expiry is not a Unix millisecond timestamp")
		}
		return parsed, nil
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, errors.New("expiry is not an integer Unix millisecond timestamp")
		}
		return parsed, nil
	case float64:
		if typed != float64(int64(typed)) {
			return 0, errors.New("expiry is not an integer Unix millisecond timestamp")
		}
		return int64(typed), nil
	default:
		return 0, errors.New("expiry has an unsupported representation")
	}
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

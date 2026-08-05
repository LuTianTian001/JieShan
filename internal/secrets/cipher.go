package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type Cipher struct {
	aead cipher.AEAD
}

func New(rawKey []byte) (*Cipher, error) {
	if len(rawKey) != 32 {
		return nil, fmt.Errorf("secret key must be 32 bytes, got %d", len(rawKey))
	}
	block, err := aes.NewCipher(rawKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

func LoadOrCreate(dataDir, configured string) (*Cipher, error) {
	key, err := resolveKey(dataDir, configured)
	if err != nil {
		return nil, err
	}
	return New(key)
}

func (c *Cipher) Encrypt(plain string) ([]byte, error) {
	if plain == "" {
		return nil, nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, []byte(plain), nil), nil
}

func (c *Cipher) Decrypt(ciphertext []byte) (string, error) {
	if len(ciphertext) == 0 {
		return "", nil
	}
	nonceSize := c.aead.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", errors.New("invalid encrypted value")
	}
	plain, err := c.aead.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], nil)
	if err != nil {
		return "", errors.New("cannot decrypt secret")
	}
	return string(plain), nil
}

func resolveKey(dataDir, configured string) ([]byte, error) {
	if configured != "" {
		decoded, err := hex.DecodeString(configured)
		if err != nil || len(decoded) != 32 {
			return nil, errors.New("JIESHAN_SECRET_KEY must be exactly 64 hexadecimal characters")
		}
		return decoded, nil
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, "secret.key")
	if encoded, err := os.ReadFile(path); err == nil {
		decoded, decodeErr := hex.DecodeString(string(encoded))
		if decodeErr == nil && len(decoded) == 32 {
			return decoded, nil
		}
		return nil, fmt.Errorf("invalid secret key file %s", path)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

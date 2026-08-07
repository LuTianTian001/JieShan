package runtimekey

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	masterKeyBytes = 32
	keyFileName    = "jieshan-secret.key"
)

var errKeyFileInitializing = errors.New("JieShan secret key file is still being initialized")

// LoadOrCreate resolves the VNext master key without ever logging or wrapping
// the key material in an error. A configured key takes precedence; otherwise
// a private, stable key file is created atomically in the runtime data folder.
func LoadOrCreate(dataDir, configured string) ([]byte, error) {
	if value := strings.TrimSpace(configured); value != "" {
		return decode(value, "configured JieShan secret key")
	}
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return nil, errors.New("JieShan data directory is required")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create VNext data directory: %w", err)
	}
	path := filepath.Join(dataDir, keyFileName)
	for attempt := 0; attempt < 12; attempt++ {
		key, err := read(path)
		if err == nil {
			return key, nil
		}
		if errors.Is(err, errKeyFileInitializing) {
			time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		key, err = generate()
		if err != nil {
			return nil, err
		}
		created, err := create(path, key)
		if err != nil {
			clear(key)
			return nil, err
		}
		if created {
			return key, nil
		}
		clear(key)
	}
	return nil, errors.New("JieShan secret key file could not be initialized")
}

func read(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("JieShan secret key path must be a regular file")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read JieShan secret key file: %w", err)
	}
	if len(strings.TrimSpace(string(encoded))) < hex.EncodedLen(masterKeyBytes) {
		return nil, errKeyFileInitializing
	}
	key, err := decode(strings.TrimSpace(string(encoded)), "JieShan secret key file")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		clear(key)
		return nil, fmt.Errorf("secure JieShan secret key file: %w", err)
	}
	return key, nil
}

func create(path string, key []byte) (bool, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("create JieShan secret key file: %w", err)
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = os.Remove(path)
		}
	}()
	encoded := make([]byte, hex.EncodedLen(len(key)))
	hex.Encode(encoded, key)
	written, err := file.Write(encoded)
	if err != nil {
		return false, fmt.Errorf("write JieShan secret key file: %w", err)
	}
	if written != len(encoded) {
		return false, fmt.Errorf("write JieShan secret key file: %w", io.ErrShortWrite)
	}
	if err := file.Sync(); err != nil {
		return false, fmt.Errorf("sync JieShan secret key file: %w", err)
	}
	if err := file.Close(); err != nil {
		return false, fmt.Errorf("close JieShan secret key file: %w", err)
	}
	complete = true
	return true, nil
}

func generate() ([]byte, error) {
	key := make([]byte, masterKeyBytes)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, errors.New("generate JieShan secret key")
	}
	return key, nil
}

func decode(value, source string) ([]byte, error) {
	if len(value) != hex.EncodedLen(masterKeyBytes) {
		return nil, fmt.Errorf("%s must contain exactly 64 hexadecimal characters", source)
	}
	key, err := hex.DecodeString(value)
	if err != nil || len(key) != masterKeyBytes {
		return nil, fmt.Errorf("%s must contain exactly 64 hexadecimal characters", source)
	}
	return key, nil
}

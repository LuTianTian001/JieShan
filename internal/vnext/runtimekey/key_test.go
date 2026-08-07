package runtimekey

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLoadOrCreateUsesConfiguredKeyWithoutWritingAFile(t *testing.T) {
	directory := t.TempDir()
	want := bytes.Repeat([]byte{0x7a}, masterKeyBytes)
	got, err := LoadOrCreate(directory, hex.EncodeToString(want))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("key mismatch")
	}
	if _, err := os.Stat(filepath.Join(directory, keyFileName)); !os.IsNotExist(err) {
		t.Fatalf("configured key unexpectedly wrote a file: %v", err)
	}
}

func TestLoadOrCreateRejectsMalformedConfiguredKey(t *testing.T) {
	for _, value := range []string{"short", strings.Repeat("z", 64), strings.Repeat("ab", 33)} {
		if _, err := LoadOrCreate(t.TempDir(), value); err == nil {
			t.Fatalf("LoadOrCreate(%q) succeeded", value)
		}
	}
}

func TestLoadOrCreatePersistsOneStablePrivateKey(t *testing.T) {
	directory := t.TempDir()
	first, err := LoadOrCreate(directory, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreate(directory, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != masterKeyBytes || !bytes.Equal(first, second) {
		t.Fatalf("persisted key mismatch")
	}
	encoded, err := os.ReadFile(filepath.Join(directory, keyFileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) != 64 || string(encoded) != hex.EncodeToString(first) {
		t.Fatalf("stored key is malformed")
	}
}

func TestLoadOrCreateConcurrentCallersShareTheSameKey(t *testing.T) {
	directory := t.TempDir()
	const callers = 12
	keys := make(chan []byte, callers)
	errors := make(chan error, callers)
	var group sync.WaitGroup
	for index := 0; index < callers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			key, err := LoadOrCreate(directory, "")
			if err != nil {
				errors <- err
				return
			}
			keys <- key
		}()
	}
	group.Wait()
	close(keys)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	var first []byte
	for key := range keys {
		if first == nil {
			first = key
			continue
		}
		if !bytes.Equal(first, key) {
			t.Fatal("concurrent callers received different keys")
		}
	}
}

func TestLoadOrCreateRejectsCorruptKeyFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, keyFileName)
	if err := os.WriteFile(path, []byte("not-a-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(directory, ""); err == nil {
		t.Fatal("corrupt key file was accepted")
	}
}

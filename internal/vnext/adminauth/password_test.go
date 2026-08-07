package adminauth

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func TestArgon2idVerifierRejectsUnsafeStoredParametersBeforeAllocation(t *testing.T) {
	salt := base64.RawStdEncoding.EncodeToString(make([]byte, 16))
	key := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	encoded := "$argon2id$v=19$m=999999,t=1,p=1$" + salt + "$" + key
	if _, err := verifyPassword(encoded, "irrelevant-password"); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("error = %v", err)
	}
}

func TestArgon2idHashRoundTrip(t *testing.T) {
	params := Argon2Params{MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, SaltBytes: 16, KeyBytes: 32}
	hash, err := hashPassword("a sufficiently long password", params, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := verifyPassword(hash, "a sufficiently long password")
	if err != nil || !valid {
		t.Fatalf("valid=%v err=%v", valid, err)
	}
	valid, err = verifyPassword(hash, "a different long password")
	if err != nil || valid {
		t.Fatalf("valid=%v err=%v", valid, err)
	}
}

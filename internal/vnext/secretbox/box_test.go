package secretbox

import (
	"bytes"
	"testing"
)

func newTestBox(t *testing.T) *Box {
	t.Helper()
	box, err := New(bytes.Repeat([]byte{0x42}, masterKeySize))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestSealRoundTrip(t *testing.T) {
	box := newTestBox(t)
	identity := Identity{RecordID: 10, OwnerID: 4}
	ciphertext, err := box.Seal(PurposeSiteCredential, identity, []byte("sk-upstream"))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := box.Open(PurposeSiteCredential, identity, ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if string(plaintext) != "sk-upstream" {
		t.Fatalf("unexpected plaintext %q", plaintext)
	}
}

func TestCiphertextCannotMoveAcrossRecordsOrPurposes(t *testing.T) {
	box := newTestBox(t)
	identity := Identity{RecordID: 10, OwnerID: 4}
	ciphertext, err := box.Seal(PurposeSiteCredential, identity, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		purpose  Purpose
		identity Identity
	}{
		{purpose: PurposeSiteCredential, identity: Identity{RecordID: 11, OwnerID: 4}},
		{purpose: PurposeSiteCredential, identity: Identity{RecordID: 10, OwnerID: 5}},
		{purpose: PurposeSiteSecretHeaders, identity: identity},
	} {
		if plaintext, err := box.Open(test.purpose, test.identity, ciphertext); err == nil {
			t.Fatalf("expected transplant to fail, decrypted %q", plaintext)
		}
	}
}

func TestSealUsesFreshNonce(t *testing.T) {
	box := newTestBox(t)
	identity := Identity{RecordID: 10, OwnerID: 4}
	first, err := box.Seal(PurposeSiteCredential, identity, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := box.Seal(PurposeSiteCredential, identity, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("expected a fresh nonce for every encryption")
	}
}

func TestTamperingAndUnknownVersionsFailClosed(t *testing.T) {
	box := newTestBox(t)
	identity := Identity{RecordID: 10, OwnerID: 4}
	ciphertext, err := box.Seal(PurposeSiteCredential, identity, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), ciphertext...)
	tampered[len(tampered)-1] ^= 0xff
	if plaintext, err := box.Open(PurposeSiteCredential, identity, tampered); err == nil {
		t.Fatalf("expected tampering to fail, decrypted %q", plaintext)
	}
	unknown := append([]byte(nil), ciphertext...)
	unknown[0] = 99
	if _, err := box.Open(PurposeSiteCredential, identity, unknown); err == nil {
		t.Fatal("expected unknown cipher version to fail")
	}
}

func TestNewRejectsWrongMasterKeyLength(t *testing.T) {
	if _, err := New([]byte("short")); err == nil {
		t.Fatal("expected short master key to fail")
	}
}

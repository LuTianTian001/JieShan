package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	masterKeySize   = 32
	cipherVersionV1 = byte(1)
)

type Purpose string

const (
	PurposeSiteCredential      Purpose = "site_credential"
	PurposeSiteSecretHeaders   Purpose = "site_secret_headers"
	PurposeSiteAdministration  Purpose = "site_administration"
	PurposeDownstreamKeyReveal Purpose = "downstream_key_reveal"
)

type Identity struct {
	RecordID int64
	OwnerID  int64
}

type Box struct {
	master [masterKeySize]byte
	random io.Reader
}

func New(master []byte) (*Box, error) {
	if len(master) != masterKeySize {
		return nil, fmt.Errorf("VNext secret master key must be %d bytes", masterKeySize)
	}
	box := &Box{random: rand.Reader}
	copy(box.master[:], master)
	return box, nil
}

func (box *Box) Seal(purpose Purpose, identity Identity, plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, errors.New("cannot encrypt an empty secret")
	}
	aead, associatedData, err := box.prepare(purpose, identity)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(box.random, nonce); err != nil {
		return nil, errors.New("generate secret nonce")
	}
	sealed := aead.Seal(nil, nonce, plaintext, associatedData)
	result := make([]byte, 1+len(nonce)+len(sealed))
	result[0] = cipherVersionV1
	copy(result[1:], nonce)
	copy(result[1+len(nonce):], sealed)
	return result, nil
}

func (box *Box) Open(purpose Purpose, identity Identity, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, errors.New("encrypted secret is empty")
	}
	if ciphertext[0] != cipherVersionV1 {
		return nil, errors.New("encrypted secret version is not supported")
	}
	aead, associatedData, err := box.prepare(purpose, identity)
	if err != nil {
		return nil, err
	}
	nonceSize := aead.NonceSize()
	if len(ciphertext) < 1+nonceSize+aead.Overhead() {
		return nil, errors.New("encrypted secret is malformed")
	}
	nonce := ciphertext[1 : 1+nonceSize]
	plaintext, err := aead.Open(nil, nonce, ciphertext[1+nonceSize:], associatedData)
	if err != nil {
		return nil, errors.New("encrypted secret authentication failed")
	}
	return plaintext, nil
}

func (box *Box) prepare(purpose Purpose, identity Identity) (cipher.AEAD, []byte, error) {
	if !knownPurpose(purpose) {
		return nil, nil, fmt.Errorf("unsupported secret purpose %q", purpose)
	}
	if identity.RecordID <= 0 || identity.OwnerID < 0 {
		return nil, nil, errors.New("secret identity is invalid")
	}
	key, err := hkdf.Key(sha256.New, box.master[:], []byte("JieShan/vnext/secretbox/v1"), string(purpose), masterKeySize)
	if err != nil {
		return nil, nil, errors.New("derive purpose-specific secret key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, errors.New("create secret cipher")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, errors.New("create authenticated secret cipher")
	}
	return aead, encodeAAD(purpose, identity), nil
}

func encodeAAD(purpose Purpose, identity Identity) []byte {
	purposeBytes := []byte(purpose)
	result := make([]byte, 1+2+len(purposeBytes)+8+8)
	result[0] = cipherVersionV1
	binary.BigEndian.PutUint16(result[1:3], uint16(len(purposeBytes)))
	copy(result[3:], purposeBytes)
	offset := 3 + len(purposeBytes)
	binary.BigEndian.PutUint64(result[offset:offset+8], uint64(identity.RecordID))
	binary.BigEndian.PutUint64(result[offset+8:offset+16], uint64(identity.OwnerID))
	return result
}

func knownPurpose(purpose Purpose) bool {
	switch purpose {
	case PurposeSiteCredential, PurposeSiteSecretHeaders, PurposeSiteAdministration, PurposeDownstreamKeyReveal:
		return true
	default:
		return false
	}
}

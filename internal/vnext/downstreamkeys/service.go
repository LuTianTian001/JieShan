package downstreamkeys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/secretbox"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const (
	randomKeyBytes    = 32
	displayPrefixSize = 9
	revealVersionV1   = int64(1)
)

var dummyKeyDigest = sha256.Sum256([]byte("JieShan/vnext/downstream-key/dummy/v1"))

var (
	ErrInvalidInput                   = errors.New("invalid downstream key input")
	ErrIssueFailed                    = errors.New("downstream key could not be issued")
	ErrRotateFailed                   = errors.New("downstream key could not be rotated")
	ErrKeyNotFound                    = errors.New("downstream key was not found")
	ErrInvalidKey                     = errors.New("invalid downstream API key")
	ErrAuthenticationUnavailable      = errors.New("downstream key authentication is unavailable")
	ErrListFailed                     = errors.New("downstream keys could not be listed")
	ErrRecentReauthenticationRequired = errors.New("recent administrator reauthentication is required")
	ErrNotRevealable                  = errors.New("downstream key is not revealable and must be rotated")
	ErrRevealFailed                   = errors.New("downstream key could not be revealed")
)

type CreateInput struct {
	Name                 string
	RoutingProfileID     *int64
	QuotaNanoUSD         *int64
	HourlyQuotaNanoUSD   *int64
	BillingMultiplierBPS *int
	ExpiresAt            *int64
	Enabled              bool
}

type IssuedKey struct {
	Key       vnextstore.DownstreamKey
	RawSecret string
}

type RecentReauthVerifier interface {
	VerifyRecentReauthentication(context.Context) error
}

type SecretCipher interface {
	Seal(secretbox.Purpose, secretbox.Identity, []byte) ([]byte, error)
	Open(secretbox.Purpose, secretbox.Identity, []byte) ([]byte, error)
}

type Repository interface {
	CreateRevealableDownstreamKey(context.Context, vnextstore.DownstreamKeyWrite, int64, func(int64) ([]byte, error)) (vnextstore.DownstreamKey, error)
	RotateDownstreamKeySecret(context.Context, int64, int64, string, []byte, []byte, int64) (vnextstore.DownstreamKey, error)
	GetDownstreamKeyRevealSecret(context.Context, int64) (vnextstore.DownstreamKeyRevealSecret, error)
	ListDownstreamKeyAuthCandidates(context.Context, string) ([]vnextstore.DownstreamKeyAuthCandidate, error)
	ListDownstreamKeys(context.Context) ([]vnextstore.DownstreamKey, error)
}

type Service struct {
	repository Repository
	cipher     SecretCipher
	reauth     RecentReauthVerifier
	random     io.Reader
	now        func() time.Time
	compare    func([]byte, []byte) int
}

func New(repository Repository, cipher SecretCipher, reauth RecentReauthVerifier) (*Service, error) {
	if repository == nil || cipher == nil {
		return nil, errors.New("downstream key repository and cipher are required")
	}
	return &Service{
		repository: repository,
		cipher:     cipher,
		reauth:     reauth,
		random:     rand.Reader,
		now:        time.Now,
		compare:    subtle.ConstantTimeCompare,
	}, nil
}

// Create returns the raw secret only after both its digest and record-bound
// encrypted reveal copy have committed successfully.
func (s *Service) Create(ctx context.Context, input CreateInput) (IssuedKey, error) {
	if !validInput(input) {
		return IssuedKey{}, ErrInvalidInput
	}
	raw, prefix, err := generateSecret(s.random)
	if err != nil {
		return IssuedKey{}, ErrIssueFailed
	}
	digest := vnextstore.DigestDownstreamKey(raw)
	item, err := s.repository.CreateRevealableDownstreamKey(ctx, vnextstore.DownstreamKeyWrite{
		Name:                 input.Name,
		RoutingProfileID:     input.RoutingProfileID,
		KeyPrefix:            prefix,
		KeyDigest:            digest,
		Enabled:              input.Enabled,
		QuotaNanoUSD:         input.QuotaNanoUSD,
		HourlyQuotaNanoUSD:   input.HourlyQuotaNanoUSD,
		BillingMultiplierBPS: input.BillingMultiplierBPS,
		ExpiresAt:            input.ExpiresAt,
	}, revealVersionV1, func(recordID int64) ([]byte, error) {
		return s.cipher.Seal(secretbox.PurposeDownstreamKeyReveal, downstreamKeyIdentity(recordID), []byte(raw))
	})
	if err != nil {
		if errors.Is(err, vnextstore.ErrConflict) {
			return IssuedKey{}, vnextstore.ErrConflict
		}
		return IssuedKey{}, ErrIssueFailed
	}
	return IssuedKey{Key: item, RawSecret: raw}, nil
}

// Rotate atomically replaces the digest and reveal ciphertext. The returned
// raw value is the only plaintext response produced by the rotation.
func (s *Service) Rotate(ctx context.Context, id, expectedRevision int64) (IssuedKey, error) {
	if id <= 0 {
		return IssuedKey{}, ErrKeyNotFound
	}
	if expectedRevision <= 0 {
		return IssuedKey{}, vnextstore.ErrRevisionConflict
	}
	raw, prefix, err := generateSecret(s.random)
	if err != nil {
		return IssuedKey{}, ErrRotateFailed
	}
	ciphertext, err := s.cipher.Seal(secretbox.PurposeDownstreamKeyReveal, downstreamKeyIdentity(id), []byte(raw))
	if err != nil {
		return IssuedKey{}, ErrRotateFailed
	}
	item, err := s.repository.RotateDownstreamKeySecret(
		ctx, id, expectedRevision, prefix, vnextstore.DigestDownstreamKey(raw), ciphertext, revealVersionV1,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return IssuedKey{}, ErrKeyNotFound
	}
	if err != nil {
		if errors.Is(err, vnextstore.ErrRevisionConflict) {
			return IssuedKey{}, vnextstore.ErrRevisionConflict
		}
		if errors.Is(err, vnextstore.ErrConflict) {
			return IssuedKey{}, vnextstore.ErrConflict
		}
		return IssuedKey{}, ErrRotateFailed
	}
	return IssuedKey{Key: item, RawSecret: raw}, nil
}

// Reveal is deliberately gated independently of normal administrator session
// authentication. Digest-only migrated keys remain usable but require rotation
// before they can be revealed.
func (s *Service) Reveal(ctx context.Context, id int64) (string, error) {
	if s.reauth == nil || s.reauth.VerifyRecentReauthentication(ctx) != nil {
		return "", ErrRecentReauthenticationRequired
	}
	if id <= 0 {
		return "", ErrKeyNotFound
	}
	stored, err := s.repository.GetDownstreamKeyRevealSecret(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrKeyNotFound
	}
	if err != nil {
		return "", ErrRevealFailed
	}
	if len(stored.EncryptedSecret) == 0 || stored.RevealVersion == 0 {
		return "", ErrNotRevealable
	}
	if stored.RevealVersion != revealVersionV1 {
		return "", ErrRevealFailed
	}
	plaintext, err := s.cipher.Open(secretbox.PurposeDownstreamKeyReveal, downstreamKeyIdentity(id), stored.EncryptedSecret)
	if err != nil {
		return "", ErrRevealFailed
	}
	raw := string(plaintext)
	clear(plaintext)
	return raw, nil
}

func (s *Service) Authenticate(ctx context.Context, raw string) (vnextstore.DownstreamKey, error) {
	digest := sha256.Sum256([]byte(raw))
	prefix, validPrefix := authenticationPrefix(raw)
	candidates, err := s.repository.ListDownstreamKeyAuthCandidates(ctx, prefix)
	if err != nil {
		return vnextstore.DownstreamKey{}, ErrAuthenticationUnavailable
	}
	var item vnextstore.DownstreamKey
	found := 0
	if len(candidates) == 0 {
		_ = s.compare(digest[:], dummyKeyDigest[:])
	}
	for _, candidate := range candidates {
		equal := s.compare(digest[:], candidate.KeyDigest[:])
		if equal == 1 {
			item = candidate.Key
		}
		found |= equal
	}
	if !validPrefix || found != 1 {
		return vnextstore.DownstreamKey{}, ErrInvalidKey
	}
	if !item.Enabled || (item.ExpiresAt != nil && *item.ExpiresAt <= s.now().UTC().UnixMilli()) {
		return vnextstore.DownstreamKey{}, ErrInvalidKey
	}
	return item, nil
}

func (s *Service) List(ctx context.Context) ([]vnextstore.DownstreamKey, error) {
	items, err := s.repository.ListDownstreamKeys(ctx)
	if err != nil {
		return nil, ErrListFailed
	}
	return items, nil
}

func generateSecret(random io.Reader) (raw, prefix string, err error) {
	bytes := make([]byte, randomKeyBytes)
	if _, err := io.ReadFull(random, bytes); err != nil {
		return "", "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(bytes)
	return "js_" + encoded, "js_" + encoded[:displayPrefixSize], nil
}

func authenticationPrefix(raw string) (string, bool) {
	length := len("js_") + displayPrefixSize
	if len(raw) < length || !strings.HasPrefix(raw, "js_") {
		return "", false
	}
	return raw[:length], true
}

func downstreamKeyIdentity(id int64) secretbox.Identity {
	return secretbox.Identity{RecordID: id, OwnerID: 0}
}

func validInput(input CreateInput) bool {
	if strings.TrimSpace(input.Name) == "" {
		return false
	}
	if input.QuotaNanoUSD != nil && *input.QuotaNanoUSD < 0 {
		return false
	}
	if input.HourlyQuotaNanoUSD != nil && *input.HourlyQuotaNanoUSD < 0 {
		return false
	}
	return input.BillingMultiplierBPS == nil ||
		(*input.BillingMultiplierBPS >= 0 && *input.BillingMultiplierBPS <= vnextstore.MaxBillingMultiplierBPS)
}

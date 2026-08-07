package downstreamkeys

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/LuTianTian001/JieShan/internal/vnext/secretbox"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func TestCreateCommitsRecordBoundSecretAndListsOnlyMetadata(t *testing.T) {
	service, repository := newServiceFixture(t, allowReauth{})
	hourlyQuota := int64(2_000_000_000)
	multiplier := 12_500
	issued, err := service.Create(context.Background(), CreateInput{
		Name: "Personal", Enabled: true, HourlyQuotaNanoUSD: &hourlyQuota, BillingMultiplierBPS: &multiplier,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertGeneratedSecret(t, issued.RawSecret)
	if issued.Key.KeyPrefix != issued.RawSecret[:len("js_")+displayPrefixSize] {
		t.Fatalf("display prefix = %q, raw = %q", issued.Key.KeyPrefix, issued.RawSecret)
	}
	if !issued.Key.Revealable || issued.Key.RevealVersion != revealVersionV1 {
		t.Fatalf("issued metadata = %+v", issued.Key)
	}
	if issued.Key.HourlyQuotaNanoUSD == nil || *issued.Key.HourlyQuotaNanoUSD != hourlyQuota ||
		issued.Key.BillingMultiplierBPS != multiplier || issued.Key.RPMLimit != 0 {
		t.Fatalf("issued billing policy = %+v", issued.Key)
	}

	var digest, ciphertext []byte
	if err := repository.DB.QueryRow(`SELECT key_digest,encrypted_secret FROM downstream_keys WHERE id=?`, issued.Key.ID).
		Scan(&digest, &ciphertext); err != nil {
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256([]byte(issued.RawSecret))
	if !bytes.Equal(digest, wantDigest[:]) {
		t.Fatal("stored digest is not SHA-256 of the issued key")
	}
	if bytes.Contains(ciphertext, []byte(issued.RawSecret)) {
		t.Fatal("encrypted reveal copy contains plaintext")
	}

	items, err := service.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(issued.RawSecret)) || bytes.Contains(encoded, ciphertext) {
		t.Fatalf("list response leaked secret material: %s", encoded)
	}
	revealed, err := service.Reveal(context.Background(), issued.Key.ID)
	if err != nil || revealed != issued.RawSecret {
		t.Fatalf("Reveal() = %q, %v", revealed, err)
	}
}

func TestCreateEncryptionFailureRollsBackAndDoesNotLeak(t *testing.T) {
	repository := newTestStore(t)
	service, err := New(repository, echoFailCipher{}, allowReauth{})
	if err != nil {
		t.Fatal(err)
	}
	randomBytes := bytes.Repeat([]byte{0x5a}, randomKeyBytes)
	service.random = bytes.NewReader(randomBytes)
	wouldBeRaw := "js_" + base64.RawURLEncoding.EncodeToString(randomBytes)

	issued, err := service.Create(context.Background(), CreateInput{Name: "Must roll back", Enabled: true})
	if !errors.Is(err, ErrIssueFailed) {
		t.Fatalf("Create() error = %v", err)
	}
	if issued.RawSecret != "" || strings.Contains(err.Error(), wouldBeRaw) {
		t.Fatalf("failed issue leaked raw key: issued=%+v error=%q", issued, err)
	}
	var count int
	if queryErr := repository.DB.QueryRow(`SELECT COUNT(*) FROM downstream_keys`).Scan(&count); queryErr != nil {
		t.Fatal(queryErr)
	}
	if count != 0 {
		t.Fatalf("failed transaction left %d downstream key rows", count)
	}
}

func TestCiphertextCannotBeTransplantedBetweenKeys(t *testing.T) {
	service, repository := newServiceFixture(t, allowReauth{})
	service.random = bytes.NewReader(append(bytes.Repeat([]byte{0x11}, randomKeyBytes), bytes.Repeat([]byte{0x22}, randomKeyBytes)...))
	first, err := service.Create(context.Background(), CreateInput{Name: "First", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(context.Background(), CreateInput{Name: "Second", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var firstCiphertext []byte
	if err := repository.DB.QueryRow(`SELECT encrypted_secret FROM downstream_keys WHERE id=?`, first.Key.ID).Scan(&firstCiphertext); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.DB.Exec(`UPDATE downstream_keys SET encrypted_secret=? WHERE id=?`, firstCiphertext, second.Key.ID); err != nil {
		t.Fatal(err)
	}
	if revealed, err := service.Reveal(context.Background(), second.Key.ID); !errors.Is(err, ErrRevealFailed) || revealed != "" {
		t.Fatalf("transplanted ciphertext Reveal() = %q, %v", revealed, err)
	}
}

func TestRotateImmediatelyInvalidatesOldDigest(t *testing.T) {
	service, _ := newServiceFixture(t, allowReauth{})
	issued, err := service.Create(context.Background(), CreateInput{Name: "Rotating", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(context.Background(), issued.RawSecret); err != nil {
		t.Fatalf("old key before rotation: %v", err)
	}
	rotated, err := service.Rotate(context.Background(), issued.Key.ID, issued.Key.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.RawSecret == issued.RawSecret {
		t.Fatal("rotation reused the old raw key")
	}
	if _, err := service.Authenticate(context.Background(), issued.RawSecret); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("old key after rotation error = %v", err)
	}
	if key, err := service.Authenticate(context.Background(), rotated.RawSecret); err != nil || key.ID != issued.Key.ID {
		t.Fatalf("new key authentication = %+v, %v", key, err)
	}
	if revealed, err := service.Reveal(context.Background(), issued.Key.ID); err != nil || revealed != rotated.RawSecret {
		t.Fatalf("rotated Reveal() = %q, %v", revealed, err)
	}
}

func TestRotateRequiresCurrentRevisionAndPreservesOldSecretOnConflict(t *testing.T) {
	service, _ := newServiceFixture(t, allowReauth{})
	issued, err := service.Create(context.Background(), CreateInput{Name: "CAS rotation", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Rotate(context.Background(), issued.Key.ID, issued.Key.Revision+1); !errors.Is(err, vnextstore.ErrRevisionConflict) {
		t.Fatalf("stale Rotate() error = %v", err)
	}
	if key, err := service.Authenticate(context.Background(), issued.RawSecret); err != nil || key.ID != issued.Key.ID {
		t.Fatalf("stale rotation changed active key = %+v, %v", key, err)
	}
}

func TestRevealRequiresIndependentRecentReauthentication(t *testing.T) {
	verifier := &recordingReauth{err: errors.New("reauth expired")}
	service, repository := newServiceFixture(t, verifier)
	issued, err := service.Create(context.Background(), CreateInput{Name: "Protected", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.DB.Exec(`DELETE FROM downstream_keys WHERE id=?`, issued.Key.ID); err != nil {
		t.Fatal(err)
	}
	if revealed, err := service.Reveal(context.Background(), issued.Key.ID); !errors.Is(err, ErrRecentReauthenticationRequired) || revealed != "" {
		t.Fatalf("Reveal() without reauth = %q, %v", revealed, err)
	}
	if verifier.calls != 1 {
		t.Fatalf("reauth verifier calls = %d", verifier.calls)
	}
}

func TestDigestOnlyMigratedKeyAuthenticatesButCannotBeRevealed(t *testing.T) {
	service, repository := newServiceFixture(t, allowReauth{})
	raw := "js_migrated_digest_only"
	id, err := repository.ImportDigestOnlyDownstreamKey(context.Background(), vnextstore.DownstreamKeyWrite{
		Name: "Migrated", KeyPrefix: raw[:len("js_")+displayPrefixSize], KeyDigest: vnextstore.DigestDownstreamKey(raw), Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := service.Authenticate(context.Background(), raw)
	if err != nil || key.ID != id || key.Revealable {
		t.Fatalf("migrated authentication = %+v, %v", key, err)
	}
	if revealed, err := service.Reveal(context.Background(), id); !errors.Is(err, ErrNotRevealable) || revealed != "" {
		t.Fatalf("migrated Reveal() = %q, %v", revealed, err)
	}
}

func TestAuthenticationErrorsDoNotEchoPresentedKey(t *testing.T) {
	service, _ := newServiceFixture(t, allowReauth{})
	raw := "js_this_must_never_appear_in_an_error"
	_, err := service.Authenticate(context.Background(), raw)
	if !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if strings.Contains(err.Error(), raw) {
		t.Fatalf("authentication error leaked presented key: %q", err)
	}
}

func TestAuthenticateDoesNotMisclassifyQuotaExhaustionAsInvalidKey(t *testing.T) {
	service, repository := newServiceFixture(t, allowReauth{})
	quota := int64(1)
	issued, err := service.Create(context.Background(), CreateInput{Name: "Exhausted", Enabled: true, QuotaNanoUSD: &quota})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.DB.Exec(`UPDATE downstream_keys SET used_nano_usd=? WHERE id=?`, quota, issued.Key.ID); err != nil {
		t.Fatal(err)
	}
	if key, err := service.Authenticate(context.Background(), issued.RawSecret); err != nil || key.ID != issued.Key.ID {
		t.Fatalf("quota-exhausted authentication = %+v, %v", key, err)
	}
}

func TestAuthenticateComparesEveryPrefixCandidateAndUsesDummyForEmptySet(t *testing.T) {
	service, repository := newServiceFixture(t, allowReauth{})
	prefix := "js_123456789"
	firstRaw := prefix + "_first"
	secondRaw := prefix + "_second"
	for index, raw := range []string{firstRaw, secondRaw} {
		if _, err := repository.ImportDigestOnlyDownstreamKey(context.Background(), vnextstore.DownstreamKeyWrite{
			Name: fmt.Sprintf("Candidate %d", index), KeyPrefix: prefix,
			KeyDigest: vnextstore.DigestDownstreamKey(raw), Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	comparisons := 0
	service.compare = func(left, right []byte) int {
		comparisons++
		return subtle.ConstantTimeCompare(left, right)
	}
	if _, err := service.Authenticate(context.Background(), secondRaw); err != nil {
		t.Fatal(err)
	}
	if comparisons != 2 {
		t.Fatalf("same-prefix digest comparisons = %d, want 2", comparisons)
	}

	comparisons = 0
	if _, err := service.Authenticate(context.Background(), "js_abcdefghi_missing"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("missing candidate authentication error = %v", err)
	}
	if comparisons != 1 {
		t.Fatalf("empty candidate set comparisons = %d, want one dummy comparison", comparisons)
	}
}

func TestDigestOnlyImportHasNoCallerSuppliedCiphertextSurface(t *testing.T) {
	repository := newTestStore(t)
	writeType := reflect.TypeOf(vnextstore.DownstreamKeyWrite{})
	for _, forbidden := range []string{"EncryptedSecret", "RevealVersion"} {
		if _, exists := writeType.FieldByName(forbidden); exists {
			t.Fatalf("DownstreamKeyWrite still exposes forbidden field %s", forbidden)
		}
	}
	raw := "js_digest_only_import"
	id, err := repository.ImportDigestOnlyDownstreamKey(context.Background(), vnextstore.DownstreamKeyWrite{
		Name: "Safe import", KeyPrefix: raw[:len("js_")+displayPrefixSize],
		KeyDigest: vnextstore.DigestDownstreamKey(raw), Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var encrypted []byte
	var revealVersion int64
	if err := repository.DB.QueryRow(`SELECT encrypted_secret,reveal_version FROM downstream_keys WHERE id=?`, id).
		Scan(&encrypted, &revealVersion); err != nil {
		t.Fatal(err)
	}
	if len(encrypted) != 0 || revealVersion != 0 {
		t.Fatalf("digest-only import stored reveal material: ciphertext=%d version=%d", len(encrypted), revealVersion)
	}
}

func newServiceFixture(t *testing.T, verifier RecentReauthVerifier) (*Service, *vnextstore.Store) {
	t.Helper()
	repository := newTestStore(t)
	box, err := secretbox.New(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(repository, box, verifier)
	if err != nil {
		t.Fatal(err)
	}
	return service, repository
}

func newTestStore(t *testing.T) *vnextstore.Store {
	t.Helper()
	repository, err := vnextstore.Open(context.Background(), filepath.Join(t.TempDir(), "vnext.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	return repository
}

func assertGeneratedSecret(t *testing.T, raw string) {
	t.Helper()
	if !strings.HasPrefix(raw, "js_") {
		t.Fatalf("raw key prefix = %q", raw)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, "js_"))
	if err != nil {
		t.Fatalf("raw key is not base64url: %v", err)
	}
	if len(decoded) != randomKeyBytes {
		t.Fatalf("decoded random key bytes = %d, want %d", len(decoded), randomKeyBytes)
	}
}

type allowReauth struct{}

func (allowReauth) VerifyRecentReauthentication(context.Context) error { return nil }

type recordingReauth struct {
	err   error
	calls int
}

func (verifier *recordingReauth) VerifyRecentReauthentication(context.Context) error {
	verifier.calls++
	return verifier.err
}

type echoFailCipher struct{}

func (echoFailCipher) Seal(_ secretbox.Purpose, _ secretbox.Identity, plaintext []byte) ([]byte, error) {
	return nil, fmt.Errorf("refused plaintext %s", plaintext)
}

func (echoFailCipher) Open(secretbox.Purpose, secretbox.Identity, []byte) ([]byte, error) {
	return nil, errors.New("not implemented")
}

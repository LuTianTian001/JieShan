package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/LuTianTian001/JieShan/internal/store"
)

const CookieName = "jieshan_session"

type Service struct {
	store *store.Store
	ttl   time.Duration
}

func New(s *store.Store, ttl time.Duration) *Service {
	return &Service{store: s, ttl: ttl}
}

func (s *Service) EnsureAdmin(ctx context.Context, configuredPassword string) (generatedPassword string, err error) {
	_, lookupErr := s.store.AdminByUsername(ctx, "admin")
	if configuredPassword == "" && lookupErr == nil {
		return "", nil
	}
	password := configuredPassword
	if password == "" {
		if !store.IsNotFound(lookupErr) {
			return "", lookupErr
		}
		password, err = randomToken(18)
		if err != nil {
			return "", err
		}
		generatedPassword = password
	}
	hash, err := HashPassword(password)
	if err != nil {
		return "", err
	}
	if err := s.store.UpsertAdmin(ctx, "admin", hash); err != nil {
		return "", err
	}
	return generatedPassword, nil
}

func (s *Service) Login(ctx context.Context, password string) (rawToken string, admin store.Admin, expiresAt int64, err error) {
	admin, err = s.store.AdminByUsername(ctx, "admin")
	if err != nil {
		return "", store.Admin{}, 0, err
	}
	if !VerifyPassword(password, admin.PasswordHash) {
		return "", store.Admin{}, 0, fmt.Errorf("invalid credentials")
	}
	rawToken, err = randomToken(32)
	if err != nil {
		return "", store.Admin{}, 0, err
	}
	expiresAt = time.Now().Add(s.ttl).UnixMilli()
	if err = s.store.CreateSession(ctx, rawToken, admin.ID, expiresAt); err != nil {
		return "", store.Admin{}, 0, err
	}
	return rawToken, admin, expiresAt, nil
}

func (s *Service) Authenticate(ctx context.Context, rawToken string) (store.Admin, error) {
	if rawToken == "" {
		return store.Admin{}, fmt.Errorf("missing session")
	}
	return s.store.SessionAdmin(ctx, rawToken, time.Now().UnixMilli())
}

func (s *Service) Logout(ctx context.Context, rawToken string) error {
	if rawToken == "" {
		return nil
	}
	return s.store.DeleteSession(ctx, rawToken)
}

func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	const memory = 64 * 1024
	const iterations = 1
	const parallelism = 2
	hash := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", memory, iterations, parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func VerifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return false
	}
	memory, okM := parseParam(params[0], "m=")
	iterations, okT := parseParam(params[1], "t=")
	parallelism, okP := parseParam(params[2], "p=")
	if !okM || !okT || !okP || memory > 256*1024 || iterations > 10 || parallelism > 16 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, uint32(iterations), uint32(memory), uint8(parallelism), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func GenerateAPIKey() (raw, prefix string, err error) {
	token, err := randomToken(32)
	if err != nil {
		return "", "", err
	}
	raw = "js_" + token
	prefix = raw
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	return raw, prefix, nil
}

func parseParam(value, prefix string) (int, bool) {
	if !strings.HasPrefix(value, prefix) {
		return 0, false
	}
	parsed, err := strconv.Atoi(strings.TrimPrefix(value, prefix))
	return parsed, err == nil && parsed > 0
}

func randomToken(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

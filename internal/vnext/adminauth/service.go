package adminauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const opaqueTokenBytes = 32

type Options struct {
	InitialPassword           string
	PersistGeneratedPassword  func(string) error
	Argon2                    Argon2Params
	SessionTTL                time.Duration
	SessionTouchInterval      time.Duration
	RecentReauthenticationTTL time.Duration
	MaxLoginFailures          int
	LoginWindow               time.Duration
	LoginLockout              time.Duration
	AllowedOrigins            []string
	TrustProxyHeaders         bool
	SecureCookies             *bool
	Now                       func() time.Time
	Random                    io.Reader
	ClientKey                 func(*http.Request) string
}

type Service struct {
	repository                Repository
	persistGeneratedPassword  func(string) error
	argon2                    Argon2Params
	sessionTTL                time.Duration
	sessionTouchInterval      time.Duration
	recentReauthenticationTTL time.Duration
	allowedOrigins            map[string]struct{}
	trustProxyHeaders         bool
	secureCookies             *bool
	now                       func() time.Time
	random                    io.Reader
	randomMu                  sync.Mutex
	clientKey                 func(*http.Request) string
	limiter                   *loginLimiter
}

func NewService(ctx context.Context, repository Repository, options Options) (*Service, Bootstrap, error) {
	if repository == nil {
		return nil, Bootstrap{}, errors.New("administrator repository is required")
	}
	normalized, err := normalizeOptions(options)
	if err != nil {
		return nil, Bootstrap{}, err
	}
	service := &Service{
		repository:                repository,
		persistGeneratedPassword:  normalized.PersistGeneratedPassword,
		argon2:                    normalized.Argon2,
		sessionTTL:                normalized.SessionTTL,
		sessionTouchInterval:      normalized.SessionTouchInterval,
		recentReauthenticationTTL: normalized.RecentReauthenticationTTL,
		allowedOrigins:            normalized.allowedOrigins,
		trustProxyHeaders:         normalized.TrustProxyHeaders,
		secureCookies:             normalized.SecureCookies,
		now:                       normalized.Now,
		random:                    normalized.Random,
		clientKey:                 normalized.ClientKey,
		limiter: newLoginLimiter(
			normalized.MaxLoginFailures, normalized.LoginWindow, normalized.LoginLockout,
		),
	}
	bootstrap, err := service.ensureAdministrator(ctx, normalized.InitialPassword)
	if err != nil {
		return nil, Bootstrap{}, err
	}
	if _, err := service.CleanupExpiredSessions(ctx); err != nil {
		return nil, Bootstrap{}, fmt.Errorf("clean expired administrator sessions: %w", err)
	}
	return service, bootstrap, nil
}

type normalizedOptions struct {
	Options
	allowedOrigins map[string]struct{}
}

func normalizeOptions(input Options) (normalizedOptions, error) {
	params, err := normalizeArgon2Params(input.Argon2)
	if err != nil {
		return normalizedOptions{}, err
	}
	input.Argon2 = params
	if input.SessionTTL == 0 {
		input.SessionTTL = 24 * time.Hour
	}
	if input.SessionTTL < 5*time.Minute || input.SessionTTL > 30*24*time.Hour {
		return normalizedOptions{}, errors.New("administrator session TTL must be between 5 minutes and 30 days")
	}
	if input.SessionTouchInterval == 0 {
		input.SessionTouchInterval = 5 * time.Minute
	}
	if input.SessionTouchInterval < 0 || input.SessionTouchInterval > input.SessionTTL {
		return normalizedOptions{}, errors.New("administrator session touch interval is invalid")
	}
	if input.RecentReauthenticationTTL == 0 {
		input.RecentReauthenticationTTL = 5 * time.Minute
	}
	if input.RecentReauthenticationTTL < time.Second || input.RecentReauthenticationTTL > input.SessionTTL {
		return normalizedOptions{}, errors.New("administrator recent reauthentication TTL is invalid")
	}
	if input.MaxLoginFailures == 0 {
		input.MaxLoginFailures = 5
	}
	if input.MaxLoginFailures < 2 || input.MaxLoginFailures > 20 {
		return normalizedOptions{}, errors.New("administrator login failure limit must be between 2 and 20")
	}
	if input.LoginWindow == 0 {
		input.LoginWindow = 5 * time.Minute
	}
	if input.LoginLockout == 0 {
		input.LoginLockout = 15 * time.Minute
	}
	if input.LoginWindow < time.Second || input.LoginWindow > time.Hour ||
		input.LoginLockout < time.Second || input.LoginLockout > 24*time.Hour {
		return normalizedOptions{}, errors.New("administrator login rate-limit durations are invalid")
	}
	if input.Now == nil {
		input.Now = func() time.Time { return time.Now().UTC() }
	}
	if input.Random == nil {
		input.Random = rand.Reader
	}
	if input.ClientKey == nil {
		input.ClientKey = defaultClientKey
	}
	origins, err := normalizeAllowedOrigins(input.AllowedOrigins)
	if err != nil {
		return normalizedOptions{}, err
	}
	return normalizedOptions{Options: input, allowedOrigins: origins}, nil
}

func (service *Service) ensureAdministrator(ctx context.Context, explicitPassword string) (Bootstrap, error) {
	admin, err := service.repository.GetAdminUser(ctx, AdminUsername)
	if err == nil {
		if admin.ID != 1 || admin.Username != AdminUsername || strings.TrimSpace(admin.PasswordHash) == "" {
			return Bootstrap{}, errors.New("stored VNext administrator is invalid")
		}
		return Bootstrap{}, nil
	}
	if !errors.Is(err, ErrAdminNotFound) {
		return Bootstrap{}, fmt.Errorf("read VNext administrator: %w", err)
	}
	password := explicitPassword
	generated := false
	if password == "" {
		value, err := service.randomValue(24)
		if err != nil {
			return Bootstrap{}, fmt.Errorf("generate bootstrap administrator password: %w", err)
		}
		password = value
		generated = true
	}
	if err := validateInitialPassword(password); err != nil {
		return Bootstrap{}, err
	}
	if generated && service.persistGeneratedPassword != nil {
		if err := service.persistGeneratedPassword(password); err != nil {
			return Bootstrap{}, fmt.Errorf("persist generated administrator password: %w", err)
		}
	}
	service.randomMu.Lock()
	passwordHash, err := hashPassword(password, service.argon2, service.random)
	service.randomMu.Unlock()
	if err != nil {
		return Bootstrap{}, err
	}
	now := service.now().UTC().UnixMilli()
	admin, created, err := service.repository.EnsureAdminUser(ctx, AdminUser{
		ID: 1, Username: AdminUsername, PasswordHash: passwordHash,
		PasswordChangedAt: now, Revision: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return Bootstrap{}, fmt.Errorf("initialize VNext administrator: %w", err)
	}
	if admin.ID != 1 || admin.Username != AdminUsername {
		return Bootstrap{}, errors.New("initialized VNext administrator is invalid")
	}
	result := Bootstrap{Created: created}
	if created && generated && service.persistGeneratedPassword == nil {
		result.GeneratedPassword = password
	}
	return result, nil
}

func (service *Service) Login(ctx context.Context, username, password, clientKey string) (LoginResult, error) {
	now := service.now().UTC()
	clientKey = normalizeClientKey(clientKey)
	if retry := service.limiter.before(clientKey, now); retry > 0 {
		return LoginResult{}, &RateLimitError{RetryAfter: retry}
	}
	admin, err := service.repository.GetAdminUser(ctx, AdminUsername)
	if err != nil {
		return LoginResult{}, fmt.Errorf("read VNext administrator: %w", err)
	}
	passwordValid, err := verifyPassword(admin.PasswordHash, password)
	if err != nil {
		return LoginResult{}, fmt.Errorf("verify VNext administrator password: %w", err)
	}
	usernameValid := subtle.ConstantTimeCompare([]byte(username), []byte(AdminUsername)) == 1
	if !passwordValid || !usernameValid {
		if retry := service.limiter.failure(clientKey, now); retry > 0 {
			return LoginResult{}, &RateLimitError{RetryAfter: retry}
		}
		return LoginResult{}, ErrInvalidCredentials
	}
	sessionToken, err := service.randomValue(opaqueTokenBytes)
	if err != nil {
		return LoginResult{}, fmt.Errorf("generate administrator session: %w", err)
	}
	csrfToken, err := service.randomValue(opaqueTokenBytes)
	if err != nil {
		return LoginResult{}, fmt.Errorf("generate administrator CSRF token: %w", err)
	}
	tokenHash := sha256.Sum256([]byte(sessionToken))
	csrfHash := sha256.Sum256([]byte(csrfToken))
	expiresAt := now.Add(service.sessionTTL)
	session := Session{
		TokenHash: tokenHash, AdminUserID: admin.ID, AdminUsername: admin.Username, CSRFHash: csrfHash,
		ExpiresAt: expiresAt.UnixMilli(), LastSeenAt: now.UnixMilli(), CreatedAt: now.UnixMilli(),
	}
	if err := service.repository.CreateAdminSession(ctx, session, admin.Revision); err != nil {
		if errors.Is(err, ErrAdminRevisionConflict) {
			return LoginResult{}, ErrInvalidCredentials
		}
		return LoginResult{}, fmt.Errorf("create administrator session: %w", err)
	}
	service.limiter.success(clientKey)
	return LoginResult{
		Principal:    Principal{AdminUserID: admin.ID, Username: admin.Username, ExpiresAt: session.ExpiresAt},
		SessionToken: sessionToken, CSRFToken: csrfToken, ExpiresAt: expiresAt,
	}, nil
}

func (service *Service) ChangePassword(
	ctx context.Context,
	authenticated AuthenticatedSession,
	currentPassword string,
	newPassword string,
	confirmation string,
) error {
	if service == nil || authenticated.Principal.AdminUserID != 1 || authenticated.Principal.Username != AdminUsername ||
		authenticated.Session.AdminUserID != 1 || authenticated.Session.AdminUsername != AdminUsername {
		return ErrUnauthenticated
	}
	if subtle.ConstantTimeCompare([]byte(newPassword), []byte(confirmation)) != 1 {
		return ErrPasswordConfirmationMismatch
	}
	if err := validateNewPassword(newPassword); err != nil {
		return err
	}
	admin, err := service.repository.GetAdminUser(ctx, AdminUsername)
	if err != nil {
		return fmt.Errorf("read VNext administrator: %w", err)
	}
	passwordValid, err := verifyPassword(admin.PasswordHash, currentPassword)
	if err != nil {
		return fmt.Errorf("verify VNext administrator password: %w", err)
	}
	if !passwordValid {
		return ErrInvalidCredentials
	}
	if subtle.ConstantTimeCompare([]byte(currentPassword), []byte(newPassword)) == 1 {
		return ErrPasswordUnchanged
	}
	service.randomMu.Lock()
	passwordHash, err := hashPassword(newPassword, service.argon2, service.random)
	service.randomMu.Unlock()
	if err != nil {
		return fmt.Errorf("hash VNext administrator password: %w", err)
	}
	changedAt := service.now().UTC().UnixMilli()
	if changedAt <= admin.PasswordChangedAt {
		changedAt = admin.PasswordChangedAt + 1
	}
	if err := service.repository.ChangeAdminPassword(
		ctx,
		admin.ID,
		admin.Revision,
		passwordHash,
		changedAt,
		authenticated.Session.TokenHash,
	); err != nil {
		return fmt.Errorf("change VNext administrator password: %w", err)
	}
	return nil
}

func (service *Service) AuthenticateToken(ctx context.Context, rawToken string) (AuthenticatedSession, error) {
	tokenHash, err := digestOpaqueToken(rawToken)
	if err != nil {
		return AuthenticatedSession{}, ErrUnauthenticated
	}
	session, err := service.repository.GetAdminSession(ctx, tokenHash)
	if errors.Is(err, ErrSessionNotFound) {
		return AuthenticatedSession{}, ErrUnauthenticated
	}
	if err != nil {
		return AuthenticatedSession{}, fmt.Errorf("read administrator session: %w", err)
	}
	now := service.now().UTC().UnixMilli()
	if session.ExpiresAt <= now {
		_ = service.repository.DeleteAdminSession(ctx, tokenHash)
		return AuthenticatedSession{}, ErrUnauthenticated
	}
	if session.AdminUserID != 1 || session.AdminUsername != AdminUsername {
		return AuthenticatedSession{}, ErrUnauthenticated
	}
	if service.sessionTouchInterval == 0 || now-session.LastSeenAt >= service.sessionTouchInterval.Milliseconds() {
		if err := service.repository.TouchAdminSession(ctx, tokenHash, now); err != nil {
			if errors.Is(err, ErrSessionNotFound) {
				return AuthenticatedSession{}, ErrUnauthenticated
			}
			return AuthenticatedSession{}, fmt.Errorf("touch administrator session: %w", err)
		}
		session.LastSeenAt = now
	}
	principal := Principal{AdminUserID: session.AdminUserID, Username: session.AdminUsername, ExpiresAt: session.ExpiresAt}
	return AuthenticatedSession{Principal: principal, Session: session}, nil
}

func (service *Service) AuthenticateRequest(r *http.Request) (AuthenticatedSession, error) {
	if r == nil {
		return AuthenticatedSession{}, ErrUnauthenticated
	}
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		return AuthenticatedSession{}, ErrUnauthenticated
	}
	return service.AuthenticateToken(r.Context(), cookie.Value)
}

// VerifyRecentReauthentication accepts only the authentication proof attached
// by WrapAdmin. Session touches do not extend this window; signing in again
// creates a new session and therefore a new proof.
func (service *Service) VerifyRecentReauthentication(ctx context.Context) error {
	if service == nil || ctx == nil {
		return ErrRecentReauthenticationRequired
	}
	session, ok := ctx.Value(authenticatedSessionContextKey{}).(Session)
	if !ok || session.AdminUserID != 1 || session.AdminUsername != AdminUsername || session.CreatedAt <= 0 {
		return ErrRecentReauthenticationRequired
	}
	now := service.now().UTC().UnixMilli()
	if session.CreatedAt > now || session.ExpiresAt <= now || now-session.CreatedAt > service.recentReauthenticationTTL.Milliseconds() {
		return ErrRecentReauthenticationRequired
	}
	return nil
}

func (service *Service) VerifyCSRF(r *http.Request, session Session) error {
	if r == nil {
		return ErrCSRFRejected
	}
	cookie, err := r.Cookie(CSRFCookieName)
	if err != nil || cookie.Value == "" {
		return ErrCSRFRejected
	}
	header := strings.TrimSpace(r.Header.Get(CSRFHeaderName))
	if header == "" || subtle.ConstantTimeCompare([]byte(header), []byte(cookie.Value)) != 1 {
		return ErrCSRFRejected
	}
	hash, err := digestOpaqueToken(cookie.Value)
	if err != nil || subtle.ConstantTimeCompare(hash[:], session.CSRFHash[:]) != 1 {
		return ErrCSRFRejected
	}
	return nil
}

func (service *Service) LogoutToken(ctx context.Context, rawToken string) error {
	hash, err := digestOpaqueToken(rawToken)
	if err != nil {
		return nil
	}
	return service.repository.DeleteAdminSession(ctx, hash)
}

func (service *Service) CleanupExpiredSessions(ctx context.Context) (int64, error) {
	return service.repository.DeleteExpiredAdminSessions(ctx, service.now().UTC().UnixMilli())
}

func (service *Service) randomValue(bytes int) (string, error) {
	buffer := make([]byte, bytes)
	service.randomMu.Lock()
	_, err := io.ReadFull(service.random, buffer)
	service.randomMu.Unlock()
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func digestOpaqueToken(raw string) ([32]byte, error) {
	var zero [32]byte
	if len(raw) < 40 || len(raw) > 64 {
		return zero, errors.New("opaque token length is invalid")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) != opaqueTokenBytes || base64.RawURLEncoding.EncodeToString(decoded) != raw {
		return zero, errors.New("opaque token is invalid")
	}
	return sha256.Sum256([]byte(raw)), nil
}

func normalizeClientKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	if len(value) > 256 {
		value = value[:256]
	}
	return value
}

func defaultClientKey(r *http.Request) string {
	if r == nil {
		return "unknown"
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	return normalizeClientKey(r.RemoteAddr)
}

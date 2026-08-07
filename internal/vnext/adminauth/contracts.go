package adminauth

import (
	"context"
	"errors"
	"net/http"
	"time"
)

const (
	AdminUsername     = "admin"
	AuthAPIPrefix     = "/api/vnext/auth"
	SessionCookieName = "jieshan_admin_session"
	CSRFCookieName    = "jieshan_admin_csrf"
	CSRFHeaderName    = "X-CSRF-Token"
)

var (
	ErrAdminNotFound                  = errors.New("VNext administrator not found")
	ErrSessionNotFound                = errors.New("VNext administrator session not found")
	ErrInvalidCredentials             = errors.New("invalid administrator credentials")
	ErrRateLimited                    = errors.New("administrator login is rate limited")
	ErrUnauthenticated                = errors.New("administrator session is unauthenticated")
	ErrRecentReauthenticationRequired = errors.New("recent administrator reauthentication is required")
	ErrOriginRejected                 = errors.New("administrator request origin was rejected")
	ErrCSRFRejected                   = errors.New("administrator CSRF validation failed")
)

type AdminUser struct {
	ID                int64
	Username          string
	PasswordHash      string
	PasswordChangedAt int64
	Revision          int64
	CreatedAt         int64
	UpdatedAt         int64
}

type Session struct {
	TokenHash     [32]byte
	AdminUserID   int64
	AdminUsername string
	CSRFHash      [32]byte
	ExpiresAt     int64
	LastSeenAt    int64
	CreatedAt     int64
}

type Repository interface {
	GetAdminUser(context.Context, string) (AdminUser, error)
	EnsureAdminUser(context.Context, AdminUser) (AdminUser, bool, error)
	CreateAdminSession(context.Context, Session) error
	GetAdminSession(context.Context, [32]byte) (Session, error)
	TouchAdminSession(context.Context, [32]byte, int64) error
	DeleteAdminSession(context.Context, [32]byte) error
	DeleteExpiredAdminSessions(context.Context, int64) (int64, error)
}

type Bootstrap struct {
	Created           bool
	GeneratedPassword string
}

type Principal struct {
	AdminUserID int64  `json:"-"`
	Username    string `json:"username"`
	ExpiresAt   int64  `json:"expires_at"`
}

type AuthenticatedSession struct {
	Principal Principal
	Session   Session
}

type LoginResult struct {
	Principal    Principal
	SessionToken string
	CSRFToken    string
	ExpiresAt    time.Time
}

type RateLimitError struct {
	RetryAfter time.Duration
}

func (err *RateLimitError) Error() string {
	return ErrRateLimited.Error()
}

func (err *RateLimitError) Unwrap() error {
	return ErrRateLimited
}

type principalContextKey struct{}
type authenticatedSessionContextKey struct{}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}

func withPrincipal(request *http.Request, principal Principal) *http.Request {
	return request.WithContext(context.WithValue(request.Context(), principalContextKey{}, principal))
}

func withAuthenticatedSession(request *http.Request, authenticated AuthenticatedSession) *http.Request {
	request = withPrincipal(request, authenticated.Principal)
	return request.WithContext(context.WithValue(request.Context(), authenticatedSessionContextKey{}, authenticated.Session))
}

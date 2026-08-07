package adminauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

const maxAuthBodyBytes = 64 << 10

type Handler struct {
	service *Service
}

func NewHandler(service *Service) (*Handler, error) {
	if service == nil {
		return nil, errors.New("administrator authentication service is required")
	}
	return &Handler{service: service}, nil
}

func (handler *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if handler == nil || handler.service == nil {
		writeAuthError(w, http.StatusServiceUnavailable, "auth_unavailable", "Administrator authentication is unavailable.")
		return
	}
	switch r.URL.Path {
	case AuthAPIPrefix + "/status":
		handler.status(w, r)
	case AuthAPIPrefix + "/login":
		handler.login(w, r)
	case AuthAPIPrefix + "/logout":
		handler.logout(w, r)
	case AuthAPIPrefix + "/password":
		handler.changePassword(w, r)
	default:
		writeAuthError(w, http.StatusNotFound, "not_found", "Authentication resource was not found.")
	}
}

func (handler *Handler) status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		authMethodNotAllowed(w, http.MethodGet)
		return
	}
	response := authStatusResponse{Initialized: true}
	authenticated, err := handler.service.AuthenticateRequest(r)
	if err == nil {
		response.Authenticated = true
		response.Username = authenticated.Principal.Username
		response.ExpiresAt = authenticated.Principal.ExpiresAt
	} else if errors.Is(err, ErrUnauthenticated) {
		clearAuthCookies(w, r, handler.service)
	} else {
		writeAuthError(w, http.StatusInternalServerError, "auth_unavailable", "Administrator session could not be checked.")
		return
	}
	writeAuthJSON(w, http.StatusOK, response)
}

func (handler *Handler) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		authMethodNotAllowed(w, http.MethodPost)
		return
	}
	if err := handler.service.VerifyOrigin(r); err != nil {
		writeAuthError(w, http.StatusForbidden, "origin_rejected", "Login origin was rejected.")
		return
	}
	var input loginRequest
	if status, err := decodeAuthJSON(w, r, &input); err != nil {
		writeAuthError(w, status, "invalid_request", err.Error())
		return
	}
	result, err := handler.service.Login(r.Context(), input.Username, input.Password, handler.service.clientKey(r))
	if err != nil {
		switch {
		case errors.Is(err, ErrRateLimited):
			var limited *RateLimitError
			retry := time.Second
			if errors.As(err, &limited) && limited.RetryAfter > 0 {
				retry = limited.RetryAfter
			}
			seconds := int(math.Ceil(retry.Seconds()))
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", fmt.Sprintf("%d", seconds))
			writeAuthError(w, http.StatusTooManyRequests, "login_rate_limited", "Too many failed login attempts.")
		case errors.Is(err, ErrInvalidCredentials):
			writeAuthError(w, http.StatusUnauthorized, "invalid_credentials", "Administrator credentials are invalid.")
		default:
			writeAuthError(w, http.StatusInternalServerError, "auth_unavailable", "Administrator login could not be completed.")
		}
		return
	}
	if existing, cookieErr := r.Cookie(SessionCookieName); cookieErr == nil && existing.Value != result.SessionToken {
		_ = handler.service.LogoutToken(r.Context(), existing.Value)
	}
	setAuthCookies(w, r, handler.service, result)
	writeAuthJSON(w, http.StatusOK, authStatusResponse{
		Initialized: true, Authenticated: true, Username: result.Principal.Username,
		ExpiresAt: result.Principal.ExpiresAt,
	})
}

func (handler *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		authMethodNotAllowed(w, http.MethodPost)
		return
	}
	authenticated, err := handler.service.AuthenticateRequest(r)
	if err != nil {
		if errors.Is(err, ErrUnauthenticated) {
			clearAuthCookies(w, r, handler.service)
			writeAuthError(w, http.StatusUnauthorized, "unauthenticated", "Administrator session is not authenticated.")
		} else {
			writeAuthError(w, http.StatusInternalServerError, "auth_unavailable", "Administrator session could not be checked.")
		}
		return
	}
	if err := handler.service.VerifyOrigin(r); err != nil {
		writeAuthError(w, http.StatusForbidden, "origin_rejected", "Password change origin was rejected.")
		return
	}
	if err := handler.service.VerifyCSRF(r, authenticated.Session); err != nil {
		writeAuthError(w, http.StatusForbidden, "csrf_rejected", "Password change CSRF validation failed.")
		return
	}
	var input passwordChangeRequest
	if status, err := decodeAuthJSON(w, r, &input); err != nil {
		writeAuthError(w, status, "invalid_request", err.Error())
		return
	}
	if err := handler.service.ChangePassword(
		r.Context(), authenticated, input.CurrentPassword, input.NewPassword, input.ConfirmPassword,
	); err != nil {
		switch {
		case errors.Is(err, ErrInvalidCredentials):
			writeAuthError(w, http.StatusUnauthorized, "invalid_current_password", "Current administrator password is invalid.")
		case errors.Is(err, ErrPasswordConfirmationMismatch):
			writeAuthError(w, http.StatusBadRequest, "password_confirmation_mismatch", "New password confirmation does not match.")
		case errors.Is(err, ErrPasswordUnchanged):
			writeAuthError(w, http.StatusBadRequest, "password_unchanged", "New password must differ from the current password.")
		case errors.Is(err, ErrPasswordTooShort):
			writeAuthError(w, http.StatusBadRequest, "password_too_short", "New password must contain at least 12 characters.")
		case errors.Is(err, ErrPasswordTooLong):
			writeAuthError(w, http.StatusBadRequest, "password_too_long", "New password must not exceed 1024 bytes.")
		case errors.Is(err, ErrAdminRevisionConflict):
			writeAuthError(w, http.StatusConflict, "password_change_conflict", "Administrator password changed concurrently; retry with the current password.")
		default:
			writeAuthError(w, http.StatusInternalServerError, "auth_unavailable", "Administrator password could not be changed.")
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		authMethodNotAllowed(w, http.MethodPost)
		return
	}
	authenticated, err := handler.service.AuthenticateRequest(r)
	if err != nil {
		clearAuthCookies(w, r, handler.service)
		if errors.Is(err, ErrUnauthenticated) {
			writeAuthError(w, http.StatusUnauthorized, "unauthenticated", "Administrator session is not authenticated.")
		} else {
			writeAuthError(w, http.StatusInternalServerError, "auth_unavailable", "Administrator session could not be checked.")
		}
		return
	}
	if err := handler.service.VerifyOrigin(r); err != nil {
		writeAuthError(w, http.StatusForbidden, "origin_rejected", "Logout origin was rejected.")
		return
	}
	if err := handler.service.VerifyCSRF(r, authenticated.Session); err != nil {
		writeAuthError(w, http.StatusForbidden, "csrf_rejected", "Logout CSRF validation failed.")
		return
	}
	if cookie, err := r.Cookie(SessionCookieName); err == nil {
		if err := handler.service.LogoutToken(r.Context(), cookie.Value); err != nil {
			writeAuthError(w, http.StatusInternalServerError, "auth_unavailable", "Administrator logout could not be completed.")
			return
		}
	}
	clearAuthCookies(w, r, handler.service)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// WrapAdmin directly satisfies runtime.AdminMiddleware without importing the
// runtime composition package.
func (service *Service) WrapAdmin(next http.Handler) http.Handler {
	if service == nil || next == nil {
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authenticated, err := service.AuthenticateRequest(r)
		if err != nil {
			if errors.Is(err, ErrUnauthenticated) {
				clearAuthCookies(w, r, service)
				writeAuthError(w, http.StatusUnauthorized, "unauthenticated", "Administrator session is not authenticated.")
			} else {
				writeAuthError(w, http.StatusInternalServerError, "auth_unavailable", "Administrator session could not be checked.")
			}
			return
		}
		if requiresCSRF(r.Method) {
			if err := service.VerifyOrigin(r); err != nil {
				writeAuthError(w, http.StatusForbidden, "origin_rejected", "Administrator request origin was rejected.")
				return
			}
			if err := service.VerifyCSRF(r, authenticated.Session); err != nil {
				writeAuthError(w, http.StatusForbidden, "csrf_rejected", "Administrator CSRF validation failed.")
				return
			}
		}
		next.ServeHTTP(w, withAuthenticatedSession(r, authenticated))
	})
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type passwordChangeRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
	ConfirmPassword string `json:"confirmPassword"`
}

type authStatusResponse struct {
	Initialized   bool   `json:"initialized"`
	Authenticated bool   `json:"authenticated"`
	Username      string `json:"username,omitempty"`
	ExpiresAt     int64  `json:"expires_at,omitempty"`
}

type authErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeAuthJSON(w http.ResponseWriter, r *http.Request, target any) (int, error) {
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	if contentType != "application/json" {
		return http.StatusUnsupportedMediaType, errors.New("Content-Type must be application/json")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return http.StatusRequestEntityTooLarge, errors.New("authentication request body is too large")
		}
		return http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return http.StatusBadRequest, errors.New("JSON body must contain one object")
		}
		return http.StatusBadRequest, fmt.Errorf("invalid trailing JSON: %w", err)
	}
	return 0, nil
}

func setAuthCookies(w http.ResponseWriter, r *http.Request, service *Service, result LoginResult) {
	secure := service.cookieSecure(r)
	maxAge := int(math.Ceil(result.ExpiresAt.Sub(service.now().UTC()).Seconds()))
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookieName, Value: result.SessionToken, Path: "/", HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteStrictMode, Expires: result.ExpiresAt, MaxAge: maxAge,
	})
	http.SetCookie(w, &http.Cookie{
		Name: CSRFCookieName, Value: result.CSRFToken, Path: "/", HttpOnly: false,
		Secure: secure, SameSite: http.SameSiteStrictMode, Expires: result.ExpiresAt, MaxAge: maxAge,
	})
}

func clearAuthCookies(w http.ResponseWriter, r *http.Request, service *Service) {
	secure := service != nil && service.cookieSecure(r)
	expires := time.Unix(1, 0).UTC()
	for _, cookie := range []http.Cookie{
		{Name: SessionCookieName, Path: "/", HttpOnly: true},
		{Name: CSRFCookieName, Path: "/", HttpOnly: false},
	} {
		cookie.Value = ""
		cookie.Secure = secure
		cookie.SameSite = http.SameSiteStrictMode
		cookie.Expires = expires
		cookie.MaxAge = -1
		http.SetCookie(w, &cookie)
	}
}

func authMethodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeAuthError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method is not allowed for this authentication resource.")
}

func writeAuthError(w http.ResponseWriter, status int, code, message string) {
	response := authErrorResponse{}
	response.Error.Code = code
	response.Error.Message = message
	writeAuthJSON(w, status, response)
}

func writeAuthJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

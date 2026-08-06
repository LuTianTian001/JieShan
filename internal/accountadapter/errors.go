package accountadapter

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

var (
	ErrUnsupported       = errors.New("account adapter: unsupported operation")
	ErrUnsupportedKind   = errors.New("account adapter: unsupported kind")
	ErrInvalidConnection = errors.New("account adapter: invalid connection")
)

// RemoteError is safe to log: all known credentials are removed at creation.
type RemoteError struct {
	Kind       Kind
	Operation  string
	StatusCode int
	Code       string
	Message    string
}

type redactedWrappedError struct {
	message string
	cause   error
}

func (e *redactedWrappedError) Error() string { return e.message }

func (e *redactedWrappedError) Unwrap() error { return e.cause }

func (e *RemoteError) Error() string {
	parts := []string{string(e.Kind), e.Operation}
	if e.StatusCode != 0 {
		parts = append(parts, fmt.Sprintf("HTTP %d", e.StatusCode))
	}
	if e.Code != "" {
		parts = append(parts, "code "+e.Code)
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	return strings.Join(parts, ": ")
}

func remoteError(kind Kind, operation string, status int, code, message string, credentials ...Credentials) error {
	return &RemoteError{
		Kind:       kind,
		Operation:  operation,
		StatusCode: status,
		Code:       sanitize(code, credentials...),
		Message:    sanitize(message, credentials...),
	}
}

func isRemoteStatus(err error, status int) bool {
	var remote *RemoteError
	return errors.As(err, &remote) && remote.StatusCode == status
}

func wrapRedactedError(prefix string, err error, credentials ...Credentials) error {
	if err == nil {
		return nil
	}
	return &redactedWrappedError{
		message: prefix + sanitize(err.Error(), credentials...),
		cause:   err,
	}
}

func sanitize(value string, credentials ...Credentials) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	for _, credential := range credentials {
		secrets := []string{
			credential.Authorization,
			credential.AccessToken,
			credential.RefreshToken,
		}
		for _, secret := range secrets {
			secret = strings.TrimSpace(secret)
			if secret == "" {
				continue
			}
			value = strings.ReplaceAll(value, secret, "[redacted]")
			value = strings.ReplaceAll(value, url.QueryEscape(secret), "[redacted]")
			fields := strings.Fields(secret)
			if len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
				value = strings.ReplaceAll(value, fields[1], "[redacted]")
			}
		}
	}
	value = strings.TrimSpace(value)
	if len(value) > 512 {
		value = value[:512]
	}
	return value
}

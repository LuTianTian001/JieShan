package adminauth

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

func normalizeAllowedOrigins(values []string) (map[string]struct{}, error) {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		origin, err := canonicalOrigin(value)
		if err != nil {
			return nil, err
		}
		result[origin] = struct{}{}
	}
	return result, nil
}

func (service *Service) VerifyOrigin(r *http.Request) error {
	if r == nil {
		return ErrOriginRejected
	}
	origin, err := canonicalOrigin(r.Header.Get("Origin"))
	if err != nil {
		return ErrOriginRejected
	}
	if len(service.allowedOrigins) > 0 {
		if _, allowed := service.allowedOrigins[origin]; !allowed {
			return ErrOriginRejected
		}
		return nil
	}
	expected, err := service.requestOrigin(r)
	if err != nil || origin != expected {
		return ErrOriginRejected
	}
	return nil
}

func (service *Service) requestOrigin(r *http.Request) (string, error) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if service.trustProxyHeaders {
		forwarded := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")))
		if forwarded == "http" || forwarded == "https" {
			scheme = forwarded
		} else if forwarded != "" {
			return "", errors.New("forwarded protocol is invalid")
		}
	}
	return canonicalOrigin(scheme + "://" + strings.TrimSpace(r.Host))
}

func canonicalOrigin(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "null" || len(value) > 2_048 {
		return "", errors.New("origin is missing or invalid")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("origin is invalid")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("origin scheme is invalid")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("origin must not contain a path")
	}
	hostname := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if hostname == "" {
		return "", errors.New("origin host is invalid")
	}
	port := parsed.Port()
	if port == "80" && scheme == "http" || port == "443" && scheme == "https" {
		port = ""
	}
	host := hostname
	if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	if port != "" {
		if _, err := net.LookupPort("tcp", port); err != nil {
			return "", errors.New("origin port is invalid")
		}
		host = net.JoinHostPort(hostname, port)
	}
	return scheme + "://" + host, nil
}

func (service *Service) cookieSecure(r *http.Request) bool {
	if service.secureCookies != nil {
		return *service.secureCookies
	}
	if r != nil && r.TLS != nil {
		return true
	}
	if r != nil && service.trustProxyHeaders && strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
		return true
	}
	if r != nil {
		origin, err := canonicalOrigin(r.Header.Get("Origin"))
		return err == nil && strings.HasPrefix(origin, "https://")
	}
	return false
}

func requiresCSRF(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

package upstream

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

type ClientOptions struct {
	AllowPrivateUpstreams bool
}

type outboundPolicy struct {
	allowPrivate bool
	resolver     *net.Resolver
	dialer       *net.Dialer
}

type guardedTransport struct {
	base   http.RoundTripper
	policy *outboundPolicy
}

var privateNetworkPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
}

var nonPublicNetworkPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
}

var metadataAddresses = map[netip.Addr]struct{}{
	netip.MustParseAddr("100.100.100.200"): {},
	netip.MustParseAddr("168.63.129.16"):   {},
	netip.MustParseAddr("169.254.169.254"): {},
	netip.MustParseAddr("169.254.170.2"):   {},
	netip.MustParseAddr("fd00:ec2::254"):   {},
}

func newOutboundPolicy(allowPrivate bool) *outboundPolicy {
	return &outboundPolicy{
		allowPrivate: allowPrivate,
		resolver:     net.DefaultResolver,
		dialer: &net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		},
	}
}

func (t *guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, errors.New("upstream request URL is missing")
	}
	if err := t.policy.validateURL(req.URL); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}

func (p *outboundPolicy) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 upstream redirects")
	}
	if req == nil || req.URL == nil {
		return errors.New("redirected upstream URL is missing")
	}
	if err := p.validateURL(req.URL); err != nil {
		return fmt.Errorf("refused upstream redirect: %w", err)
	}
	if len(via) > 0 && !sameOrigin(via[len(via)-1].URL, req.URL) {
		if requestHasBody(req) {
			return errors.New("refused cross-origin redirect with request body")
		}
		stripCrossOriginCredentials(req)
	}
	return nil
}

func requestHasBody(req *http.Request) bool {
	return req != nil && req.Body != nil && req.Body != http.NoBody
}

func (p *outboundPolicy) validateURL(target *url.URL) error {
	if err := validateURLSyntax(target); err != nil {
		return err
	}
	host := strings.TrimSuffix(strings.ToLower(target.Hostname()), ".")
	if isMetadataHostname(host) {
		return errors.New("cloud metadata endpoints are not allowed as upstreams")
	}
	if address, err := netip.ParseAddr(host); err == nil {
		return p.validateAddress(address)
	}
	return nil
}

func validateURLSyntax(target *url.URL) error {
	if target == nil || target.Opaque != "" || target.Host == "" || target.Hostname() == "" {
		return errors.New("invalid upstream URL")
	}
	switch strings.ToLower(target.Scheme) {
	case "http", "https":
	default:
		return errors.New("upstream URL must use http or https")
	}
	if target.User != nil {
		return errors.New("upstream URL must not contain user information")
	}
	return nil
}

func (p *outboundPolicy) validateAddress(address netip.Addr) error {
	address = address.Unmap()
	if _, found := metadataAddresses[address]; found {
		return errors.New("cloud metadata endpoints are not allowed as upstreams")
	}
	if address.IsUnspecified() || address.IsMulticast() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() {
		return fmt.Errorf("upstream address %s is not publicly routable", address)
	}
	if address.IsLoopback() || address.IsPrivate() || containsAddress(privateNetworkPrefixes, address) {
		if p.allowPrivate {
			return nil
		}
		return fmt.Errorf("private upstream address %s is blocked", address)
	}
	if containsAddress(nonPublicNetworkPrefixes, address) || !address.IsGlobalUnicast() {
		return fmt.Errorf("upstream address %s is not publicly routable", address)
	}
	return nil
}

func (p *outboundPolicy) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid upstream network address: %w", err)
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if isMetadataHostname(host) {
		return nil, errors.New("cloud metadata endpoints are not allowed as upstreams")
	}

	var resolved []netip.Addr
	if literal, parseErr := netip.ParseAddr(host); parseErr == nil {
		resolved = []netip.Addr{literal}
	} else {
		resolved, err = p.resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve upstream host: %w", err)
		}
	}

	var lastErr error
	for _, candidate := range resolved {
		candidate = candidate.Unmap()
		if network == "tcp4" && !candidate.Is4() {
			continue
		}
		if network == "tcp6" && !candidate.Is6() {
			continue
		}
		if err := p.validateAddress(candidate); err != nil {
			lastErr = err
			continue
		}
		conn, dialErr := p.dialer.DialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("upstream host did not resolve to a usable address")
}

func containsAddress(prefixes []netip.Prefix, address netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func isMetadataHostname(host string) bool {
	switch strings.TrimSuffix(strings.ToLower(host), ".") {
	case "metadata.google.internal", "metadata.goog", "metadata.aws.internal", "instance-data", "instance-data.ec2.internal":
		return true
	default:
		return false
	}
}

func sameOrigin(left, right *url.URL) bool {
	if left == nil || right == nil || !strings.EqualFold(left.Scheme, right.Scheme) {
		return false
	}
	if canonicalHostname(left) != canonicalHostname(right) {
		return false
	}
	return canonicalPort(left) == canonicalPort(right)
}

func canonicalHostname(target *url.URL) string {
	host := strings.TrimSuffix(strings.ToLower(target.Hostname()), ".")
	if address, err := netip.ParseAddr(host); err == nil {
		return address.Unmap().String()
	}
	return host
}

func canonicalPort(target *url.URL) string {
	if port := target.Port(); port != "" {
		return port
	}
	switch strings.ToLower(target.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

func stripCrossOriginCredentials(req *http.Request) {
	for key := range req.Header {
		if isCredentialHeader(key) || strings.EqualFold(key, "Referer") {
			req.Header.Del(key)
		}
	}
	if req.URL == nil || req.URL.RawQuery == "" {
		return
	}
	query := req.URL.Query()
	for key := range query {
		if isCredentialQueryKey(key) {
			query.Del(key)
		}
	}
	req.URL.RawQuery = query.Encode()
}

func isCredentialHeader(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	switch normalized {
	case "authorization", "proxy-authorization", "cookie", "cookie2", "api-key", "x-api-key", "x-goog-api-key", "x-auth-token", "x-access-token":
		return true
	}
	compact := strings.NewReplacer("-", "", "_", "").Replace(normalized)
	return strings.Contains(compact, "apikey") || strings.HasSuffix(compact, "accesstoken") || strings.HasSuffix(compact, "authtoken")
}

func isCredentialQueryKey(key string) bool {
	compact := strings.NewReplacer("-", "", "_", "", ".", "").Replace(strings.ToLower(strings.TrimSpace(key)))
	switch compact {
	case "key", "apikey", "accesstoken", "token", "auth", "authorization", "signature", "xamzcredential", "xamzsignature":
		return true
	default:
		return false
	}
}

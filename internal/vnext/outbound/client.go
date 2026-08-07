package outbound

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

type Options struct {
	AllowPrivate          bool
	DialTimeout           time.Duration
	ResponseHeaderTimeout time.Duration
	MaxConnsPerHost       int
}

type Client struct {
	http *http.Client
}

func New(options Options) *Client {
	if options.DialTimeout <= 0 {
		options.DialTimeout = 10 * time.Second
	}
	if options.ResponseHeaderTimeout <= 0 {
		options.ResponseHeaderTimeout = 60 * time.Second
	}
	if options.MaxConnsPerHost <= 0 {
		options.MaxConnsPerHost = 12
	}
	policy := &policy{
		allowPrivate: options.AllowPrivate,
		resolver:     net.DefaultResolver,
		dialer: &net.Dialer{
			Timeout:   options.DialTimeout,
			KeepAlive: 30 * time.Second,
		},
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = policy.dialContext
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = options.ResponseHeaderTimeout
	transport.MaxIdleConnsPerHost = min(options.MaxConnsPerHost, 6)
	transport.MaxIdleConns = min(max(16, transport.MaxIdleConnsPerHost*4), 32)
	transport.MaxConnsPerHost = options.MaxConnsPerHost
	transport.IdleConnTimeout = 90 * time.Second
	transport.MaxResponseHeaderBytes = 1 << 20
	transport.ReadBufferSize = 16 << 10
	transport.WriteBufferSize = 16 << 10
	transport.ForceAttemptHTTP2 = true
	return &Client{http: &http.Client{
		Transport:     guardedTransport{base: transport, policy: policy},
		CheckRedirect: policy.checkRedirect,
	}}
}

func (client *Client) Do(request *http.Request) (*http.Response, error) {
	if client == nil || client.http == nil {
		return nil, errors.New("outbound client is unavailable")
	}
	response, err := client.http.Do(request)
	if err == nil {
		return response, nil
	}
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	return nil, errors.New("outbound request failed")
}

func (client *Client) CloseIdleConnections() {
	if client != nil && client.http != nil {
		client.http.CloseIdleConnections()
	}
}

type resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type dialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type policy struct {
	allowPrivate bool
	resolver     resolver
	dialer       dialer
}

type guardedTransport struct {
	base   http.RoundTripper
	policy *policy
}

func (transport guardedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil {
		return nil, errors.New("outbound request URL is missing")
	}
	if transport.policy == nil || transport.base == nil {
		return nil, errors.New("outbound transport is unavailable")
	}
	if err := transport.policy.validateURL(request.URL); err != nil {
		return nil, err
	}
	return transport.base.RoundTrip(request)
}

func (policy *policy) checkRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("outbound redirect limit exceeded")
	}
	if request == nil || request.URL == nil {
		return errors.New("outbound redirect URL is missing")
	}
	if err := policy.validateURL(request.URL); err != nil {
		return errors.New("outbound redirect was refused")
	}
	if len(via) == 0 || sameOrigin(via[len(via)-1].URL, request.URL) {
		return nil
	}
	if request.Body != nil && request.Body != http.NoBody {
		return errors.New("cross-origin redirect with a request body was refused")
	}
	stripCredentials(request)
	return nil
}

func (policy *policy) validateURL(target *url.URL) error {
	if target == nil || target.Opaque != "" || target.Host == "" || target.Hostname() == "" || target.User != nil {
		return errors.New("outbound URL is invalid")
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return errors.New("outbound URL must use HTTP or HTTPS")
	}
	host := canonicalHost(target.Hostname())
	if metadataHostname(host) {
		return errors.New("cloud metadata endpoints are blocked")
	}
	if address, err := netip.ParseAddr(host); err == nil {
		return policy.validateAddress(address)
	}
	return nil
}

func (policy *policy) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("outbound network address is invalid")
	}
	host = canonicalHost(host)
	if metadataHostname(host) {
		return nil, errors.New("cloud metadata endpoints are blocked")
	}
	resolved := make([]netip.Addr, 0, 2)
	if literal, err := netip.ParseAddr(host); err == nil {
		resolved = append(resolved, literal)
	} else {
		if policy.resolver == nil {
			return nil, errors.New("outbound DNS resolver is unavailable")
		}
		resolved, err = policy.resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, errors.New("outbound host resolution failed")
		}
	}
	if policy.dialer == nil {
		return nil, errors.New("outbound network dialer is unavailable")
	}
	var lastError error
	for _, candidate := range resolved {
		candidate = candidate.Unmap()
		if network == "tcp4" && !candidate.Is4() || network == "tcp6" && !candidate.Is6() {
			continue
		}
		if err := policy.validateAddress(candidate); err != nil {
			lastError = err
			continue
		}
		connection, err := policy.dialer.DialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
		if err == nil {
			return connection, nil
		}
		lastError = errors.New("outbound connection failed")
	}
	if lastError != nil {
		return nil, lastError
	}
	return nil, errors.New("outbound host has no usable address")
}

func (policy *policy) validateAddress(address netip.Addr) error {
	address = address.Unmap()
	if _, blocked := metadataAddresses[address]; blocked {
		return errors.New("cloud metadata endpoints are blocked")
	}
	if address.IsUnspecified() || address.IsMulticast() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() {
		return errors.New("outbound address is not routable")
	}
	if address.IsLoopback() || address.IsPrivate() || inPrefixes(privatePrefixes, address) {
		if policy.allowPrivate {
			return nil
		}
		return errors.New("private outbound address is blocked")
	}
	if inPrefixes(nonPublicPrefixes, address) || !address.IsGlobalUnicast() {
		return errors.New("outbound address is not publicly routable")
	}
	return nil
}

var privatePrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
}

var nonPublicPrefixes = []netip.Prefix{
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

func inPrefixes(prefixes []netip.Prefix, address netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func metadataHostname(host string) bool {
	switch canonicalHost(host) {
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
	return canonicalHost(left.Hostname()) == canonicalHost(right.Hostname()) && canonicalPort(left) == canonicalPort(right)
}

func canonicalHost(host string) string {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if address, err := netip.ParseAddr(host); err == nil {
		return address.Unmap().String()
	}
	return host
}

func canonicalPort(target *url.URL) string {
	if target == nil {
		return ""
	}
	if port := target.Port(); port != "" {
		return port
	}
	if strings.EqualFold(target.Scheme, "http") {
		return "80"
	}
	if strings.EqualFold(target.Scheme, "https") {
		return "443"
	}
	return ""
}

func stripCredentials(request *http.Request) {
	for key := range request.Header {
		if credentialHeader(key) || strings.EqualFold(key, "Referer") {
			request.Header.Del(key)
		}
	}
	if request.URL == nil || request.URL.RawQuery == "" {
		return
	}
	query := request.URL.Query()
	for key := range query {
		if credentialQueryKey(key) {
			query.Del(key)
		}
	}
	request.URL.RawQuery = query.Encode()
}

func credentialHeader(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	switch normalized {
	case "authorization", "proxy-authorization", "cookie", "cookie2", "api-key", "x-api-key", "x-goog-api-key", "x-auth-token", "x-access-token", "cf-access-client-secret":
		return true
	}
	compact := strings.NewReplacer("-", "", "_", "").Replace(normalized)
	return strings.Contains(compact, "apikey") || strings.HasSuffix(compact, "accesstoken") || strings.HasSuffix(compact, "authtoken") || strings.HasSuffix(compact, "clientsecret")
}

func credentialQueryKey(key string) bool {
	compact := strings.NewReplacer("-", "", "_", "", ".", "").Replace(strings.ToLower(strings.TrimSpace(key)))
	switch compact {
	case "key", "apikey", "accesstoken", "refreshtoken", "token", "auth", "authorization", "signature", "xamzcredential", "xamzsignature":
		return true
	default:
		return false
	}
}

func ValidateURL(raw string, allowPrivate bool) error {
	target, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("parse outbound URL: %w", err)
	}
	return (&policy{allowPrivate: allowPrivate}).validateURL(target)
}

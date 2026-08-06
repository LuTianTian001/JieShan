package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/LuTianTian001/JieShan/internal/redact"
	"github.com/LuTianTian001/JieShan/internal/secrets"
	"github.com/LuTianTian001/JieShan/internal/store"
)

type Client struct {
	store  *store.Store
	cipher *secrets.Cipher
	http   *http.Client
	policy *outboundPolicy
}

type DiscoveryResult struct {
	Models   []string               `json:"models"`
	Complete bool                   `json:"complete"`
	Pages    int                    `json:"pagesFetched"`
	Diff     store.ModelApplyResult `json:"diff"`
}

func NewClient(s *store.Store, cipher *secrets.Cipher, timeout time.Duration, configured ...ClientOptions) *Client {
	options := ClientOptions{}
	if len(configured) > 0 {
		options = configured[0]
	}
	httpClient, policy := newHTTPClient(timeout, options)
	return &Client{
		store:  s,
		cipher: cipher,
		policy: policy,
		http:   httpClient,
	}
}

// NewHTTPClient creates an outbound client with the same SSRF and DNS
// rebinding protections used by inference requests.
func NewHTTPClient(timeout time.Duration, options ClientOptions) *http.Client {
	client, _ := newHTTPClient(timeout, options)
	client.Timeout = timeout
	return client
}

func newHTTPClient(timeout time.Duration, options ClientOptions) (*http.Client, *outboundPolicy) {
	policy := newOutboundPolicy(options.AllowPrivateUpstreams)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = policy.dialContext
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = timeout
	transport.MaxIdleConns = 32
	transport.MaxIdleConnsPerHost = 8
	transport.IdleConnTimeout = 90 * time.Second
	return &http.Client{
		Transport:     &guardedTransport{base: transport, policy: policy},
		CheckRedirect: policy.checkRedirect,
	}, policy
}

func (c *Client) DiscoverAndApply(ctx context.Context, upstreamID int64) (DiscoveryResult, error) {
	discovery, err := c.DiscoverModels(ctx, upstreamID)
	if err != nil {
		return DiscoveryResult{Models: discovery.Models, Complete: discovery.Complete, Pages: discovery.PagesFetched}, err
	}
	diff, err := c.store.ApplyDiscoveredModels(ctx, upstreamID, discovery.Models)
	if err != nil {
		return DiscoveryResult{Models: discovery.Models, Complete: discovery.Complete, Pages: discovery.PagesFetched}, err
	}
	return DiscoveryResult{Models: discovery.Models, Complete: discovery.Complete, Pages: discovery.PagesFetched, Diff: diff}, nil
}

// Discover preserves the original model-name-only contract for existing callers.
// New code should use DiscoverModels so incomplete pagination is explicit.
func (c *Client) Discover(ctx context.Context, upstreamID int64) ([]string, error) {
	result, err := c.DiscoverModels(ctx, upstreamID)
	return result.Models, err
}

func (c *Client) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.http.Do(req)
	if err == nil {
		return resp, nil
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	return nil, safeError(err)
}

func applyAuth(req *http.Request, kind, secret string) {
	switch kind {
	case "anthropic":
		req.Header.Set("x-api-key", secret)
		req.Header.Set("anthropic-version", "2023-06-01")
	case "gemini":
		// Gemini discovery uses the query parameter. Native generation support can
		// use the same helper without leaking the key into logs.
	default:
		req.Header.Set("Authorization", "Bearer "+secret)
	}
}

func applyCustomHeaders(headers http.Header, raw []byte) {
	var values map[string]string
	if len(raw) == 0 || json.Unmarshal(raw, &values) != nil {
		return
	}
	for key, value := range values {
		if isSensitiveHopHeader(key) {
			continue
		}
		headers.Set(key, value)
	}
}

func isSensitiveHopHeader(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "authorization", "proxy-authorization", "connection", "transfer-encoding", "content-length", "host", "cookie", "set-cookie":
		return true
	default:
		return false
	}
}

func compact(body []byte, max int) string {
	value := strings.TrimSpace(string(body))
	if len(value) > max {
		return value[:max] + "..."
	}
	return value
}

func safeError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(redact.String(err.Error()))
}

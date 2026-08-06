package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
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
	models, err := c.Discover(ctx, upstreamID)
	if err != nil {
		return DiscoveryResult{}, err
	}
	diff, err := c.store.ApplyDiscoveredModels(ctx, upstreamID, models)
	if err != nil {
		return DiscoveryResult{}, err
	}
	return DiscoveryResult{Models: models, Complete: true, Diff: diff}, nil
}

func (c *Client) Discover(ctx context.Context, upstreamID int64) ([]string, error) {
	item, err := c.store.GetUpstreamSecret(ctx, upstreamID)
	if err != nil {
		return nil, err
	}
	secret, err := c.cipher.Decrypt(item.SecretCipher)
	if err != nil {
		return nil, err
	}
	requestURL, err := modelsURL(item.Kind, item.BaseURL, secret)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	applyAuth(req, item.Kind, secret)
	applyCustomHeaders(req.Header, item.CustomHeaders)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, safeError(fmt.Errorf("model discovery returned %d: %s", resp.StatusCode, compact(body, 500)))
	}
	models, err := parseModels(item.Kind, body)
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("model discovery returned an empty complete list")
	}
	return models, nil
}

func (c *Client) BuildChatRequest(ctx context.Context, target store.RouteTarget, body []byte) (*http.Request, error) {
	if target.UpstreamKind != "openai" && target.UpstreamKind != "compatible" {
		return nil, fmt.Errorf("protocol %s is not yet supported by the OpenAI chat surface", target.UpstreamKind)
	}
	secret, err := c.cipher.Decrypt(target.SecretCipher)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid JSON request: %w", err)
	}
	payload["model"] = target.UpstreamModel
	upstreamBody, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	requestURL, err := chatURL(target.BaseURL)
	if err != nil {
		return nil, safeError(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(upstreamBody))
	if err != nil {
		return nil, safeError(err)
	}
	req.Header.Set("Content-Type", "application/json")
	applyAuth(req, target.UpstreamKind, secret)
	applyCustomHeaders(req.Header, target.CustomHeaders)
	return req, nil
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

func modelsURL(kind, baseURL, secret string) (string, error) {
	base, err := parseBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	switch kind {
	case "gemini":
		base.Path = strings.TrimRight(base.Path, "/")
		if !strings.HasSuffix(base.Path, "/v1beta") {
			base.Path += "/v1beta"
		}
		base.Path += "/models"
		query := base.Query()
		query.Set("key", secret)
		base.RawQuery = query.Encode()
	case "anthropic", "openai", "compatible":
		base.Path = strings.TrimRight(base.Path, "/")
		if !strings.HasSuffix(base.Path, "/v1") {
			base.Path += "/v1"
		}
		base.Path += "/models"
	default:
		return "", fmt.Errorf("unsupported upstream protocol %q", kind)
	}
	return base.String(), nil
}

func chatURL(baseURL string) (string, error) {
	base, err := parseBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	base.Path = strings.TrimRight(base.Path, "/")
	if !strings.HasSuffix(base.Path, "/v1") {
		base.Path += "/v1"
	}
	base.Path += "/chat/completions"
	base.RawPath = ""
	return base.String(), nil
}

func parseBaseURL(raw string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || validateURLSyntax(base) != nil {
		return nil, errors.New("invalid upstream base URL; only http and https URLs without user information are allowed")
	}
	return base, nil
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

func parseModels(kind string, body []byte) ([]string, error) {
	seen := map[string]struct{}{}
	if kind == "gemini" {
		var payload struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("decode Gemini model list: %w", err)
		}
		for _, model := range payload.Models {
			name := strings.TrimPrefix(strings.TrimSpace(model.Name), "models/")
			if name != "" {
				seen[name] = struct{}{}
			}
		}
	} else {
		var payload struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("decode model list: %w", err)
		}
		for _, model := range payload.Data {
			if name := strings.TrimSpace(model.ID); name != "" {
				seen[name] = struct{}{}
			}
		}
	}
	models := make([]string, 0, len(seen))
	for name := range seen {
		models = append(models, name)
	}
	sort.Strings(models)
	return models, nil
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

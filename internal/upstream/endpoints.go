package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/inferenceprotocol"
	"github.com/LuTianTian001/JieShan/internal/store"
)

// EndpointCapability identifies an upstream HTTP surface independently from
// the protocol label shown in the UI.
type EndpointCapability string

const (
	EndpointModels          EndpointCapability = "models"
	EndpointChatCompletions EndpointCapability = "chat_completions"
	EndpointResponses       EndpointCapability = "responses"
)

// SupportsEndpoint reports the surfaces JieShan can safely construct for a
// protocol. It does not claim that every compatible relay implements them.
func SupportsEndpoint(protocol string, endpoint EndpointCapability) bool {
	capabilities := inferenceprotocol.For(protocol)
	switch endpoint {
	case EndpointModels:
		return capabilities.ModelDiscovery
	case EndpointChatCompletions:
		return capabilities.ChatCompletions
	case EndpointResponses:
		return capabilities.Responses
	default:
		return false
	}
}

func (c *Client) BuildChatRequest(ctx context.Context, target store.RouteTarget, body []byte) (*http.Request, error) {
	return c.buildOpenAIRequest(ctx, target, body, EndpointChatCompletions)
}

// BuildResponsesRequest prepares an OpenAI Responses request. The caller owns
// response streaming and passthrough; this method only selects the endpoint,
// replaces the routed model, and applies the upstream credential.
func (c *Client) BuildResponsesRequest(ctx context.Context, target store.RouteTarget, body []byte) (*http.Request, error) {
	return c.buildOpenAIRequest(ctx, target, body, EndpointResponses)
}

func (c *Client) BuildResolvedChatRequest(ctx context.Context, target store.ResolvedRouteSiteTarget, credential store.InferenceCredentialSecret, body []byte) (*http.Request, error) {
	return c.buildOpenAIRequest(ctx, resolvedLegacyTarget(target, credential), body, EndpointChatCompletions)
}

func (c *Client) BuildResolvedResponsesRequest(ctx context.Context, target store.ResolvedRouteSiteTarget, credential store.InferenceCredentialSecret, body []byte) (*http.Request, error) {
	return c.buildOpenAIRequest(ctx, resolvedLegacyTarget(target, credential), body, EndpointResponses)
}

func resolvedLegacyTarget(target store.ResolvedRouteSiteTarget, credential store.InferenceCredentialSecret) store.RouteTarget {
	protocol, err := normalizeInferenceProtocol(target.WireProtocol)
	if err != nil {
		protocol = target.WireProtocol
	}
	return store.RouteTarget{
		ID: target.ID, UpstreamID: target.SiteID, UpstreamName: target.SiteName,
		UpstreamKind: protocol, UpstreamModel: target.SourceModel, BaseURL: target.BaseURL,
		EndpointID: target.EndpointID, CredentialID: credential.ID, SecretCipher: credential.SecretCipher,
		CustomHeaders: target.CustomHeaders, CredentialState: credential.RuntimeState, CredentialName: credential.Name,
	}
}

func (c *Client) buildOpenAIRequest(ctx context.Context, target store.RouteTarget, body []byte, endpoint EndpointCapability) (*http.Request, error) {
	if !SupportsEndpoint(target.UpstreamKind, endpoint) {
		return nil, fmt.Errorf("protocol %s is not supported by the OpenAI %s surface", target.UpstreamKind, endpoint)
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
	requestURL, err := openAIEndpointURL(target.BaseURL, endpoint)
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

func modelsURL(kind, baseURL, secret string) (string, error) {
	if !SupportsEndpoint(kind, EndpointModels) {
		return "", fmt.Errorf("unsupported upstream protocol %q", kind)
	}
	base, err := parseBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	base.Path = strings.TrimRight(base.Path, "/")
	if kind == "gemini" {
		if !strings.HasSuffix(base.Path, "/v1beta") {
			base.Path += "/v1beta"
		}
		base.Path += "/models"
		query := base.Query()
		query.Set("key", secret)
		base.RawQuery = query.Encode()
	} else {
		base.Path = appendV1Path(base.Path, "/models")
	}
	base.RawPath = ""
	return base.String(), nil
}

func chatURL(baseURL string) (string, error) {
	return openAIEndpointURL(baseURL, EndpointChatCompletions)
}

func responsesURL(baseURL string) (string, error) {
	return openAIEndpointURL(baseURL, EndpointResponses)
}

func openAIEndpointURL(baseURL string, endpoint EndpointCapability) (string, error) {
	var suffix string
	switch endpoint {
	case EndpointChatCompletions:
		suffix = "/chat/completions"
	case EndpointResponses:
		suffix = "/responses"
	default:
		return "", fmt.Errorf("unsupported OpenAI endpoint %q", endpoint)
	}
	base, err := parseBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	base.Path = appendV1Path(strings.TrimRight(base.Path, "/"), suffix)
	base.RawPath = ""
	return base.String(), nil
}

func appendV1Path(basePath, suffix string) string {
	basePath = strings.TrimRight(basePath, "/")
	if strings.HasSuffix(basePath, suffix) {
		return basePath
	}
	if !strings.HasSuffix(basePath, "/v1") {
		basePath += "/v1"
	}
	return basePath + suffix
}

func parseBaseURL(raw string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || validateURLSyntax(base) != nil {
		return nil, errors.New("invalid upstream base URL; only http and https URLs without user information are allowed")
	}
	return base, nil
}

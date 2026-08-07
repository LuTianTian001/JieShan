package openai

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

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

const (
	maxBodyBytes  = 8 << 20
	maxEventBytes = 256 << 10
)

var errBodyTooLarge = errors.New("OpenAI response body exceeds the safety limit")

// HTTPDoer lets the runtime provide its SSRF-safe, timeout-bound HTTP client.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// ChatCompletionsAdapter implements the complete native OpenAI Chat
// Completions surface. It deliberately does not infer auth or transport policy.
type ChatCompletionsAdapter struct {
	client HTTPDoer
}

func NewChatCompletionsAdapter(client HTTPDoer) (*ChatCompletionsAdapter, error) {
	if client == nil {
		return nil, errors.New("OpenAI Chat Completions adapter requires a secured HTTP client")
	}
	return &ChatCompletionsAdapter{client: client}, nil
}

var (
	_ vnextprotocol.Discoverer      = (*ChatCompletionsAdapter)(nil)
	_ vnextprotocol.RequestEncoder  = (*ChatCompletionsAdapter)(nil)
	_ vnextprotocol.ResponseDecoder = (*ChatCompletionsAdapter)(nil)
	_ vnextprotocol.StreamDecoder   = (*ChatCompletionsAdapter)(nil)
	_ vnextprotocol.UsageExtractor  = (*ChatCompletionsAdapter)(nil)
	_ vnextprotocol.ErrorDecoder    = (*ChatCompletionsAdapter)(nil)
)

func (adapter *ChatCompletionsAdapter) DiscoverModels(ctx context.Context, input vnextprotocol.DiscoveryInput) (vnextprotocol.DiscoveryResult, error) {
	if adapter == nil || adapter.client == nil {
		return vnextprotocol.DiscoveryResult{Models: make([]string, 0)}, errors.New("OpenAI Chat Completions adapter is unavailable")
	}
	return discoverModels(ctx, adapter.client, input)
}

func discoverModels(ctx context.Context, client HTTPDoer, input vnextprotocol.DiscoveryInput) (vnextprotocol.DiscoveryResult, error) {
	result := vnextprotocol.DiscoveryResult{Models: make([]string, 0)}
	requestURL, err := endpointURL(input.BaseURL, "/models")
	if err != nil {
		return result, err
	}
	material, err := input.Auth.Material()
	if err != nil {
		return result, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return result, errors.New("could not construct the OpenAI model discovery request")
	}
	applyAuthMaterial(request, material)
	request.Header.Set("Accept", "application/json")

	response, err := client.Do(request)
	if err != nil {
		return result, errors.New("OpenAI model discovery request failed")
	}
	if response == nil || response.Body == nil {
		return result, errors.New("OpenAI model discovery returned no response body")
	}
	defer response.Body.Close()
	body, err := readBounded(response.Body, maxBodyBytes)
	if err != nil {
		return result, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return result, openAIStatusError(response.StatusCode, response.Header, body)
	}

	var envelope struct {
		Data *[]struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := decodeSingleJSON(body, &envelope); err != nil || envelope.Data == nil {
		return result, errors.New("OpenAI model discovery returned a malformed model list")
	}
	seen := make(map[string]struct{}, len(*envelope.Data))
	for _, item := range *envelope.Data {
		model := strings.TrimSpace(item.ID)
		if model == "" {
			return result, errors.New("OpenAI model discovery returned a model without an id")
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		result.Models = append(result.Models, model)
	}
	sort.Strings(result.Models)
	result.Complete = true
	return result, nil
}

func (adapter *ChatCompletionsAdapter) EncodeRequest(_ context.Context, input vnextprotocol.RequestBuildInput) (vnextprotocol.EncodedRequest, error) {
	if input.Protocol != vnextprotocol.OpenAI || input.Surface != vnextprotocol.OpenAIChatCompletions {
		return vnextprotocol.EncodedRequest{}, fmt.Errorf("OpenAI Chat Completions adapter cannot encode %q/%q", input.Protocol, input.Surface)
	}
	model := strings.TrimSpace(input.Model)
	if model == "" {
		return vnextprotocol.EncodedRequest{}, errors.New("OpenAI source model is required")
	}
	if len(input.Payload) == 0 {
		return vnextprotocol.EncodedRequest{}, errors.New("OpenAI request payload is required")
	}
	if len(input.Payload) > maxBodyBytes {
		return vnextprotocol.EncodedRequest{}, errors.New("OpenAI request payload exceeds the safety limit")
	}
	requestURL, err := endpointURL(input.BaseURL, "/chat/completions")
	if err != nil {
		return vnextprotocol.EncodedRequest{}, err
	}
	material, err := input.Auth.Material()
	if err != nil {
		return vnextprotocol.EncodedRequest{}, err
	}
	body, stream, err := rewriteRequestPayload(input.Payload, model)
	if err != nil {
		return vnextprotocol.EncodedRequest{}, err
	}
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	if stream {
		header.Set("Accept", "text/event-stream")
	} else {
		header.Set("Accept", "application/json")
	}
	applyAuthToParts(requestURL, header, material)
	return vnextprotocol.EncodedRequest{
		Method: http.MethodPost,
		URL:    requestURL.String(),
		Header: header,
		Body:   body,
	}, nil
}

func rewriteRequestPayload(raw []byte, model string) ([]byte, bool, error) {
	var payload map[string]json.RawMessage
	if err := decodeSingleJSON(raw, &payload); err != nil || payload == nil {
		return nil, false, errors.New("OpenAI request payload must be one JSON object")
	}
	encodedModel, _ := json.Marshal(model)
	payload["model"] = encodedModel

	stream := false
	if rawStream, exists := payload["stream"]; exists {
		if err := json.Unmarshal(rawStream, &stream); err != nil {
			return nil, false, errors.New("OpenAI request stream field must be boolean")
		}
	}
	if stream {
		options := make(map[string]json.RawMessage)
		if rawOptions, exists := payload["stream_options"]; exists && !isJSONNull(rawOptions) {
			if err := json.Unmarshal(rawOptions, &options); err != nil || options == nil {
				return nil, false, errors.New("OpenAI request stream_options field must be an object")
			}
		}
		options["include_usage"] = json.RawMessage("true")
		encodedOptions, err := json.Marshal(options)
		if err != nil {
			return nil, false, errors.New("could not encode OpenAI stream options")
		}
		payload["stream_options"] = encodedOptions
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, false, errors.New("could not encode the OpenAI request payload")
	}
	if len(body) > maxBodyBytes {
		return nil, false, errors.New("OpenAI request payload exceeds the safety limit")
	}
	return body, stream, nil
}

func endpointURL(rawBaseURL, suffix string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimSpace(rawBaseURL))
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.Fragment != "" {
		return nil, errors.New("invalid OpenAI base URL; use an http or https URL without user information or fragments")
	}
	base.Path = appendV1Surface(base.Path, suffix)
	base.RawPath = ""
	return base, nil
}

func appendV1Surface(basePath, suffix string) string {
	basePath = strings.TrimRight(basePath, "/")
	if strings.HasSuffix(basePath, suffix) {
		return basePath
	}
	for _, otherSurface := range []string{"/models", "/chat/completions", "/responses"} {
		if strings.HasSuffix(basePath, otherSurface) {
			basePath = strings.TrimSuffix(basePath, otherSurface)
			break
		}
	}
	if !strings.HasSuffix(basePath, "/v1") {
		basePath += "/v1"
	}
	return basePath + suffix
}

func applyAuthMaterial(request *http.Request, material vnextprotocol.AuthMaterial) {
	applyAuthToParts(request.URL, request.Header, material)
}

func applyAuthToParts(requestURL *url.URL, header http.Header, material vnextprotocol.AuthMaterial) {
	if material.HeaderName != "" {
		header.Set(material.HeaderName, material.HeaderValue)
	}
	if material.QueryParameter != "" {
		query := requestURL.Query()
		query.Set(material.QueryParameter, material.QueryValue)
		requestURL.RawQuery = query.Encode()
	}
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	if reader == nil {
		return nil, errors.New("OpenAI response body is unavailable")
	}
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, errors.New("could not read the OpenAI response body")
	}
	if int64(len(body)) > limit {
		return nil, errBodyTooLarge
	}
	return body, nil
}

func decodeSingleJSON(raw []byte, target any) error {
	trimmed := bytes.TrimPrefix(bytes.TrimSpace(raw), []byte{0xef, 0xbb, 0xbf})
	if len(trimmed) == 0 {
		return io.ErrUnexpectedEOF
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

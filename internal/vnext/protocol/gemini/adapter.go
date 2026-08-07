package gemini

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
	maxBodyBytes      = 8 << 20
	maxEventBytes     = 256 << 10
	maxDiscoveryPages = 20
)

var errBodyTooLarge = errors.New("Gemini response body exceeds the safety limit")

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// GenerateContentAdapter implements only the native Gemini GenerateContent
// surface. The caller remains responsible for providing a secured HTTP client.
type GenerateContentAdapter struct {
	client HTTPDoer
}

func NewGenerateContentAdapter(client HTTPDoer) (*GenerateContentAdapter, error) {
	if client == nil {
		return nil, errors.New("Gemini GenerateContent adapter requires a secured HTTP client")
	}
	return &GenerateContentAdapter{client: client}, nil
}

var (
	_ vnextprotocol.Discoverer      = (*GenerateContentAdapter)(nil)
	_ vnextprotocol.RequestEncoder  = (*GenerateContentAdapter)(nil)
	_ vnextprotocol.ResponseDecoder = (*GenerateContentAdapter)(nil)
	_ vnextprotocol.StreamDecoder   = (*GenerateContentAdapter)(nil)
	_ vnextprotocol.UsageExtractor  = (*GenerateContentAdapter)(nil)
	_ vnextprotocol.ErrorDecoder    = (*GenerateContentAdapter)(nil)
)

func (adapter *GenerateContentAdapter) DiscoverModels(ctx context.Context, input vnextprotocol.DiscoveryInput) (vnextprotocol.DiscoveryResult, error) {
	result := vnextprotocol.DiscoveryResult{Models: make([]string, 0)}
	if adapter == nil || adapter.client == nil {
		return result, errors.New("Gemini GenerateContent adapter is unavailable")
	}
	material, err := nativeAuthMaterial(input.Auth)
	if err != nil {
		return result, err
	}
	base, err := endpointURL(input.BaseURL, "", false)
	if err != nil {
		return result, err
	}
	seenModels := make(map[string]struct{})
	seenTokens := make(map[string]struct{})
	pageToken := ""
	for page := 0; page < maxDiscoveryPages; page++ {
		current := *base
		query := current.Query()
		query.Set("pageSize", "1000")
		if pageToken == "" {
			query.Del("pageToken")
		} else {
			query.Set("pageToken", pageToken)
		}
		current.RawQuery = query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, current.String(), nil)
		if err != nil {
			return result, errors.New("could not construct the Gemini model discovery request")
		}
		applyNativeHeaders(request.Header, material, false)
		response, err := adapter.client.Do(request)
		if err != nil {
			return result, errors.New("Gemini model discovery request failed")
		}
		if response == nil || response.Body == nil {
			return result, errors.New("Gemini model discovery returned no response body")
		}
		body, readErr := readBounded(response.Body, maxBodyBytes)
		_ = response.Body.Close()
		if readErr != nil {
			return result, readErr
		}
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return result, adapter.statusError(response.StatusCode, response.Header, body)
		}
		var envelope struct {
			Models *[]struct {
				Name                       string   `json:"name"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if decodeSingleJSON(body, &envelope) != nil || envelope.Models == nil {
			return result, errors.New("Gemini model discovery returned a malformed page")
		}
		for _, item := range *envelope.Models {
			if !containsMethod(item.SupportedGenerationMethods, "generateContent") {
				continue
			}
			name := strings.TrimSpace(item.Name)
			if _, err := modelID(name); err != nil {
				return result, errors.New("Gemini model discovery returned an invalid model name")
			}
			if _, exists := seenModels[name]; exists {
				continue
			}
			seenModels[name] = struct{}{}
			result.Models = append(result.Models, name)
		}
		pageToken = strings.TrimSpace(envelope.NextPageToken)
		if pageToken == "" {
			sort.Strings(result.Models)
			result.Complete = true
			return result, nil
		}
		if _, exists := seenTokens[pageToken]; exists {
			return result, errors.New("Gemini model discovery pagination repeated a page token")
		}
		seenTokens[pageToken] = struct{}{}
	}
	return result, fmt.Errorf("Gemini model discovery exceeded the %d page safety limit", maxDiscoveryPages)
}

func containsMethod(methods []string, target string) bool {
	for _, method := range methods {
		if strings.TrimSpace(method) == target {
			return true
		}
	}
	return false
}

func (adapter *GenerateContentAdapter) EncodeRequest(_ context.Context, input vnextprotocol.RequestBuildInput) (vnextprotocol.EncodedRequest, error) {
	if input.Protocol != vnextprotocol.Gemini || input.Surface != vnextprotocol.GeminiGenerateContent {
		return vnextprotocol.EncodedRequest{}, fmt.Errorf("Gemini GenerateContent adapter cannot encode %q/%q", input.Protocol, input.Surface)
	}
	if len(input.Payload) == 0 {
		return vnextprotocol.EncodedRequest{}, errors.New("Gemini request payload is required")
	}
	if len(input.Payload) > maxBodyBytes {
		return vnextprotocol.EncodedRequest{}, errors.New("Gemini request payload exceeds the safety limit")
	}
	id, err := modelID(input.Model)
	if err != nil {
		return vnextprotocol.EncodedRequest{}, err
	}
	material, err := nativeAuthMaterial(input.Auth)
	if err != nil {
		return vnextprotocol.EncodedRequest{}, err
	}
	body, stream, err := rewriteRequestPayload(input.Payload)
	if err != nil {
		return vnextprotocol.EncodedRequest{}, err
	}
	requestURL, err := endpointURL(input.BaseURL, id, stream)
	if err != nil {
		return vnextprotocol.EncodedRequest{}, err
	}
	header := make(http.Header)
	applyNativeHeaders(header, material, stream)
	return vnextprotocol.EncodedRequest{Method: http.MethodPost, URL: requestURL.String(), Header: header, Body: body}, nil
}

func rewriteRequestPayload(raw []byte) ([]byte, bool, error) {
	var payload map[string]json.RawMessage
	if decodeSingleJSON(raw, &payload) != nil || payload == nil {
		return nil, false, errors.New("Gemini request payload must be one JSON object")
	}
	var contents []json.RawMessage
	if json.Unmarshal(payload["contents"], &contents) != nil || len(contents) == 0 {
		return nil, false, errors.New("Gemini request payload must contain a non-empty contents array")
	}
	stream := false
	if rawStream, exists := payload["stream"]; exists {
		if json.Unmarshal(rawStream, &stream) != nil {
			return nil, false, errors.New("Gemini request stream field must be boolean")
		}
		delete(payload, "stream")
	}
	delete(payload, "model")
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, false, errors.New("could not encode the Gemini request payload")
	}
	if len(body) > maxBodyBytes {
		return nil, false, errors.New("Gemini request payload exceeds the safety limit")
	}
	return body, stream, nil
}

func nativeAuthMaterial(input vnextprotocol.AuthInput) (vnextprotocol.AuthMaterial, error) {
	if input.Scheme != vnextprotocol.AuthXGoogAPIKey {
		return vnextprotocol.AuthMaterial{}, errors.New("native Gemini API requires the x-goog-api-key auth scheme")
	}
	material, err := input.Material()
	if err != nil {
		return vnextprotocol.AuthMaterial{}, err
	}
	if !strings.EqualFold(material.HeaderName, "x-goog-api-key") || material.HeaderValue == "" || material.QueryParameter != "" {
		return vnextprotocol.AuthMaterial{}, errors.New("native Gemini API requires x-goog-api-key header auth material")
	}
	return material, nil
}

func applyNativeHeaders(header http.Header, material vnextprotocol.AuthMaterial, stream bool) {
	header.Set(material.HeaderName, material.HeaderValue)
	header.Set("Content-Type", "application/json")
	if stream {
		header.Set("Accept", "text/event-stream")
	} else {
		header.Set("Accept", "application/json")
	}
}

func endpointURL(rawBaseURL, model string, stream bool) (*url.URL, error) {
	base, err := url.Parse(strings.TrimSpace(rawBaseURL))
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.Fragment != "" {
		return nil, errors.New("invalid Gemini base URL; use an http or https URL without user information or fragments")
	}
	for key := range base.Query() {
		if strings.EqualFold(key, "key") {
			return nil, errors.New("Gemini base URL must not contain query-key authentication")
		}
	}
	root := geminiAPIRoot(base.Path)
	query := base.Query()
	query.Del("alt")
	base.RawQuery = query.Encode()
	if model == "" {
		base.Path = root + "/models"
	} else {
		action := "generateContent"
		if stream {
			action = "streamGenerateContent"
			query := base.Query()
			query.Set("alt", "sse")
			base.RawQuery = query.Encode()
		}
		base.Path = root + "/models/" + model + ":" + action
	}
	base.RawPath = ""
	return base, nil
}

func geminiAPIRoot(path string) string {
	path = strings.TrimRight(path, "/")
	if index := strings.LastIndex(path, "/models/"); index >= 0 {
		path = path[:index]
	} else if strings.HasSuffix(path, "/models") {
		path = strings.TrimSuffix(path, "/models")
	}
	if strings.HasSuffix(path, "/v1beta") || strings.HasSuffix(path, "/v1") {
		return path
	}
	return path + "/v1beta"
}

func modelID(value string) (string, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "models/")
	if value == "" {
		return "", errors.New("Gemini source model is required")
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.' {
			continue
		}
		return "", errors.New("Gemini source model must be one models/{id} path segment")
	}
	return value, nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	if reader == nil {
		return nil, errors.New("Gemini response body is unavailable")
	}
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, errors.New("could not read the Gemini response body")
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

func nonNullJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

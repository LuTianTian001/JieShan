package anthropic

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
	anthropicVersion  = "2023-06-01"
	maxBodyBytes      = 8 << 20
	maxEventBytes     = 256 << 10
	maxDiscoveryPages = 20
)

var errBodyTooLarge = errors.New("Anthropic response body exceeds the safety limit")

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// MessagesAdapter implements only the native Anthropic Messages surface.
type MessagesAdapter struct {
	client HTTPDoer
}

func NewMessagesAdapter(client HTTPDoer) (*MessagesAdapter, error) {
	if client == nil {
		return nil, errors.New("Anthropic Messages adapter requires a secured HTTP client")
	}
	return &MessagesAdapter{client: client}, nil
}

var (
	_ vnextprotocol.Discoverer      = (*MessagesAdapter)(nil)
	_ vnextprotocol.RequestEncoder  = (*MessagesAdapter)(nil)
	_ vnextprotocol.ResponseDecoder = (*MessagesAdapter)(nil)
	_ vnextprotocol.StreamDecoder   = (*MessagesAdapter)(nil)
	_ vnextprotocol.UsageExtractor  = (*MessagesAdapter)(nil)
	_ vnextprotocol.ErrorDecoder    = (*MessagesAdapter)(nil)
)

func (adapter *MessagesAdapter) DiscoverModels(ctx context.Context, input vnextprotocol.DiscoveryInput) (vnextprotocol.DiscoveryResult, error) {
	result := vnextprotocol.DiscoveryResult{Models: make([]string, 0)}
	if adapter == nil || adapter.client == nil {
		return result, errors.New("Anthropic Messages adapter is unavailable")
	}
	material, err := nativeAuthMaterial(input.Auth)
	if err != nil {
		return result, err
	}
	base, err := endpointURL(input.BaseURL, "/models")
	if err != nil {
		return result, err
	}
	seenModels := make(map[string]struct{})
	seenCursors := make(map[string]struct{})
	cursor := ""
	for pageNumber := 0; pageNumber < maxDiscoveryPages; pageNumber++ {
		current := *base
		query := current.Query()
		if cursor == "" {
			query.Del("after_id")
		} else {
			query.Set("after_id", cursor)
		}
		current.RawQuery = query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, current.String(), nil)
		if err != nil {
			return result, errors.New("could not construct the Anthropic model discovery request")
		}
		applyNativeHeaders(request.Header, material, false)
		response, err := adapter.client.Do(request)
		if err != nil {
			return result, errors.New("Anthropic model discovery request failed")
		}
		if response == nil || response.Body == nil {
			return result, errors.New("Anthropic model discovery returned no response body")
		}
		body, readErr := readBounded(response.Body, maxBodyBytes)
		_ = response.Body.Close()
		if readErr != nil {
			return result, readErr
		}
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return result, adapter.statusError(response.StatusCode, response.Header, body)
		}
		var page struct {
			Data *[]struct {
				ID string `json:"id"`
			} `json:"data"`
			HasMore *bool   `json:"has_more"`
			LastID  *string `json:"last_id"`
		}
		if err := decodeSingleJSON(body, &page); err != nil || page.Data == nil || page.HasMore == nil {
			return result, errors.New("Anthropic model discovery returned a malformed page")
		}
		for _, item := range *page.Data {
			model := strings.TrimSpace(item.ID)
			if model == "" {
				return result, errors.New("Anthropic model discovery returned a model without an id")
			}
			if _, exists := seenModels[model]; exists {
				continue
			}
			seenModels[model] = struct{}{}
			result.Models = append(result.Models, model)
		}
		if !*page.HasMore {
			sort.Strings(result.Models)
			result.Complete = true
			return result, nil
		}
		if page.LastID == nil || strings.TrimSpace(*page.LastID) == "" {
			return result, errors.New("Anthropic model discovery indicated another page without a last_id")
		}
		cursor = strings.TrimSpace(*page.LastID)
		if _, exists := seenCursors[cursor]; exists {
			return result, errors.New("Anthropic model discovery pagination repeated a cursor")
		}
		seenCursors[cursor] = struct{}{}
	}
	return result, fmt.Errorf("Anthropic model discovery exceeded the %d page safety limit", maxDiscoveryPages)
}

func (adapter *MessagesAdapter) EncodeRequest(_ context.Context, input vnextprotocol.RequestBuildInput) (vnextprotocol.EncodedRequest, error) {
	if input.Protocol != vnextprotocol.Anthropic || input.Surface != vnextprotocol.AnthropicMessages {
		return vnextprotocol.EncodedRequest{}, fmt.Errorf("Anthropic Messages adapter cannot encode %q/%q", input.Protocol, input.Surface)
	}
	model := strings.TrimSpace(input.Model)
	if model == "" {
		return vnextprotocol.EncodedRequest{}, errors.New("Anthropic source model is required")
	}
	if len(input.Payload) == 0 {
		return vnextprotocol.EncodedRequest{}, errors.New("Anthropic request payload is required")
	}
	if len(input.Payload) > maxBodyBytes {
		return vnextprotocol.EncodedRequest{}, errors.New("Anthropic request payload exceeds the safety limit")
	}
	material, err := nativeAuthMaterial(input.Auth)
	if err != nil {
		return vnextprotocol.EncodedRequest{}, err
	}
	requestURL, err := endpointURL(input.BaseURL, "/messages")
	if err != nil {
		return vnextprotocol.EncodedRequest{}, err
	}
	body, stream, err := rewriteRequestPayload(input.Payload, model)
	if err != nil {
		return vnextprotocol.EncodedRequest{}, err
	}
	header := make(http.Header)
	applyNativeHeaders(header, material, stream)
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
		return nil, false, errors.New("Anthropic request payload must be one JSON object")
	}
	encodedModel, _ := json.Marshal(model)
	payload["model"] = encodedModel
	stream := false
	if rawStream, exists := payload["stream"]; exists {
		if err := json.Unmarshal(rawStream, &stream); err != nil {
			return nil, false, errors.New("Anthropic request stream field must be boolean")
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, false, errors.New("could not encode the Anthropic request payload")
	}
	if len(body) > maxBodyBytes {
		return nil, false, errors.New("Anthropic request payload exceeds the safety limit")
	}
	return body, stream, nil
}

func nativeAuthMaterial(input vnextprotocol.AuthInput) (vnextprotocol.AuthMaterial, error) {
	if input.Scheme != vnextprotocol.AuthXAPIKey {
		return vnextprotocol.AuthMaterial{}, errors.New("native Anthropic API requires the x-api-key auth scheme")
	}
	material, err := input.Material()
	if err != nil {
		return vnextprotocol.AuthMaterial{}, err
	}
	if !strings.EqualFold(material.HeaderName, "x-api-key") || material.HeaderValue == "" {
		return vnextprotocol.AuthMaterial{}, errors.New("native Anthropic API requires x-api-key auth material")
	}
	return material, nil
}

func applyNativeHeaders(header http.Header, material vnextprotocol.AuthMaterial, stream bool) {
	header.Set(material.HeaderName, material.HeaderValue)
	header.Set("anthropic-version", anthropicVersion)
	if stream {
		header.Set("Accept", "text/event-stream")
	} else {
		header.Set("Accept", "application/json")
	}
	header.Set("Content-Type", "application/json")
}

func endpointURL(rawBaseURL, suffix string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimSpace(rawBaseURL))
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.Fragment != "" {
		return nil, errors.New("invalid Anthropic base URL; use an http or https URL without user information or fragments")
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
	for _, otherSurface := range []string{"/models", "/messages"} {
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

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	if reader == nil {
		return nil, errors.New("Anthropic response body is unavailable")
	}
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, errors.New("could not read the Anthropic response body")
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

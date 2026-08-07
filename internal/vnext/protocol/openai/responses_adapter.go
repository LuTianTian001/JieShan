package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

// ResponsesAdapter implements only the native OpenAI Responses surface. It is
// intentionally a separate concrete type from ChatCompletionsAdapter so the
// registry derives capabilities independently for each surface.
type ResponsesAdapter struct {
	client HTTPDoer
}

func NewResponsesAdapter(client HTTPDoer) (*ResponsesAdapter, error) {
	if client == nil {
		return nil, errors.New("OpenAI Responses adapter requires a secured HTTP client")
	}
	return &ResponsesAdapter{client: client}, nil
}

var (
	_ vnextprotocol.Discoverer      = (*ResponsesAdapter)(nil)
	_ vnextprotocol.RequestEncoder  = (*ResponsesAdapter)(nil)
	_ vnextprotocol.ResponseDecoder = (*ResponsesAdapter)(nil)
	_ vnextprotocol.StreamDecoder   = (*ResponsesAdapter)(nil)
	_ vnextprotocol.UsageExtractor  = (*ResponsesAdapter)(nil)
	_ vnextprotocol.ErrorDecoder    = (*ResponsesAdapter)(nil)
)

func (adapter *ResponsesAdapter) DiscoverModels(ctx context.Context, input vnextprotocol.DiscoveryInput) (vnextprotocol.DiscoveryResult, error) {
	if adapter == nil || adapter.client == nil {
		return vnextprotocol.DiscoveryResult{Models: make([]string, 0)}, errors.New("OpenAI Responses adapter is unavailable")
	}
	return discoverModels(ctx, adapter.client, input)
}

func (adapter *ResponsesAdapter) EncodeRequest(_ context.Context, input vnextprotocol.RequestBuildInput) (vnextprotocol.EncodedRequest, error) {
	if input.Protocol != vnextprotocol.OpenAI || input.Surface != vnextprotocol.OpenAIResponses {
		return vnextprotocol.EncodedRequest{}, fmt.Errorf("OpenAI Responses adapter cannot encode %q/%q", input.Protocol, input.Surface)
	}
	model := strings.TrimSpace(input.Model)
	if model == "" {
		return vnextprotocol.EncodedRequest{}, errors.New("OpenAI Responses source model is required")
	}
	if len(input.Payload) == 0 {
		return vnextprotocol.EncodedRequest{}, errors.New("OpenAI Responses request payload is required")
	}
	if len(input.Payload) > maxBodyBytes {
		return vnextprotocol.EncodedRequest{}, errors.New("OpenAI Responses request payload exceeds the safety limit")
	}
	requestURL, err := endpointURL(input.BaseURL, "/responses")
	if err != nil {
		return vnextprotocol.EncodedRequest{}, err
	}
	material, err := input.Auth.Material()
	if err != nil {
		return vnextprotocol.EncodedRequest{}, err
	}
	body, stream, err := rewriteResponsesRequestPayload(input.Payload, model)
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

func rewriteResponsesRequestPayload(raw []byte, model string) ([]byte, bool, error) {
	var payload map[string]json.RawMessage
	if err := decodeSingleJSON(raw, &payload); err != nil || payload == nil {
		return nil, false, errors.New("OpenAI Responses request payload must be one JSON object")
	}
	encodedModel, _ := json.Marshal(model)
	payload["model"] = encodedModel
	stream := false
	if rawStream, exists := payload["stream"]; exists {
		if err := json.Unmarshal(rawStream, &stream); err != nil {
			return nil, false, errors.New("OpenAI Responses request stream field must be boolean")
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, false, errors.New("could not encode the OpenAI Responses request payload")
	}
	if len(body) > maxBodyBytes {
		return nil, false, errors.New("OpenAI Responses request payload exceeds the safety limit")
	}
	return body, stream, nil
}

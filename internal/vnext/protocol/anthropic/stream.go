package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

var (
	ErrStreamTruncated = errors.New("stream_truncated")
	ErrStreamProtocol  = errors.New("anthropic_stream_protocol_error")
	errEventTooLarge   = errors.New("Anthropic stream event exceeds the safety limit")
)

type streamState struct {
	model      string
	seenStart  bool
	seenDelta  bool
	cumulative *anthropicUsagePayload
}

func (adapter *MessagesAdapter) DecodeStream(ctx context.Context, input vnextprotocol.StreamInput, emit func(vnextprotocol.StreamEvent) error) (vnextprotocol.StreamResult, error) {
	result := vnextprotocol.StreamResult{}
	if input.Body == nil {
		return result, errors.New("Anthropic stream body is unavailable")
	}
	if input.StatusCode < http.StatusOK || input.StatusCode >= http.StatusMultipleChoices {
		body, err := readBounded(input.Body, maxBodyBytes)
		if err != nil {
			return result, err
		}
		return result, adapter.statusError(input.StatusCode, input.Header, body)
	}
	state := &streamState{}
	reader := bufio.NewReaderSize(input.Body, 32<<10)
	var buffered bytes.Buffer
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		line, readErr := readSSELine(reader, maxEventBytes-buffered.Len())
		if len(line) > 0 {
			if isBlankSSELine(line) {
				if buffered.Len() > 0 {
					raw := make([]byte, 0, buffered.Len()+len(line))
					raw = append(raw, buffered.Bytes()...)
					raw = append(raw, line...)
					event, emitEvent, err := decodeStreamEvent(raw, state)
					if err != nil {
						return result, err
					}
					result.Model = state.model
					if event.Terminal {
						result.Terminal = true
					}
					if emitEvent && emit != nil {
						if err := emit(event); err != nil {
							return result, err
						}
					}
					if result.Terminal {
						return result, nil
					}
					buffered.Reset()
				}
			} else {
				if buffered.Len()+len(line) > maxEventBytes {
					return result, errEventTooLarge
				}
				_, _ = buffered.Write(line)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				if err := ctx.Err(); err != nil {
					return result, err
				}
				if buffered.Len() > 0 {
					event, emitEvent, err := decodeStreamEvent(buffered.Bytes(), state)
					if err != nil {
						return result, err
					}
					result.Model = state.model
					result.Terminal = event.Terminal
					if emitEvent && emit != nil {
						if err := emit(event); err != nil {
							return result, err
						}
					}
					if result.Terminal {
						return result, nil
					}
				}
				return result, ErrStreamTruncated
			}
			if errors.Is(readErr, errEventTooLarge) {
				return result, errEventTooLarge
			}
			return result, errors.New("could not read the Anthropic event stream")
		}
	}
}

func decodeStreamEvent(raw []byte, state *streamState) (vnextprotocol.StreamEvent, bool, error) {
	event := vnextprotocol.StreamEvent{Body: bytes.Clone(raw)}
	eventName, data, hasData := parseSSEEvent(raw)
	if !hasData {
		return event, len(bytes.TrimSpace(raw)) > 0, nil
	}
	var envelope struct {
		Type         string                 `json:"type"`
		Message      json.RawMessage        `json:"message"`
		ContentBlock json.RawMessage        `json:"content_block"`
		Delta        json.RawMessage        `json:"delta"`
		Usage        *anthropicUsagePayload `json:"usage"`
	}
	if err := decodeSingleJSON(data, &envelope); err != nil {
		return vnextprotocol.StreamEvent{}, false, errors.New("Anthropic stream contained a malformed JSON event")
	}
	eventType := strings.TrimSpace(envelope.Type)
	if eventType == "" {
		return vnextprotocol.StreamEvent{}, false, fmt.Errorf("%w: event type is missing", ErrStreamProtocol)
	}
	if eventName != "" && eventName != eventType {
		return vnextprotocol.StreamEvent{}, false, fmt.Errorf("%w: SSE event name does not match JSON type", ErrStreamProtocol)
	}
	switch eventType {
	case "ping":
		return event, true, nil
	case "error":
		decoded := decodeAnthropicError(vnextprotocol.ErrorInput{Body: data})
		return vnextprotocol.StreamEvent{}, false, fmt.Errorf("%s: %s", decoded.Class, decoded.Message)
	case "message_start":
		if state.seenStart {
			return vnextprotocol.StreamEvent{}, false, fmt.Errorf("%w: duplicate message_start", ErrStreamProtocol)
		}
		var message struct {
			Type  string                 `json:"type"`
			Role  string                 `json:"role"`
			Model string                 `json:"model"`
			Usage *anthropicUsagePayload `json:"usage"`
		}
		if decodeSingleJSON(envelope.Message, &message) != nil || strings.TrimSpace(message.Type) != "message" || strings.TrimSpace(message.Role) != "assistant" || strings.TrimSpace(message.Model) == "" {
			return vnextprotocol.StreamEvent{}, false, fmt.Errorf("%w: malformed message_start", ErrStreamProtocol)
		}
		if message.Usage != nil {
			if _, err := normalizeUsage(message.Usage); err != nil {
				return vnextprotocol.StreamEvent{}, false, err
			}
			state.cumulative = mergeUsage(nil, message.Usage)
		}
		state.model = strings.TrimSpace(message.Model)
		state.seenStart = true
		return event, true, nil
	case "message_delta":
		if !state.seenStart {
			return vnextprotocol.StreamEvent{}, false, fmt.Errorf("%w: message_delta arrived without message_start", ErrStreamProtocol)
		}
		if envelope.Usage != nil {
			state.cumulative = mergeUsage(state.cumulative, envelope.Usage)
			if _, err := normalizeUsage(state.cumulative); err != nil {
				return vnextprotocol.StreamEvent{}, false, err
			}
		}
		state.seenDelta = true
		return event, true, nil
	case "content_block_start":
		if !state.seenStart {
			return vnextprotocol.StreamEvent{}, false, fmt.Errorf("%w: content block arrived before message_start", ErrStreamProtocol)
		}
		semantic, err := contentBlockIsSemantic(envelope.ContentBlock)
		if err != nil {
			return vnextprotocol.StreamEvent{}, false, err
		}
		event.Semantic = semantic
		return event, true, nil
	case "content_block_delta":
		if !state.seenStart {
			return vnextprotocol.StreamEvent{}, false, fmt.Errorf("%w: content delta arrived before message_start", ErrStreamProtocol)
		}
		semantic, err := contentDeltaIsSemantic(envelope.Delta)
		if err != nil {
			return vnextprotocol.StreamEvent{}, false, err
		}
		event.Semantic = semantic
		return event, true, nil
	case "content_block_stop":
		if !state.seenStart {
			return vnextprotocol.StreamEvent{}, false, fmt.Errorf("%w: content block stopped before message_start", ErrStreamProtocol)
		}
		return event, true, nil
	case "message_stop":
		if !state.seenStart || !state.seenDelta {
			return vnextprotocol.StreamEvent{}, false, fmt.Errorf("%w: message_stop arrived before the message lifecycle completed", ErrStreamProtocol)
		}
		event.Terminal = true
		return event, true, nil
	default:
		return event, true, nil
	}
}

func contentDeltaIsSemantic(raw json.RawMessage) (bool, error) {
	var delta map[string]json.RawMessage
	if err := decodeSingleJSON(raw, &delta); err != nil || delta == nil {
		return false, errors.New("Anthropic stream contained a malformed content delta")
	}
	var deltaType string
	if json.Unmarshal(delta["type"], &deltaType) != nil || strings.TrimSpace(deltaType) == "" {
		return false, errors.New("Anthropic content delta did not contain a type")
	}
	switch deltaType {
	case "text_delta":
		return semanticJSONValue(delta["text"]), nil
	case "thinking_delta":
		return semanticJSONValue(delta["thinking"]), nil
	case "input_json_delta":
		return semanticJSONValue(delta["partial_json"]), nil
	case "citations_delta":
		return nonNullJSON(delta["citation"]), nil
	case "signature_delta":
		return false, nil
	default:
		return len(delta) > 1, nil
	}
}

func parseSSEEvent(raw []byte) (string, []byte, bool) {
	normalized := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	eventName := ""
	dataLines := make([]string, 0, len(lines))
	for index, line := range lines {
		if index == 0 {
			line = strings.TrimPrefix(line, "\ufeff")
		}
		if strings.HasPrefix(line, "event:") {
			value := strings.TrimPrefix(strings.TrimPrefix(line, "event:"), " ")
			eventName = strings.TrimSpace(value)
			continue
		}
		if line == "data" {
			dataLines = append(dataLines, "")
			continue
		}
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
			dataLines = append(dataLines, value)
		}
	}
	if len(dataLines) == 0 {
		return eventName, nil, false
	}
	return eventName, []byte(strings.Join(dataLines, "\n")), true
}

func readSSELine(reader *bufio.Reader, limit int) ([]byte, error) {
	if limit <= 0 {
		return nil, errEventTooLarge
	}
	line := make([]byte, 0, min(limit, 32<<10))
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > limit {
			return nil, errEventTooLarge
		}
		line = append(line, fragment...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}

func isBlankSSELine(line []byte) bool {
	return len(bytes.TrimRight(line, "\r\n")) == 0
}

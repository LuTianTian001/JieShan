package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

var (
	ErrStreamTruncated = errors.New("stream_truncated")
	errEventTooLarge   = errors.New("OpenAI stream event exceeds the safety limit")
)

func (adapter *ChatCompletionsAdapter) DecodeStream(ctx context.Context, input vnextprotocol.StreamInput, emit func(vnextprotocol.StreamEvent) error) (vnextprotocol.StreamResult, error) {
	return decodeOpenAIEventStream(ctx, input, emit, adapter.statusError, decodeChatSSEEvent)
}

type statusErrorDecoder func(int, http.Header, []byte) error
type sseEventDecoder func([]byte) (vnextprotocol.StreamEvent, string, bool, error)

func decodeOpenAIEventStream(ctx context.Context, input vnextprotocol.StreamInput, emit func(vnextprotocol.StreamEvent) error, statusError statusErrorDecoder, decodeEvent sseEventDecoder) (vnextprotocol.StreamResult, error) {
	result := vnextprotocol.StreamResult{}
	if input.Body == nil {
		return result, errors.New("OpenAI stream body is unavailable")
	}
	if input.StatusCode < http.StatusOK || input.StatusCode >= http.StatusMultipleChoices {
		body, err := readBounded(input.Body, maxBodyBytes)
		if err != nil {
			return result, err
		}
		return result, statusError(input.StatusCode, input.Header, body)
	}

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
					event, model, emitEvent, err := decodeEvent(raw)
					if err != nil {
						return result, err
					}
					if model != "" {
						result.Model = model
					}
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
				return result, ErrStreamTruncated
			}
			if errors.Is(readErr, errEventTooLarge) {
				return result, errEventTooLarge
			}
			return result, errors.New("could not read the OpenAI event stream")
		}
	}
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

func decodeChatSSEEvent(raw []byte) (vnextprotocol.StreamEvent, string, bool, error) {
	event := vnextprotocol.StreamEvent{Body: bytes.Clone(raw)}
	data, hasData := sseDataFromEvent(raw)
	if !hasData {
		return event, "", len(bytes.TrimSpace(raw)) > 0, nil
	}
	if strings.TrimSpace(string(data)) == "[DONE]" {
		event.Terminal = true
		return event, "", true, nil
	}
	var chunk streamChunk
	if err := decodeSingleJSON(data, &chunk); err != nil {
		return vnextprotocol.StreamEvent{}, "", false, errors.New("OpenAI stream contained a malformed JSON event")
	}
	if nonNullJSON(chunk.Error) {
		return vnextprotocol.StreamEvent{}, "", false, errors.New("OpenAI stream returned an error event")
	}
	event.Semantic = chunk.hasSemanticDelta()
	return event, strings.TrimSpace(chunk.Model), true, nil
}

func sseDataFromEvent(raw []byte) ([]byte, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, false
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return trimmed, true
	}
	normalized := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	dataLines := make([]string, 0, len(lines))
	for index, line := range lines {
		if index == 0 {
			line = strings.TrimPrefix(line, "\ufeff")
		}
		if line == "data" {
			dataLines = append(dataLines, "")
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		value := strings.TrimPrefix(line, "data:")
		value = strings.TrimPrefix(value, " ")
		dataLines = append(dataLines, value)
	}
	if len(dataLines) == 0 {
		return nil, false
	}
	return []byte(strings.Join(dataLines, "\n")), true
}

type streamChunk struct {
	Model   string              `json:"model"`
	Error   json.RawMessage     `json:"error"`
	Choices []streamChunkChoice `json:"choices"`
}

type streamChunkChoice struct {
	Delta streamDelta `json:"delta"`
}

type streamDelta struct {
	Content          json.RawMessage   `json:"content"`
	ReasoningContent json.RawMessage   `json:"reasoning_content"`
	Reasoning        json.RawMessage   `json:"reasoning"`
	Refusal          json.RawMessage   `json:"refusal"`
	Audio            json.RawMessage   `json:"audio"`
	ToolCalls        []json.RawMessage `json:"tool_calls"`
	FunctionCall     json.RawMessage   `json:"function_call"`
}

func (chunk streamChunk) hasSemanticDelta() bool {
	for _, choice := range chunk.Choices {
		delta := choice.Delta
		if semanticJSONValue(delta.Content) || semanticJSONValue(delta.ReasoningContent) ||
			semanticJSONValue(delta.Reasoning) || semanticJSONValue(delta.Refusal) ||
			nonNullJSON(delta.Audio) || len(delta.ToolCalls) > 0 || nonEmptyJSONObject(delta.FunctionCall) {
			return true
		}
	}
	return false
}

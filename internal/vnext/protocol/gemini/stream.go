package gemini

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
	ErrStreamProtocol  = errors.New("gemini_stream_protocol_error")
	errEventTooLarge   = errors.New("Gemini stream event exceeds the safety limit")
)

type streamState struct {
	model    string
	finished bool
}

func (adapter *GenerateContentAdapter) DecodeStream(ctx context.Context, input vnextprotocol.StreamInput, emit func(vnextprotocol.StreamEvent) error) (vnextprotocol.StreamResult, error) {
	result := vnextprotocol.StreamResult{}
	if input.Body == nil {
		return result, errors.New("Gemini stream body is unavailable")
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
					event, emitEvent, err := adapter.decodeStreamEvent(raw, state)
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
				return result, ErrStreamTruncated
			}
			if errors.Is(readErr, errEventTooLarge) {
				return result, errEventTooLarge
			}
			return result, errors.New("could not read the Gemini event stream")
		}
	}
}

func (adapter *GenerateContentAdapter) decodeStreamEvent(raw []byte, state *streamState) (vnextprotocol.StreamEvent, bool, error) {
	event := vnextprotocol.StreamEvent{Body: bytes.Clone(raw)}
	data, hasData := sseData(raw)
	if !hasData {
		return event, len(bytes.TrimSpace(raw)) > 0, nil
	}
	var response generateContentResponse
	if decodeSingleJSON(data, &response) != nil {
		return vnextprotocol.StreamEvent{}, false, errors.New("Gemini stream contained a malformed JSON event")
	}
	if nonNullJSON(response.Error) {
		return vnextprotocol.StreamEvent{}, false, adapter.statusError(0, nil, data)
	}
	if len(response.Candidates) == 0 && response.PromptFeedback == nil && response.Usage == nil && strings.TrimSpace(response.ModelVersion) == "" {
		return vnextprotocol.StreamEvent{}, false, streamProtocolError("Gemini stream contained an empty response event")
	}
	if err := validateResponseShapeForStream(&response); err != nil {
		return vnextprotocol.StreamEvent{}, false, err
	}
	model := strings.TrimSpace(response.ModelVersion)
	if model != "" {
		if state.model != "" && state.model != model {
			return vnextprotocol.StreamEvent{}, false, streamProtocolError("Gemini stream changed model version")
		}
		state.model = model
	}
	if response.Usage != nil {
		if _, err := normalizeUsage(response.Usage); err != nil {
			return vnextprotocol.StreamEvent{}, false, err
		}
	}
	if response.PromptFeedback != nil && strings.TrimSpace(response.PromptFeedback.BlockReason) != "" {
		state.finished = true
	}
	if len(response.Candidates) > 0 {
		allFinished := true
		for _, candidate := range response.Candidates {
			if !terminalFinishReason(candidate.FinishReason) {
				allFinished = false
				break
			}
		}
		state.finished = state.finished || allFinished
	}
	event.Semantic = responseHasSemanticContent(&response)
	if state.finished {
		if state.model == "" {
			return vnextprotocol.StreamEvent{}, false, streamProtocolError("Gemini completed stream did not identify its model version")
		}
		event.Terminal = true
	}
	return event, true, nil
}

func validateResponseShapeForStream(response *generateContentResponse) error {
	for _, candidate := range response.Candidates {
		if candidate.Content == nil {
			if terminalFinishReason(candidate.FinishReason) {
				continue
			}
			return streamProtocolError("Gemini stream candidate did not contain content")
		}
		if len(candidate.Content.Parts) == 0 && !terminalFinishReason(candidate.FinishReason) {
			return streamProtocolError("Gemini stream candidate content did not contain parts")
		}
		for _, part := range candidate.Content.Parts {
			var object map[string]json.RawMessage
			if decodeSingleJSON(part, &object) != nil || len(object) == 0 {
				return streamProtocolError("Gemini stream candidate contained a malformed content part")
			}
		}
	}
	return nil
}

func streamProtocolError(message string) error {
	return fmt.Errorf("%w: %s", ErrStreamProtocol, message)
}

func sseData(raw []byte) ([]byte, bool) {
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
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
			dataLines = append(dataLines, value)
		}
	}
	if len(dataLines) == 0 {
		return nil, false
	}
	return []byte(strings.Join(dataLines, "\n")), true
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

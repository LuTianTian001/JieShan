package gateway

import (
	"context"
	"errors"
	"net/http"

	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	"github.com/LuTianTian001/JieShan/internal/vnext/resolver"
	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
)

var errPrecommitStreamLimit = errors.New("upstream stream produced too much data before semantic output")

type streamSinkError struct {
	err error
}

func (err *streamSinkError) Error() string { return "downstream stream write failed" }
func (err *streamSinkError) Unwrap() error { return err.err }

func (service *Service) consumeStream(
	ctx context.Context,
	response *http.Response,
	components protocol.AdapterComponents,
	sink StreamSink,
	watchdog *attemptWatchdog,
	policy routing.HealthPolicy,
	result *Result,
	attempt *Attempt,
	metadata resolver.EndpointMetadata,
	candidate routing.Candidate,
	sequence uint64,
) error {
	defer response.Body.Close()
	attempt.HTTPStatus = response.StatusCode
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		watchdog.Stop()
		body, err := readResponseBody(response.Body, service.maxBody)
		if err != nil {
			failure := routing.Failure{Kind: routing.FailureUpstreamTransient}
			attempt.ErrorCode = "invalid_upstream_error_body"
			attempt.ErrorClass = string(failure.Kind)
			service.finishFailure(ctx, policy, result.RequestID, metadata, candidate, sequence, failure, attempt)
			return &retryError{failure: failure}
		}
		decoded, decodeErr := components.ErrorDecoder.DecodeError(ctx, protocol.ErrorInput{
			StatusCode: response.StatusCode, Header: response.Header.Clone(), Body: body,
		})
		if decodeErr != nil {
			decoded = protocol.DecodedError{Code: "error_decode_failed", Class: string(routing.FailureUpstreamTransient)}
		}
		failure := failureFromDecoded(decoded, response.Header)
		attempt.ErrorCode = decoded.Code
		attempt.ErrorClass = decoded.Class
		service.finishFailure(ctx, policy, result.RequestID, metadata, candidate, sequence, failure, attempt)
		return terminalOrRetry(failure)
	}

	responseHeader := safeResponseHeaders(response.Header, true)
	events := make([]protocol.StreamEvent, 0, 16)
	buffered := make([]protocol.StreamEvent, 0, 4)
	var totalBytes, precommitBytes int64
	committed := false
	semantic := false

	streamResult, decodeErr := components.StreamDecoder.DecodeStream(ctx, protocol.StreamInput{
		StatusCode: response.StatusCode,
		Header:     response.Header.Clone(),
		Body:       response.Body,
	}, func(event protocol.StreamEvent) error {
		watchdog.ObserveStreamEvent(event.Semantic)
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		cloned := protocol.StreamEvent{
			Body: append([]byte(nil), event.Body...), Semantic: event.Semantic, Terminal: event.Terminal,
		}
		totalBytes += int64(len(cloned.Body))
		if totalBytes > service.maxBody {
			return errPrecommitStreamLimit
		}
		events = append(events, cloned)
		if event.Semantic {
			semantic = true
		}
		if !committed {
			precommitBytes += int64(len(cloned.Body))
			if precommitBytes > service.maxBuffer {
				return errPrecommitStreamLimit
			}
			buffered = append(buffered, cloned)
			if !event.Semantic {
				return nil
			}
			if err := sink.Commit(responseHeader.Clone()); err != nil {
				return &streamSinkError{err: err}
			}
			// Commit is the irreversible boundary. Record it before writing any
			// buffered event so a downstream write failure cannot be represented
			// as an uncommitted attempt or become eligible for replay later.
			committed = true
			for _, bufferedEvent := range buffered {
				if err := sink.Write(bufferedEvent.Body); err != nil {
					return &streamSinkError{err: err}
				}
				service.markFirstToken(attempt, bufferedEvent)
			}
			buffered = nil
			return nil
		}
		if err := sink.Write(cloned.Body); err != nil {
			return &streamSinkError{err: err}
		}
		service.markFirstToken(attempt, cloned)
		return nil
	})

	if decodeErr != nil || !streamResult.Terminal || !semantic {
		if failure, errorCode, terminalErr, classified := contextFailure(ctx, committed); classified {
			attempt.ErrorCode = errorCode
			attempt.ErrorClass = string(failure.Kind)
			service.finishFailure(ctx, policy, result.RequestID, metadata, candidate, sequence, failure, attempt)
			if failure.Kind == routing.FailureDownstreamCanceled {
				attempt.Outcome = "cancelled"
			}
			if terminalErr != nil {
				return terminalErr
			}
			return &retryError{failure: failure}
		}
		kind := routing.FailureStreamTruncated
		var sinkErr *streamSinkError
		if ctx.Err() != nil || errors.As(decodeErr, &sinkErr) {
			kind = routing.FailureDownstreamCanceled
		}
		failure := routing.Failure{Kind: kind, ResponseCommitted: committed}
		attempt.ErrorCode = "stream_incomplete"
		attempt.ErrorClass = string(kind)
		service.finishFailure(ctx, policy, result.RequestID, metadata, candidate, sequence, failure, attempt)
		if kind == routing.FailureDownstreamCanceled {
			attempt.Outcome = "cancelled"
			return ErrDownstreamClosed
		}
		if committed {
			return ErrCommittedStreamFailed
		}
		return &retryError{failure: failure}
	}

	attempt.ResponseModel = responseModel(streamResult.Model, metadata.SourceModel)
	usage, err := components.UsageExtractor.ExtractUsage(ctx, protocol.UsageInput{Events: events})
	recordMetering(result, usage, err)
	if failure, errorCode, terminalErr, classified := contextFailure(ctx, committed); classified {
		attempt.ErrorCode = errorCode
		attempt.ErrorClass = string(failure.Kind)
		service.finishFailure(ctx, policy, result.RequestID, metadata, candidate, sequence, failure, attempt)
		if terminalErr != nil {
			return terminalErr
		}
		return &retryError{failure: failure}
	}

	service.finishSuccess(ctx, policy, metadata, candidate, sequence, attempt)
	result.UpstreamModel = attempt.ResponseModel
	result.TargetID = candidate.Target.ID
	result.CredentialID = candidate.Credential.ID
	result.StatusCode = response.StatusCode
	result.Header = responseHeader
	return nil
}

func (service *Service) markFirstToken(attempt *Attempt, event protocol.StreamEvent) {
	if attempt == nil || !event.Semantic || attempt.FirstTokenMS != nil {
		return
	}
	value := elapsedMilliseconds(attempt.StartedAt, service.now().UTC())
	attempt.FirstTokenMS = &value
}

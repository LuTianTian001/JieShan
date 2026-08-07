package gateway

import (
	"context"
	"net/http"

	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	"github.com/LuTianTian001/JieShan/internal/vnext/resolver"
	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func (service *Service) consumeResponse(
	ctx context.Context,
	response *http.Response,
	components protocol.AdapterComponents,
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
	body, err := readResponseBody(&observedReader{reader: response.Body, observe: watchdog.ObserveBodyRead}, service.maxBody)
	if err != nil {
		if failure, errorCode, terminalErr, classified := contextFailure(ctx, false); classified {
			attempt.ErrorCode = errorCode
			attempt.ErrorClass = string(failure.Kind)
			service.finishFailure(ctx, policy, result.RequestID, metadata, candidate, sequence, failure, attempt)
			if terminalErr != nil {
				return terminalErr
			}
			return &retryError{failure: failure}
		}
		failure := routing.Failure{Kind: routing.FailureUpstreamTransient}
		attempt.ErrorCode = "invalid_upstream_body"
		attempt.ErrorClass = string(failure.Kind)
		service.finishFailure(ctx, policy, result.RequestID, metadata, candidate, sequence, failure, attempt)
		return &retryError{failure: failure}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		decoded, decodeErr := components.ErrorDecoder.DecodeError(ctx, protocol.ErrorInput{
			StatusCode: response.StatusCode,
			Header:     response.Header.Clone(),
			Body:       body,
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

	decoded, err := components.ResponseDecoder.DecodeResponse(ctx, protocol.ResponseInput{
		StatusCode: response.StatusCode,
		Header:     response.Header.Clone(),
		Body:       body,
	})
	if err != nil {
		failure := routing.Failure{Kind: routing.FailureUpstreamTransient}
		attempt.ErrorCode = "malformed_success_response"
		attempt.ErrorClass = string(failure.Kind)
		service.finishFailure(ctx, policy, result.RequestID, metadata, candidate, sequence, failure, attempt)
		return &retryError{failure: failure}
	}
	attempt.ResponseModel = responseModel(decoded.Model, metadata.SourceModel)
	usage, err := components.UsageExtractor.ExtractUsage(ctx, protocol.UsageInput{Body: decoded.Body})
	recordMetering(result, usage, err)
	if failure, errorCode, terminalErr, classified := contextFailure(ctx, false); classified {
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
	result.Header = safeResponseHeaders(response.Header, false)
	result.Body = append([]byte(nil), decoded.Body...)
	return nil
}

func terminalOrRetry(failure routing.Failure) error {
	switch failure.Disposition().Retry {
	case routing.RetryNextCredential, routing.RetryNextTarget:
		return &retryError{failure: failure}
	case routing.RetryStop:
		if failure.Kind == routing.FailureClientInvalid {
			return ErrInvalidRequest
		}
		if failure.Kind == routing.FailureDownstreamCanceled {
			return ErrDownstreamClosed
		}
		return ErrNoAvailableUpstream
	default:
		return ErrNoAvailableUpstream
	}
}

var _ HealthRepository = (*vnextstore.Store)(nil)

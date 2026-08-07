// Package probeexec owns production active-probe execution. Scheduling and
// durable history remain in monitoring; protocol encoding and error decoding
// remain in the registered adapters.
package probeexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/gateway"
	"github.com/LuTianTian001/JieShan/internal/vnext/monitoring"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	"github.com/LuTianTian001/JieShan/internal/vnext/resolver"
	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
)

const defaultMaxResponseBytes = int64(4 << 20)

var errFirstSemanticOutput = errors.New("probe observed first semantic output")

type Options struct {
	MaxResponseBytes int64
	Now              func() time.Time
}

type Executor struct {
	registry protocolRegistry
	client   gateway.HTTPDoer
	secrets  gateway.SecretProvider
	effects  gateway.CredentialEffectStore
	maxBody  int64
	now      func() time.Time
}

type protocolRegistry interface {
	Components(protocol.Protocol, protocol.Surface) (protocol.AdapterComponents, error)
}

func New(
	registry protocolRegistry,
	client gateway.HTTPDoer,
	secrets gateway.SecretProvider,
	effects gateway.CredentialEffectStore,
	options Options,
) (*Executor, error) {
	if registry == nil || client == nil || secrets == nil || effects == nil {
		return nil, errors.New("probe registry, client, secret provider, and credential effects are required")
	}
	if options.MaxResponseBytes <= 0 {
		options.MaxResponseBytes = defaultMaxResponseBytes
	}
	if options.MaxResponseBytes > 64<<20 {
		return nil, errors.New("probe response limit cannot exceed 64 MiB")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Executor{
		registry: registry,
		client:   client,
		secrets:  secrets,
		effects:  effects,
		maxBody:  options.MaxResponseBytes,
		now:      options.Now,
	}, nil
}

func (executor *Executor) Probe(ctx context.Context, request monitoring.ProbeRequest) (monitoring.ProbeObservation, error) {
	if executor == nil {
		return monitoring.ProbeObservation{}, errors.New("probe executor is unavailable")
	}
	if ctx == nil {
		return monitoring.ProbeObservation{}, errors.New("probe context is required")
	}
	if strings.TrimSpace(request.RunID) == "" || request.Target.ProviderModelTargetID <= 0 ||
		request.Target.SiteID <= 0 || request.Target.EndpointID <= 0 || strings.TrimSpace(request.Target.SourceModel) == "" {
		return monitoring.ProbeObservation{}, errors.New("probe request identity is invalid")
	}

	wireProtocol, surface, authScheme, components, payload, metadata, err := executor.prepare(request.Target)
	if err != nil {
		failure := routing.Failure{Kind: routing.FailureTargetMisconfigured}
		return monitoring.ProbeObservation{
			Outcome: monitoring.OutcomeFailure, Failure: failure, ErrorCode: "probe_target_invalid",
		}, nil
	}

	startedAt := executor.now().UTC()
	observation := monitoring.ProbeObservation{Attempts: make([]monitoring.ProbeAttempt, 0, len(request.Target.CredentialIDs))}
	if len(request.Target.CredentialIDs) == 0 {
		observation.Outcome = monitoring.OutcomeFailure
		observation.Failure = routing.Failure{Kind: routing.FailureCredentialAuth}
		observation.ErrorCode = "probe_no_credentials"
		observation.Latency = elapsed(startedAt, executor.now().UTC())
		return observation, nil
	}

	for _, rawCredentialID := range request.Target.CredentialIDs {
		credentialID := routing.CredentialID(rawCredentialID)
		attemptStartedAt := executor.now().UTC()
		attempt := monitoring.ProbeAttempt{CredentialID: rawCredentialID}
		if credentialID <= 0 {
			attempt = finishAttempt(executor.now, attemptStartedAt, attempt, routing.FailureCredentialAuth, "probe_credential_invalid", 0)
			observation.Attempts = append(observation.Attempts, attempt)
			continue
		}

		material, materialErr := executor.secrets.Materialize(ctx, metadata, credentialID)
		if materialErr != nil || strings.TrimSpace(material.Credential) == "" {
			failure := routing.Failure{Kind: routing.FailureCredentialAuth}
			attempt = finishAttempt(executor.now, attemptStartedAt, attempt, failure.Kind, "probe_credential_unavailable", 0)
			observation.Attempts = append(observation.Attempts, attempt)
			executor.applyCredentialEffect(ctx, request, credentialID, failure, attempt)
			continue
		}

		encoded, encodeErr := components.RequestEncoder.EncodeRequest(ctx, protocol.RequestBuildInput{
			Protocol: wireProtocol,
			Surface:  surface,
			BaseURL:  request.Target.BaseURL,
			Model:    request.Target.SourceModel,
			Payload:  payload,
			Auth:     protocol.AuthInput{Scheme: authScheme, Secret: material.Credential},
		})
		if encodeErr != nil {
			failure := routing.Failure{Kind: routing.FailureTargetMisconfigured}
			attempt = finishAttempt(executor.now, attemptStartedAt, attempt, failure.Kind, "probe_request_encode_failed", 0)
			observation.Attempts = append(observation.Attempts, attempt)
			return finishObservation(executor.now, startedAt, observation, failure, attempt.ErrorCode, 0, nil), nil
		}

		upstreamRequest, requestErr := http.NewRequestWithContext(ctx, encoded.Method, encoded.URL, bytes.NewReader(encoded.Body))
		if requestErr == nil {
			upstreamRequest.Header = encoded.Header.Clone()
			requestErr = gateway.MergeEndpointHeaders(upstreamRequest.Header, material.Headers)
		}
		if requestErr != nil {
			failure := routing.Failure{Kind: routing.FailureTargetMisconfigured}
			attempt = finishAttempt(executor.now, attemptStartedAt, attempt, failure.Kind, "probe_request_invalid", 0)
			observation.Attempts = append(observation.Attempts, attempt)
			return finishObservation(executor.now, startedAt, observation, failure, attempt.ErrorCode, 0, nil), nil
		}

		response, requestErr := executor.client.Do(upstreamRequest)
		if requestErr != nil || response == nil || response.Body == nil {
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
			failure := routing.Failure{Kind: routing.FailureTransport}
			attempt = finishAttempt(executor.now, attemptStartedAt, attempt, failure.Kind, "probe_transport_failed", 0)
			observation.Attempts = append(observation.Attempts, attempt)
			return finishObservation(executor.now, startedAt, observation, failure, attempt.ErrorCode, 0, nil), nil
		}

		status := response.StatusCode
		if status < http.StatusOK || status >= http.StatusMultipleChoices {
			body, readErr := executor.readBody(response.Body)
			_ = response.Body.Close()
			if readErr != nil {
				failure := routing.Failure{Kind: routing.FailureUpstreamTransient}
				attempt = finishAttempt(executor.now, attemptStartedAt, attempt, failure.Kind, "probe_body_invalid", status)
				observation.Attempts = append(observation.Attempts, attempt)
				return finishObservation(executor.now, startedAt, observation, failure, attempt.ErrorCode, status, nil), nil
			}
			decoded, decodeErr := components.ErrorDecoder.DecodeError(ctx, protocol.ErrorInput{
				StatusCode: status, Header: response.Header.Clone(), Body: body,
			})
			if decodeErr != nil {
				decoded = protocol.DecodedError{Code: "probe_error_decode_failed", Class: string(routing.FailureUpstreamTransient)}
			}
			failure := gateway.FailureFromDecoded(decoded, response.Header, executor.now().UTC())
			code := strings.TrimSpace(decoded.Code)
			if code == "" {
				code = "probe_upstream_rejected"
			}
			attempt = finishAttempt(executor.now, attemptStartedAt, attempt, failure.Kind, code, status)
			observation.Attempts = append(observation.Attempts, attempt)
			disposition := failure.Disposition()
			if disposition.CredentialEffect != routing.CredentialEffectNone {
				executor.applyCredentialEffect(ctx, request, credentialID, failure, attempt)
			}
			if disposition.Retry == routing.RetryNextCredential {
				continue
			}
			return finishObservation(executor.now, startedAt, observation, failure, code, status, nil), nil
		}

		firstOutput, decodeErr := executor.readFirstSemanticOutput(ctx, components.StreamDecoder, response, attemptStartedAt)
		_ = response.Body.Close()
		if decodeErr != nil {
			failure := routing.Failure{Kind: routing.FailureUpstreamTransient}
			attempt = finishAttempt(executor.now, attemptStartedAt, attempt, failure.Kind, "probe_stream_invalid", status)
			observation.Attempts = append(observation.Attempts, attempt)
			return finishObservation(executor.now, startedAt, observation, failure, attempt.ErrorCode, status, firstOutput), nil
		}

		attempt = finishSuccessfulAttempt(executor.now, attemptStartedAt, attempt, status)
		observation.Attempts = append(observation.Attempts, attempt)
		observation.Outcome = monitoring.OutcomeSuccess
		observation.HTTPStatus = status
		observation.Latency = elapsed(startedAt, executor.now().UTC())
		observation.FirstOutputLatency = firstOutput
		return observation, nil
	}

	last := observation.Attempts[len(observation.Attempts)-1]
	failure := routing.Failure{Kind: last.FailureKind}
	if failure.Kind == "" {
		failure.Kind = routing.FailureCredentialAuth
	}
	return finishObservation(executor.now, startedAt, observation, failure, last.ErrorCode, last.HTTPStatus, nil), nil
}

func (executor *Executor) prepare(target monitoring.ProbeTarget) (
	protocol.Protocol,
	protocol.Surface,
	protocol.AuthScheme,
	protocol.AdapterComponents,
	[]byte,
	resolver.EndpointMetadata,
	error,
) {
	wireProtocol, err := protocol.ParseProtocol(target.WireProtocol)
	if err != nil {
		return "", "", "", protocol.AdapterComponents{}, nil, resolver.EndpointMetadata{}, err
	}
	surface, err := protocol.ParseSurface(target.Surface)
	if err != nil || protocol.ValidatePair(wireProtocol, surface) != nil {
		return "", "", "", protocol.AdapterComponents{}, nil, resolver.EndpointMetadata{}, errors.New("probe protocol surface is invalid")
	}
	authScheme, err := protocol.ParseAuthScheme(target.AuthScheme)
	if err != nil {
		return "", "", "", protocol.AdapterComponents{}, nil, resolver.EndpointMetadata{}, err
	}
	components, err := executor.registry.Components(wireProtocol, surface)
	if err != nil || components.RequestEncoder == nil || components.StreamDecoder == nil || components.ErrorDecoder == nil {
		return "", "", "", protocol.AdapterComponents{}, nil, resolver.EndpointMetadata{}, errors.New("probe protocol adapter is incomplete")
	}
	payload, err := probePayload(surface)
	if err != nil {
		return "", "", "", protocol.AdapterComponents{}, nil, resolver.EndpointMetadata{}, err
	}
	credentialIDs := make([]routing.CredentialID, 0, len(target.CredentialIDs))
	for _, id := range target.CredentialIDs {
		credentialIDs = append(credentialIDs, routing.CredentialID(id))
	}
	metadata := resolver.EndpointMetadata{
		TargetID:                     routing.TargetID(target.ProviderModelTargetID),
		PublishedModelTargetID:       target.PublishedModelTargetID,
		PublishedModelTargetRevision: target.PublishedModelTargetRevision,
		SiteID:                       target.SiteID, SiteName: target.SiteName, EndpointID: target.EndpointID, EndpointName: target.EndpointName,
		BaseURL: target.BaseURL, Protocol: wireProtocol, Surface: surface, AuthScheme: authScheme,
		AdapterKind: target.AdapterKind, SourceModel: target.SourceModel,
		HeaderTemplate:             append([]byte(nil), target.HeaderTemplate...),
		SecretHeadersConfigured:    target.SecretHeadersConfigured,
		SecretHeadersCipherVersion: target.SecretHeadersCipherVersion,
		CredentialIDs:              credentialIDs,
	}
	return wireProtocol, surface, authScheme, components, payload, metadata, nil
}

func probePayload(surface protocol.Surface) ([]byte, error) {
	switch surface {
	case protocol.OpenAIChatCompletions:
		return []byte(`{"messages":[{"role":"user","content":"Reply exactly OK."}],"max_tokens":4,"stream":true}`), nil
	case protocol.OpenAIResponses:
		return []byte(`{"input":"Reply exactly OK.","max_output_tokens":16,"stream":true}`), nil
	case protocol.AnthropicMessages:
		return []byte(`{"max_tokens":16,"messages":[{"role":"user","content":"Reply exactly OK."}],"stream":true}`), nil
	case protocol.GeminiGenerateContent:
		return []byte(`{"contents":[{"role":"user","parts":[{"text":"Reply exactly OK."}]}],"generationConfig":{"maxOutputTokens":16,"temperature":0},"stream":true}`), nil
	default:
		return nil, fmt.Errorf("unsupported probe surface %q", surface)
	}
}

func (executor *Executor) readBody(body io.Reader) ([]byte, error) {
	limited := &io.LimitedReader{R: body, N: executor.maxBody + 1}
	result, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(result)) > executor.maxBody {
		return nil, errors.New("probe response exceeds the safety limit")
	}
	return result, nil
}

func (executor *Executor) readFirstSemanticOutput(
	ctx context.Context,
	decoder protocol.StreamDecoder,
	response *http.Response,
	startedAt time.Time,
) (*time.Duration, error) {
	limited := &io.LimitedReader{R: response.Body, N: executor.maxBody + 1}
	var firstOutput *time.Duration
	_, err := decoder.DecodeStream(ctx, protocol.StreamInput{
		StatusCode: response.StatusCode,
		Header:     response.Header.Clone(),
		Body:       limited,
	}, func(event protocol.StreamEvent) error {
		if !event.Semantic {
			return nil
		}
		value := elapsed(startedAt, executor.now().UTC())
		firstOutput = &value
		return errFirstSemanticOutput
	})
	if limited.N <= 0 {
		return firstOutput, errors.New("probe response exceeds the safety limit")
	}
	if errors.Is(err, errFirstSemanticOutput) {
		return firstOutput, nil
	}
	if err != nil {
		return firstOutput, err
	}
	if firstOutput == nil {
		return nil, errors.New("probe stream ended before semantic output")
	}
	return firstOutput, nil
}

func (executor *Executor) applyCredentialEffect(
	ctx context.Context,
	request monitoring.ProbeRequest,
	credentialID routing.CredentialID,
	failure routing.Failure,
	attempt monitoring.ProbeAttempt,
) {
	effect := failure.Disposition().CredentialEffect
	if effect == routing.CredentialEffectNone {
		return
	}
	_ = executor.effects.ApplyCredentialEffect(ctx, gateway.CredentialEffectEvent{
		RequestID: "probe:" + request.RunID,
		SiteID:    request.Target.SiteID, EndpointID: request.Target.EndpointID,
		TargetID: routing.TargetID(request.Target.ProviderModelTargetID), CredentialID: credentialID,
		Effect: effect, OccurredAt: attempt.FinishedAt, RetryAfter: failure.RetryAfter,
		HTTPStatus: attempt.HTTPStatus, ErrorCode: attempt.ErrorCode,
	})
}

func finishAttempt(
	now func() time.Time,
	startedAt time.Time,
	attempt monitoring.ProbeAttempt,
	failure routing.FailureKind,
	code string,
	status int,
) monitoring.ProbeAttempt {
	attempt.Outcome = monitoring.OutcomeFailure
	attempt.FailureKind = failure
	attempt.ErrorCode = code
	attempt.HTTPStatus = status
	attempt.FinishedAt = now().UTC()
	attempt.LatencyMS = durationMilliseconds(elapsed(startedAt, attempt.FinishedAt))
	return attempt
}

func finishSuccessfulAttempt(now func() time.Time, startedAt time.Time, attempt monitoring.ProbeAttempt, status int) monitoring.ProbeAttempt {
	attempt.Outcome = monitoring.OutcomeSuccess
	attempt.HTTPStatus = status
	attempt.FinishedAt = now().UTC()
	attempt.LatencyMS = durationMilliseconds(elapsed(startedAt, attempt.FinishedAt))
	return attempt
}

func finishObservation(
	now func() time.Time,
	startedAt time.Time,
	observation monitoring.ProbeObservation,
	failure routing.Failure,
	code string,
	status int,
	firstOutput *time.Duration,
) monitoring.ProbeObservation {
	observation.Outcome = monitoring.OutcomeFailure
	observation.Failure = failure
	observation.ErrorCode = code
	observation.HTTPStatus = status
	observation.Latency = elapsed(startedAt, now().UTC())
	observation.FirstOutputLatency = firstOutput
	return observation
}

func elapsed(startedAt, finishedAt time.Time) time.Duration {
	if finishedAt.Before(startedAt) {
		return 0
	}
	return finishedAt.Sub(startedAt)
}

func durationMilliseconds(value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	return value.Milliseconds()
}

var _ monitoring.ProbeExecutor = (*Executor)(nil)

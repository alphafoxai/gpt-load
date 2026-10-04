package cpa

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"gpt-load/internal/channel"
	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
	"gpt-load/internal/subscription/providers/qoder"
)

type qoderProviderCredential struct {
	value qoder.Credential
}

func (credential qoderProviderCredential) redactionValues() []string {
	return []string{
		credential.value.AccessToken, credential.value.RefreshToken,
		credential.value.DeviceToken, credential.value.DeviceRefresh,
	}
}

type qoderProviderBridge struct{}

func newQoderProviderBridge() *qoderProviderBridge { return &qoderProviderBridge{} }

func (*qoderProviderBridge) ProviderKind() channel.ProviderKind { return channel.ProviderQoder }

func (*qoderProviderBridge) UpstreamProtocol() protocol.Protocol { return protocol.OpenAICompletions }

func (*qoderProviderBridge) ValidateRouteCapability(route channel.RouteDescriptor) error {
	chat := route.Operation == execution.OperationChatCompletion &&
		(route.ClientProtocol == protocol.OpenAICompletions && route.RouteMode == execution.RouteNative ||
			(route.ClientProtocol == protocol.Anthropic || route.ClientProtocol == protocol.Gemini) &&
				route.RouteMode == execution.RouteConverted)
	responses := route.ClientProtocol == protocol.OpenAIResponses &&
		route.Operation == execution.OperationResponsesCreate &&
		route.RouteMode == execution.RouteConverted
	if !chat && !responses {
		return fmt.Errorf("route is not implemented by Qoder")
	}
	return nil
}

func (*qoderProviderBridge) ParseCredential(raw []byte) (providerCredential, error) {
	value, err := qoder.ParseCredentialJSON(raw)
	if err != nil {
		return nil, err
	}
	return qoderProviderCredential{value: value}, nil
}

func (*qoderProviderBridge) Execute(ctx context.Context, _ string, credential providerCredential, request providerRequest) (providerResponse, error) {
	value, err := qoderCredential(credential)
	if err != nil {
		return providerResponse{}, err
	}
	body, headers, _, err := qoder.Execute(ctx, value, request.Format, request.Payload, proxyURL(request), false)
	if err != nil {
		return providerResponse{}, err
	}
	return providerResponse{Payload: body, Headers: headers, UpstreamProtocol: protocol.OpenAICompletions}, nil
}

func (*qoderProviderBridge) ExecuteStream(ctx context.Context, _ string, credential providerCredential, request providerRequest) (*providerStreamResponse, error) {
	value, err := qoderCredential(credential)
	if err != nil {
		return nil, err
	}
	_, headers, chunks, err := qoder.Execute(ctx, value, request.Format, request.Payload, proxyURL(request), true)
	if err != nil {
		return nil, err
	}
	out := make(chan providerStreamChunk)
	go func() {
		defer close(out)
		for payload := range chunks {
			select {
			case out <- providerStreamChunk{Payload: payload}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return &providerStreamResponse{Headers: headers, Chunks: out, UpstreamProtocol: protocol.OpenAICompletions}, nil
}

func (*qoderProviderBridge) ClassifyError(ctx context.Context, err error, credential providerCredential) (int, *execution.ErrorEvidence) {
	if err == nil {
		return 0, nil
	}
	status := 0
	var statusError interface{ StatusCode() int }
	if errors.As(err, &statusError) && statusError != nil {
		status = statusError.StatusCode()
	}
	kind := execution.ErrorKindTransport
	switch {
	case errors.Is(err, context.DeadlineExceeded) || ctx != nil && errors.Is(context.Cause(ctx), context.DeadlineExceeded):
		kind = execution.ErrorKindTimeout
	case errors.Is(err, context.Canceled) || ctx != nil && errors.Is(context.Cause(ctx), context.Canceled):
		kind = execution.ErrorKindCanceled
	case status != 0:
		kind = execution.ErrorKindHTTP
	case func() bool { _, ok := err.(net.Error); return ok }():
		kind = execution.ErrorKindTransport
	}
	evidence := &execution.ErrorEvidence{Kind: kind, StatusCode: status, Summary: qoderErrorSummary(status)}
	stored, _ := credential.(qoderProviderCredential)
	if summary := safeErrorSummary(err, stored.redactionValues()); summary != "" && status != 0 {
		evidence.Summary = summary
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		evidence.Hint = execution.FailureHintCandidateUnavailable
		evidence.ReplaySafety = execution.ReplaySafetyRejectedBeforeProcessing
	case status == http.StatusTooManyRequests:
		evidence.Hint = execution.FailureHintRateLimited
	case status >= http.StatusInternalServerError:
		evidence.Hint = execution.FailureHintHostError
	}
	return status, evidence
}

func qoderErrorSummary(status int) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "Qoder authorization was rejected"
	case status == http.StatusTooManyRequests:
		return "Qoder upstream rate limit was reached"
	case status >= http.StatusInternalServerError:
		return "Qoder upstream service failed"
	case status >= http.StatusBadRequest:
		return "Qoder upstream request was rejected"
	default:
		return "Qoder upstream request failed"
	}
}

func qoderCredential(credential providerCredential) (qoder.Credential, error) {
	value, ok := credential.(qoderProviderCredential)
	if !ok {
		return qoder.Credential{}, errors.New("Qoder provider bridge credential mismatch")
	}
	return value.value, nil
}

var _ providerBridge = (*qoderProviderBridge)(nil)

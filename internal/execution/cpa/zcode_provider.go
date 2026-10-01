package cpa

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"gpt-load/internal/channel"
	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
	"gpt-load/internal/subscription/providers/zcode"
)

type zcodeProviderCredential struct {
	value zcode.Credential
}

func (credential zcodeProviderCredential) redactionValues() []string {
	values := []string{credential.value.APIKey}
	if credential.value.Token != "" {
		values = append(values, credential.value.Token)
	}
	return values
}

type zcodeProviderBridge struct{}

func newZCodeProviderBridge() *zcodeProviderBridge { return &zcodeProviderBridge{} }

func (*zcodeProviderBridge) ProviderKind() channel.ProviderKind { return channel.ProviderZCode }

func (*zcodeProviderBridge) UpstreamProtocol() protocol.Protocol { return protocol.Anthropic }

func (*zcodeProviderBridge) ValidateRouteCapability(route channel.RouteDescriptor) error {
	chat := route.Operation == execution.OperationChatCompletion &&
		(route.ClientProtocol == protocol.Anthropic && route.RouteMode == execution.RouteNative ||
			(route.ClientProtocol == protocol.OpenAICompletions || route.ClientProtocol == protocol.Gemini) &&
				route.RouteMode == execution.RouteConverted)
	responses := route.ClientProtocol == protocol.OpenAIResponses &&
		route.Operation == execution.OperationResponsesCreate &&
		route.RouteMode == execution.RouteConverted
	if !chat && !responses {
		return fmt.Errorf("route is not implemented by ZCode")
	}
	return nil
}

func (*zcodeProviderBridge) ParseCredential(raw []byte) (providerCredential, error) {
	value, err := zcode.ParseCredentialJSON(raw)
	if err != nil {
		return nil, err
	}
	return zcodeProviderCredential{value: value}, nil
}

func (*zcodeProviderBridge) Execute(ctx context.Context, _ string, credential providerCredential, request providerRequest) (providerResponse, error) {
	value, err := zcodeCredential(credential)
	if err != nil {
		return providerResponse{}, err
	}
	applyCredentialBase(&value, request.BaseURL)
	body, headers, _, err := zcode.Execute(ctx, value, request.Format, request.Payload, request.Headers, proxyURL(request), false)
	if err != nil {
		return providerResponse{}, err
	}
	return providerResponse{
		Payload: body, Headers: headers, UpstreamProtocol: protocol.Anthropic,
	}, nil
}

func (*zcodeProviderBridge) ExecuteStream(ctx context.Context, _ string, credential providerCredential, request providerRequest) (*providerStreamResponse, error) {
	value, err := zcodeCredential(credential)
	if err != nil {
		return nil, err
	}
	applyCredentialBase(&value, request.BaseURL)
	_, headers, chunks, err := zcode.Execute(ctx, value, request.Format, request.Payload, request.Headers, proxyURL(request), true)
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
	return &providerStreamResponse{Headers: headers, Chunks: out, UpstreamProtocol: protocol.Anthropic}, nil
}

func (*zcodeProviderBridge) ClassifyError(ctx context.Context, err error, credential providerCredential) (int, *execution.ErrorEvidence) {
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
	evidence := &execution.ErrorEvidence{Kind: kind, StatusCode: status, Summary: zcodeErrorSummary(status)}
	zcodeCredential, _ := credential.(zcodeProviderCredential)
	if summary := safeErrorSummary(err, zcodeCredential.redactionValues()); summary != "" && status != 0 {
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

func zcodeErrorSummary(status int) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "ZCode authorization was rejected"
	case status == http.StatusTooManyRequests:
		return "ZCode upstream rate limit was reached"
	case status >= http.StatusInternalServerError:
		return "ZCode upstream service failed"
	case status >= http.StatusBadRequest:
		return "ZCode upstream request was rejected"
	default:
		return "ZCode upstream request failed"
	}
}

func zcodeCredential(credential providerCredential) (zcode.Credential, error) {
	value, ok := credential.(zcodeProviderCredential)
	if !ok {
		return zcode.Credential{}, errors.New("ZCode provider bridge credential mismatch")
	}
	return value.value, nil
}

func applyCredentialBase(value *zcode.Credential, requested string) {
	if strings.TrimSpace(value.BaseURL) == "" {
		value.BaseURL = requested
	}
}

func proxyURL(request providerRequest) string {
	if request.ProxyFromEnvironment || request.ProxyURL == "" || request.ProxyURL == "direct" {
		return ""
	}
	return request.ProxyURL
}

var _ providerBridge = (*zcodeProviderBridge)(nil)

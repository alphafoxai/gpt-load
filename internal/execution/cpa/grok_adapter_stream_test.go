package cpa

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"gpt-load/internal/channel"
	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
	"gpt-load/internal/subscription/providers/grok"
)

type fakeGrokStreamExecutor struct {
	stream *grok.ExecuteStreamResponse
}

func (*fakeGrokStreamExecutor) Execute(context.Context, string, grok.Credential, grok.ExecuteRequest) (grok.ExecuteResponse, error) {
	return grok.ExecuteResponse{}, nil
}

func (*fakeGrokStreamExecutor) CountTokens(context.Context, grok.ExecuteRequest) (grok.ExecuteResponse, error) {
	return grok.ExecuteResponse{}, nil
}

func (executor *fakeGrokStreamExecutor) ExecuteStream(context.Context, string, grok.Credential, grok.ExecuteRequest) (*grok.ExecuteStreamResponse, error) {
	return executor.stream, nil
}

func TestGrokAdapterGroupsNativeResponsesSSELinesIntoCompleteEvents(t *testing.T) {
	canonical, err := grok.MarshalCredential(grok.Credential{
		Type: grok.Provider, AccessToken: "access", RefreshToken: "refresh", AccountID: "xai-account",
		Email: "grok@example.com", Expire: "2030-01-01T00:00:00Z", TokenEndpoint: "https://auth.x.ai/oauth/token",
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, _, _, keyService, row := newSubscriptionAdapterFixture(t, string(channel.Grok), canonical, "xai-account")
	bridge, ok := adapter.providers[channel.ProviderGrok].(*grokProviderBridge)
	if !ok {
		t.Fatal("Grok provider bridge is unavailable")
	}
	chunks := make(chan grok.ExecuteStreamChunk, 2)
	chunks <- grok.ExecuteStreamChunk{Payload: []byte("event: response.completed")}
	chunks <- grok.ExecuteStreamChunk{Payload: []byte(`data: {"type":"response.completed","response":{"id":"resp_1"}}`)}
	close(chunks)
	bridge.executor = &fakeGrokStreamExecutor{stream: &grok.ExecuteStreamResponse{Chunks: chunks}}

	plaintext, err := keyService.Decrypt(row.Data)
	if err != nil {
		t.Fatal(err)
	}
	spec := execution.NewAttemptSpec(execution.AttemptSpec{
		RequestID: "request-1", AttemptID: "attempt-1", Sequence: 1,
		ChannelID: string(channel.Grok), RouteMode: execution.RouteNative,
		ClientProtocol: protocol.OpenAIResponses, Operation: execution.OperationResponsesCreate,
		ClientModel: "grok-4.3", UpstreamModel: "grok-4.3", Method: http.MethodPost, Path: "/v1/responses",
		Body:       []byte(`{"model":"grok-4.3","input":"hi"}`),
		Credential: execution.NewCredentialSnapshot(row.ID, row.SecretVersion, 1, []byte(plaintext)),
	})
	var wire string
	result := adapter.ExecuteStream(t.Context(), spec, func(event execution.StreamEvent) error {
		if event.Kind == execution.StreamEventData {
			wire += string(event.Data)
		}
		return nil
	})
	if result.Error != nil {
		t.Fatalf("ExecuteStream() error = %#v", result.Error)
	}
	want := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\"}}\n\n"
	if wire != want {
		t.Fatalf("wire = %q, want %q", wire, want)
	}
}

func TestGrokAdapterFinishesNativeResponsesAfterUpstreamEOF(t *testing.T) {
	canonical, err := grok.MarshalCredential(grok.Credential{
		Type: grok.Provider, AccessToken: "access", RefreshToken: "refresh", AccountID: "xai-account",
		Email: "grok@example.com", Expire: "2030-01-01T00:00:00Z", TokenEndpoint: "https://auth.x.ai/oauth/token",
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, _, _, keyService, row := newSubscriptionAdapterFixture(t, string(channel.Grok), canonical, "xai-account")
	bridge, ok := adapter.providers[channel.ProviderGrok].(*grokProviderBridge)
	if !ok {
		t.Fatal("Grok provider bridge is unavailable")
	}
	chunks := make(chan grok.ExecuteStreamChunk, 3)
	chunks <- grok.ExecuteStreamChunk{Payload: []byte("event: response.output_text.delta")}
	chunks <- grok.ExecuteStreamChunk{Payload: []byte(`data: {"type":"response.output_text.delta","delta":"hi"}`)}
	chunks <- grok.ExecuteStreamChunk{Err: io.ErrUnexpectedEOF}
	close(chunks)
	bridge.executor = &fakeGrokStreamExecutor{stream: &grok.ExecuteStreamResponse{Chunks: chunks}}

	plaintext, err := keyService.Decrypt(row.Data)
	if err != nil {
		t.Fatal(err)
	}
	spec := execution.NewAttemptSpec(execution.AttemptSpec{
		RequestID: "request-1", AttemptID: "attempt-1", Sequence: 1,
		ChannelID: string(channel.Grok), RouteMode: execution.RouteNative,
		ClientProtocol: protocol.OpenAIResponses, Operation: execution.OperationResponsesCreate,
		ClientModel: "grok-4.6", UpstreamModel: "grok-4.6", Method: http.MethodPost, Path: "/v1/responses",
		Body:       []byte(`{"model":"grok-4.6","input":"hi"}`),
		Credential: execution.NewCredentialSnapshot(row.ID, row.SecretVersion, 1, []byte(plaintext)),
	})
	var events []string
	result := adapter.ExecuteStream(t.Context(), spec, func(event execution.StreamEvent) error {
		events = append(events, string(event.Kind)+":"+string(event.Data))
		return nil
	})
	if result.Error == nil || result.Error.Code != "upstream_stream_truncated" || result.StatusCode != http.StatusOK {
		t.Fatalf("result = %#v events=%q", result, events)
	}
	wire := strings.Join(events, "\n")
	if !strings.Contains(wire, `"delta":"hi"`) || !strings.Contains(wire, "event: response.incomplete") {
		t.Fatalf("events = %q", events)
	}
}

var _ grok.Executor = (*fakeGrokStreamExecutor)(nil)

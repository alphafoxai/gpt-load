package qoder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	_ "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator/builtin"

	"github.com/tidwall/gjson"
)

type upstreamError struct {
	status  int
	summary string
}

func (e *upstreamError) Error() string {
	if e == nil || e.summary == "" {
		return "Qoder upstream request failed"
	}
	return e.summary
}

func (e *upstreamError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.status
}

type httpClientKey struct{}

func WithHTTPClient(ctx context.Context, client *http.Client) context.Context {
	return context.WithValue(ctx, httpClientKey{}, client)
}

func httpClientFromContext(ctx context.Context) *http.Client {
	client, err := httpClientFrom(ctx, "")
	if err != nil || client == nil {
		return &http.Client{Timeout: 30 * time.Second}
	}
	return client
}

func httpClientFrom(ctx context.Context, proxyURL string) (*http.Client, error) {
	if proxyURL == "" {
		if ctx != nil {
			if client, ok := ctx.Value(httpClientKey{}).(*http.Client); ok && client != nil {
				return client, nil
			}
		}
		return &http.Client{}, nil
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("qoder proxy is invalid")
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("qoder proxy transport is unavailable")
	}
	transport := base.Clone()
	transport.Proxy = http.ProxyURL(parsed)
	return &http.Client{Transport: transport}, nil
}

var (
	listingMu    sync.Mutex
	listingCache = map[string]listingEntry{}
)

const listingTTL = 5 * time.Minute

func lookupModel(ctx context.Context, client *http.Client, cred Credential, name string) modelSpec {
	fallback := fallbackModel(name)
	s, ok := siteByID(cred.Site)
	if !ok || name == "" {
		return fallback
	}
	key := cred.Site + "\x00" + cred.UID
	listingMu.Lock()
	cached, fresh := listingCache[key]
	if fresh && time.Since(cached.at) < listingTTL {
		listingMu.Unlock()
		if spec, ok := specByName(cached.byKey, name); ok {
			return spec
		}
		return fallback
	}
	listingMu.Unlock()
	fetched, err := fetchListing(ctx, client, s, cred)
	if err != nil {
		return fallback
	}
	listingMu.Lock()
	listingCache[key] = listingEntry{at: time.Now(), byKey: fetched}
	listingMu.Unlock()
	if spec, ok := specByName(fetched, name); ok {
		return spec
	}
	return fallback
}

func fetchListing(ctx context.Context, client *http.Client, s site, cred Credential) (map[string]modelSpec, error) {
	endpoint := s.modelsURL()
	headers, err := cosyHeaders(endpoint, cred.user(), "", time.Now().Unix())
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &upstreamError{status: resp.StatusCode, summary: bounded(string(raw), resp.Status)}
	}
	parsed := parseListing(raw)
	if parsed == nil {
		return nil, fmt.Errorf("qoder model list is invalid")
	}
	return parsed, nil
}

// Execute sends one chat completion through Qoder's agent SSE and returns it
// in the caller's format. Streaming chunks are unframed JSON, plus a final
// [DONE] marker, which the execution adapter frames.
func Execute(ctx context.Context, cred Credential, format string, payload []byte, proxyURL string, stream bool) ([]byte, http.Header, <-chan []byte, error) {
	clientFormat := formatFrom(format)
	openAIPayload := append([]byte(nil), payload...)
	if clientFormat != sdktranslator.FormatOpenAI {
		openAIPayload = sdktranslator.TranslateRequest(clientFormat, sdktranslator.FormatOpenAI, gjson.GetBytes(payload, "model").String(), payload, stream)
	}
	var chat openAIChat
	if err := json.Unmarshal(openAIPayload, &chat); err != nil {
		return nil, nil, nil, fmt.Errorf("qoder request is not a chat completion")
	}
	if strings.TrimSpace(chat.Model) == "" {
		return nil, nil, nil, fmt.Errorf("qoder request is missing a model")
	}
	httpClient, err := httpClientFrom(ctx, proxyURL)
	if err != nil {
		return nil, nil, nil, err
	}
	model := lookupModel(ctx, httpClient, cred, chat.Model)
	plain, err := qoderBody(chat, openAIPayload, model, time.Now())
	if err != nil {
		return nil, nil, nil, err
	}
	s, ok := siteByID(cred.Site)
	if !ok {
		return nil, nil, nil, fmt.Errorf("qoder site is unknown")
	}
	wire := encodeRequestBody(plain)
	endpoint := s.chatURL()
	headers, err := cosyHeaders(endpoint, cred.user(), wire, time.Now().Unix())
	if err != nil {
		return nil, nil, nil, err
	}
	headers["Accept"] = "text/event-stream"
	headers["Cache-Control"] = "no-cache"
	headers["X-Model-Key"] = model.Key
	headers["X-Model-Source"] = model.Source
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(wire))
	if err != nil {
		return nil, nil, nil, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		status, message := failureStatus(resp.StatusCode, string(raw)), failureMessage(resp.StatusCode, string(raw))
		return nil, nil, nil, &upstreamError{status: status, summary: bounded(message, resp.Status)}
	}
	if !stream {
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return nil, nil, nil, err
		}
		body, _, err := openAICompletion(chat.Model, "chatcmpl-"+hexID(), time.Now().Unix(), readEvents(bytes.NewReader(raw)))
		if err != nil {
			return nil, nil, nil, err
		}
		body = translateBack(ctx, clientFormat, payload, openAIPayload, body)
		return body, resp.Header.Clone(), nil, nil
	}
	chunks := make(chan []byte)
	go func() {
		defer close(chunks)
		defer resp.Body.Close()
		events := readEvents(resp.Body)
		pieces, _, err := streamChunks(chat.Model, "chatcmpl-"+hexID(), time.Now().Unix(), events)
		if err != nil {
			raw, _ := json.Marshal(map[string]any{"error": map[string]string{"message": err.Error()}})
			select {
			case chunks <- translateChunk(ctx, clientFormat, payload, openAIPayload, raw):
			case <-ctx.Done():
			}
			return
		}
		var param any
		for _, piece := range pieces {
			out := piece
			if clientFormat != sdktranslator.FormatOpenAI && !bytes.Equal(piece, []byte("[DONE]")) {
				converted := sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, clientFormat, chat.Model, payload, openAIPayload, piece, &param)
				for _, item := range converted {
					if len(item) == 0 {
						continue
					}
					select {
					case chunks <- item:
					case <-ctx.Done():
						return
					}
				}
				continue
			}
			select {
			case chunks <- out:
			case <-ctx.Done():
				return
			}
		}
	}()
	return nil, resp.Header.Clone(), chunks, nil
}

func translateBack(ctx context.Context, clientFormat sdktranslator.Format, original, translated, body []byte) []byte {
	if clientFormat == sdktranslator.FormatOpenAI {
		return body
	}
	var param any
	return sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatOpenAI, clientFormat, "", original, translated, body, &param)
}

func translateChunk(ctx context.Context, clientFormat sdktranslator.Format, original, translated, body []byte) []byte {
	if clientFormat == sdktranslator.FormatOpenAI {
		return body
	}
	var param any
	converted := sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, clientFormat, "", original, translated, body, &param)
	if len(converted) == 0 {
		return body
	}
	return converted[0]
}

func formatFrom(value string) sdktranslator.Format {
	switch value {
	case string(sdktranslator.FormatOpenAIResponse):
		return sdktranslator.FormatOpenAIResponse
	case string(sdktranslator.FormatGemini):
		return sdktranslator.FormatGemini
	case string(sdktranslator.FormatClaude):
		return sdktranslator.FormatClaude
	default:
		return sdktranslator.FormatOpenAI
	}
}

func bounded(body, status string) string {
	body = strings.Join(strings.Fields(body), " ")
	if body == "" {
		return "Qoder upstream returned " + status
	}
	if len(body) > 512 {
		body = body[:512]
	}
	return body
}

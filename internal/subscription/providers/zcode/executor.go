package zcode

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	_ "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator/builtin"
)

const anthropicVersion = "2023-06-01"

type upstreamError struct {
	status  int
	summary string
}

func (e *upstreamError) Error() string {
	if e == nil || e.summary == "" {
		return "ZCode upstream request failed"
	}
	return e.summary
}

func (e *upstreamError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.status
}

// Execute posts one Anthropic Messages request and converts the response back
// to the caller's format. Coding-plan keys are sent as both x-api-key and
// Authorization, matching ZCode's own request.
func Execute(ctx context.Context, credential Credential, format string, payload []byte, headers http.Header, proxyURL string, stream bool) ([]byte, http.Header, <-chan []byte, error) {
	upstreamFormat := sdktranslator.FormatClaude
	clientFormat := formatFrom(format)
	requestBody := append([]byte(nil), payload...)
	if clientFormat != upstreamFormat {
		requestBody = sdktranslator.TranslateRequest(clientFormat, upstreamFormat, "", requestBody, stream)
	}
	endpoint, err := messagesURL(credential.BaseURL)
	if err != nil {
		return nil, nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return nil, nil, nil, err
	}
	copySafeHeaders(req.Header, headers)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("x-api-key", credential.APIKey)
	req.Header.Set("Authorization", "Bearer "+credential.APIKey)
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	httpClient, err := newUpstreamClient(proxyURL)
	if err != nil {
		return nil, nil, nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, nil, nil, &upstreamError{status: resp.StatusCode, summary: bounded(string(raw), resp.Status)}
	}
	if !stream {
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return nil, nil, nil, err
		}
		if clientFormat != upstreamFormat {
			var param any
			raw = sdktranslator.TranslateNonStream(ctx, upstreamFormat, clientFormat, "", payload, requestBody, raw, &param)
		}
		return raw, resp.Header.Clone(), nil, nil
	}
	chunks := make(chan []byte)
	go func() {
		defer close(chunks)
		defer resp.Body.Close()
		forwardStream(ctx, resp.Body, chunks, clientFormat, upstreamFormat, payload, requestBody)
	}()
	return nil, resp.Header.Clone(), chunks, nil
}

func forwardStream(ctx context.Context, body io.Reader, chunks chan<- []byte, clientFormat, upstreamFormat sdktranslator.Format, original, translated []byte) {
	var param any
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	var frame []string
	flush := func() {
		if len(frame) == 0 {
			return
		}
		raw := strings.Join(frame, "\n")
		frame = nil
		if clientFormat == upstreamFormat {
			select {
			case chunks <- []byte(raw):
			case <-ctx.Done():
			}
			return
		}
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "" || data == "[DONE]" {
				continue
			}
			for _, converted := range sdktranslator.TranslateStream(ctx, upstreamFormat, clientFormat, "", original, translated, []byte(data), &param) {
				if len(converted) == 0 {
					continue
				}
				select {
				case chunks <- converted:
				case <-ctx.Done():
					return
				}
			}
		}
	}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		frame = append(frame, line)
	}
	flush()
}

var newUpstreamClient = func(proxyURL string) (*http.Client, error) {
	client := &http.Client{}
	if proxyURL == "" {
		return client, nil
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("zcode proxy: %w", err)
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("zcode proxy transport is unavailable")
	}
	transport := base.Clone()
	transport.Proxy = http.ProxyURL(parsed)
	client.Transport = transport
	return client, nil
}

func messagesURL(base string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", fmt.Errorf("zcode base URL is invalid")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v1/messages"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
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

func copySafeHeaders(dst, src http.Header) {
	for name, values := range src {
		lower := strings.ToLower(name)
		switch lower {
		case "authorization", "x-api-key", "cookie", "host", "content-length", "accept-encoding":
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

func bounded(body, status string) string {
	body = strings.Join(strings.Fields(body), " ")
	if body == "" {
		return "ZCode upstream returned " + status
	}
	if len(body) > 512 {
		body = body[:512]
	}
	return body
}
